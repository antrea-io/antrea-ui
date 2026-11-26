/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { Flow, connectionKey } from './flow-types.js';

const DEFAULT_MAX_ENTRIES = 10_000;

// RATE_WINDOW_MS is the length of the sliding window used to compute the bit rate.
// It is measured against each flow's own endTs (cluster clock), not the wall-clock time
// at which records are received, so the gRPC stream backfilling many historical records
// at once on connect does not distort the rate. It is deliberately larger than the Flow
// Aggregator's active flow timeout (~60s) so a steady flow keeps at least two samples in
// the window.
const RATE_WINDOW_MS = 120_000;

// MAX_SAMPLES caps the per-connection sample history to bound memory.
const MAX_SAMPLES = 32;

// FlowSample is a point-in-time observation of a connection's cumulative byte count,
// timestamped by the flow record's endTs (in ms).
export interface FlowSample {
    t: number;
    bytes: number;
}

// FlowCounters are the traffic counters of one connection key, forward and reverse.
interface FlowCounters {
    packets: number;
    octets: number;
    reversePackets: number;
    reverseOctets: number;
}

export interface FlowEntry {
    key: string;
    flow: Flow;
    firstSeen: number;
    lastSeen: number;
    // samples holds recent (endTs, cumulative-bytes) observations within RATE_WINDOW_MS,
    // used to derive the sliding-window bit rate (see entryBitRate).
    samples: FlowSample[];
    // deltaSums is the sum of the delta counters of every record received for the key in a finite
    // query, older ones included. Unset for a live stream.
    deltaSums?: FlowCounters;
}

// hasTotals tells whether a record carries cumulative totals. The Flow Aggregator's do; a source
// that streams records newest first may send deltas only.
function hasTotals(flow: Flow): boolean {
    return flow.stats.packetTotalCount > 0 || flow.stats.octetTotalCount > 0 ||
        flow.reverseStats.packetTotalCount > 0 || flow.reverseStats.octetTotalCount > 0;
}

function addDeltas(sums: FlowCounters | undefined, flow: Flow): FlowCounters {
    return {
        packets: (sums?.packets ?? 0) + flow.stats.packetDeltaCount,
        octets: (sums?.octets ?? 0) + flow.stats.octetDeltaCount,
        reversePackets: (sums?.reversePackets ?? 0) + flow.reverseStats.packetDeltaCount,
        reverseOctets: (sums?.reverseOctets ?? 0) + flow.reverseStats.octetDeltaCount,
    };
}

// withTotals returns a copy of the record whose totals are the sums, so that every reader of a
// record's totals shows them. The received record is not modified.
function withTotals(flow: Flow, sums: FlowCounters): Flow {
    return {
        ...flow,
        stats: { ...flow.stats, packetTotalCount: sums.packets, octetTotalCount: sums.octets },
        reverseStats: {
            ...flow.reverseStats,
            packetTotalCount: sums.reversePackets,
            octetTotalCount: sums.reverseOctets,
        },
    };
}

// endMs is a record's end time in ms; one that cannot be parsed counts as the oldest.
function endMs(flow: Flow): number {
    const ms = Date.parse(flow.endTs);
    return isNaN(ms) ? -Infinity : ms;
}

// A heap item names an entry by the end time it had when pushed. It is stale once the entry has
// been removed or has a different end time.
interface HeapItem {
    ms: number;
    key: string;
}

// entryBitRate returns the connection's throughput in bits/sec over the sliding window,
// computed as the change in cumulative bytes between the oldest and newest samples in the
// window divided by the elapsed time between them. Returns 0 when there is not yet enough
// history (fewer than two samples) or no traffic in the window.
export function entryBitRate(entry: FlowEntry): number {
    const samples = entry.samples;
    if (samples.length < 2) {
        return 0;
    }
    const oldest = samples[0];
    const newest = samples[samples.length - 1];
    const dtSec = (newest.t - oldest.t) / 1000;
    if (dtSec <= 0) {
        return 0;
    }
    const dBytes = newest.bytes - oldest.bytes;
    if (dBytes <= 0) {
        return 0;
    }
    return (dBytes * 8) / dtSec;
}

// nextSamples appends a new observation to the existing sample history, handling counter
// resets (a new connection reusing the same masked 5-tuple key) and pruning anything older
// than the sliding window relative to the newest sample.
function nextSamples(existing: FlowSample[], flow: Flow): FlowSample[] {
    const endMs = Date.parse(flow.endTs);
    if (isNaN(endMs)) {
        return existing;
    }
    const bytes = flow.stats.octetTotalCount + flow.reverseStats.octetTotalCount;

    let samples = existing;
    const last = samples[samples.length - 1];
    if (last && (bytes < last.bytes || endMs < last.t)) {
        // Cumulative counter went backwards in value or time: treat as a new flow and
        // restart the history.
        samples = [];
    }

    const tail = samples[samples.length - 1];
    if (!tail || endMs > tail.t) {
        samples = [...samples, { t: endMs, bytes }];
    } else {
        // Same endTs as the last sample (duplicate/updated record): replace it.
        samples = [...samples.slice(0, -1), { t: endMs, bytes }];
    }

    const newestT = samples[samples.length - 1].t;
    samples = samples.filter((s) => newestT - s.t <= RATE_WINDOW_MS);
    if (samples.length > MAX_SAMPLES) {
        samples = samples.slice(samples.length - MAX_SAMPLES);
    }
    return samples;
}

/**
 * Bounded in-memory flow store.
 * Deduplicates flows by connection key (5-tuple with source port masked).
 * When the store exceeds maxEntries, a live stream evicts the entry that was used least recently,
 * and a finite query the one with the oldest end time, whatever order the records arrive in.
 */
export class FlowStore {
    private entries: Map<string, FlowEntry> = new Map();
    private maxEntries: number;
    private evictionCount = 0;
    // A min-heap on end time, kept only for a finite query, with stale items removed lazily.
    private heap: HeapItem[] = [];

    constructor(maxEntries: number = DEFAULT_MAX_ENTRIES) {
        this.maxEntries = maxEntries;
    }

    // finite marks a snapshot from a finite query, which is not one more sample of a live rate and
    // is handled by upsertFinite: its records arrive in no particular order, and the key drops the
    // source port so several connections share it.
    upsert(flow: Flow, finite = false): void {
        if (finite) {
            this.upsertFinite(flow);
            return;
        }
        const key = connectionKey(flow);
        const existing = this.entries.get(key);
        const now = Date.now();

        if (existing) {
            this.entries.delete(key);
        }

        const entry: FlowEntry = {
            key,
            flow,
            firstSeen: existing ? existing.firstSeen : now,
            lastSeen: now,
            samples: nextSamples(existing ? existing.samples : [], flow),
        };
        this.entries.set(key, entry);

        while (this.entries.size > this.maxEntries) {
            const oldest = this.entries.keys().next();
            if (!oldest.done) {
                this.entries.delete(oldest.value);
                this.evictionCount++;
            }
        }
    }

    // A finite query's records arrive in no particular order: a source may stream the newest
    // first. The entry shows the record with the latest endTs, and, for a source that sends
    // no totals, the sum of the deltas of all the key's records as its totals, which does not
    // depend on the order. When the store is full the entry with the oldest endTs is evicted, so
    // that it keeps the newest connections, and a record of a new key older than all of them is
    // dropped.
    private upsertFinite(flow: Flow): void {
        const key = connectionKey(flow);
        const existing = this.entries.get(key);
        const ms = endMs(flow);

        if (!existing && this.entries.size >= this.maxEntries) {
            const oldest = this.oldest();
            if (oldest && ms < oldest.ms) {
                this.evictionCount++;
                return;
            }
        }

        const sums = addDeltas(existing?.deltaSums, flow);
        let shown = existing && endMs(existing.flow) > ms ? existing.flow : flow;
        if (!hasTotals(flow)) {
            shown = withTotals(shown, sums);
        }
        const shownMs = endMs(shown);
        this.entries.set(key, {
            key,
            flow: shown,
            firstSeen: existing ? existing.firstSeen : Date.now(),
            lastSeen: Date.now(),
            samples: [],
            deltaSums: sums,
        });
        if (!existing || shownMs !== endMs(existing.flow)) {
            this.push({ ms: shownMs, key });
        }

        while (this.entries.size > this.maxEntries) {
            const oldest = this.oldest();
            if (!oldest) break;
            this.entries.delete(oldest.key);
            this.evictionCount++;
        }
    }

    // oldest returns the item of the entry with the oldest endTs, without removing the entry.
    private oldest(): HeapItem | undefined {
        for (;;) {
            const top = this.heap[0];
            if (!top) return undefined;
            const entry = this.entries.get(top.key);
            if (entry && endMs(entry.flow) === top.ms) return top;
            this.pop();
        }
    }

    private push(item: HeapItem): void {
        // Updates leave stale items behind. Rebuilding from the entries keeps the heap's size
        // proportional to the store's.
        if (this.heap.length > 4 * this.maxEntries + 64) {
            this.heap = Array.from(this.entries.values(), e => ({ ms: endMs(e.flow), key: e.key }));
            this.heapify();
            return;
        }
        const heap = this.heap;
        heap.push(item);
        let i = heap.length - 1;
        while (i > 0) {
            const parent = (i - 1) >> 1;
            if (heap[parent].ms <= heap[i].ms) break;
            [heap[parent], heap[i]] = [heap[i], heap[parent]];
            i = parent;
        }
    }

    private pop(): void {
        const heap = this.heap;
        const last = heap.pop();
        if (last && heap.length > 0) {
            heap[0] = last;
            this.siftDown(0);
        }
    }

    private heapify(): void {
        for (let i = (this.heap.length >> 1) - 1; i >= 0; i--) this.siftDown(i);
    }

    private siftDown(start: number): void {
        const heap = this.heap;
        let i = start;
        for (;;) {
            const left = 2 * i + 1;
            const right = left + 1;
            let smallest = i;
            if (left < heap.length && heap[left].ms < heap[smallest].ms) smallest = left;
            if (right < heap.length && heap[right].ms < heap[smallest].ms) smallest = right;
            if (smallest === i) return;
            [heap[smallest], heap[i]] = [heap[i], heap[smallest]];
            i = smallest;
        }
    }

    upsertBatch(flows: Flow[], finite = false): void {
        for (const flow of flows) {
            this.upsert(flow, finite);
        }
    }

    get(key: string): FlowEntry | undefined {
        const entry = this.entries.get(key);
        if (entry) {
            this.entries.delete(key);
            entry.lastSeen = Date.now();
            this.entries.set(key, entry);
        }
        return entry;
    }

    getAll(): FlowEntry[] {
        return Array.from(this.entries.values());
    }

    size(): number {
        return this.entries.size;
    }

    getEvictionCount(): number {
        return this.evictionCount;
    }

    hasEvicted(): boolean {
        return this.evictionCount > 0;
    }

    clear(): void {
        this.entries.clear();
        this.heap = [];
        this.evictionCount = 0;
    }
}
