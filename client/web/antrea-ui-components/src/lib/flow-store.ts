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

import { Flow, FlowEndReason, connectionKey } from './flow-types.js';

const DEFAULT_MAX_ENTRIES = 10_000;
// Allow several of the Flow Aggregator's usual one-minute reports before aging out a connection.
export const LIVE_FLOW_WINDOW_MS = 3 * 60_000;

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

export interface FlowEntry {
    key: string;
    flow: Flow;
    firstSeen: number;
    lastSeen: number;
    // samples holds recent (endTs, cumulative-bytes) observations within RATE_WINDOW_MS,
    // used to derive the sliding-window bit rate (see entryBitRate).
    samples: FlowSample[];
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
// resets (a new connection reusing the same 5-tuple) and pruning anything older
// than the sliding window relative to the newest sample.
function nextSamples(existing: FlowSample[], flow: Flow, endMs: number): FlowSample[] {
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

// These are final exports. An active timeout is a periodic report for a continuing flow.
function isFinalReport(reason: FlowEndReason): boolean {
    return reason === FlowEndReason.IdleTimeout || reason === FlowEndReason.EndOfFlow ||
        reason === FlowEndReason.ForcedEnd || reason === FlowEndReason.LackOfResources;
}

/** One entry per transport five-tuple, bounded by capacity and, for live queries, by report age. */
export class FlowStore {
    private entries = new Map<string, FlowEntry & { expiresAt: number }>();
    // In LRU order so a burst of completed connections cannot displace continuing ones.
    private finished = new Set<string>();
    private nextExpiry = Infinity;
    private evictionCount = 0;
    private evictionWarningUntil = 0;

    constructor(private readonly maxEntries = DEFAULT_MAX_ENTRIES) {}

    upsert(flow: Flow, finite = false): void {
        this.upsertBatch([flow], finite);
    }

    upsertBatch(flows: Flow[], finite = false): void {
        const now = Date.now();
        if (!finite) this.expireLiveEntries(now);
        for (const flow of flows) {
            const end = Date.parse(flow.endTs);
            // Replayed history ages from its report time. Fall back to receipt time if the
            // timestamp is missing or ahead of the browser, so a record cannot live forever.
            const reportedAt = Number.isFinite(end) ? Math.min(end, now) : now;
            const expiresAt = finite ? Infinity : reportedAt + LIVE_FLOW_WINDOW_MS;
            if (expiresAt <= now) continue;

            const key = connectionKey(flow);
            const existing = this.entries.get(key);
            const final = !finite && isFinalReport(flow.endReason);
            if (existing) {
                const previousEnd = Date.parse(existing.flow.endTs);
                // Reconnects can replay an older report, including a periodic report for a
                // connection whose final export we already have.
                if (previousEnd > end || (previousEnd === end && this.finished.has(key) && !final)) continue;
                this.entries.delete(key);
                this.finished.delete(key);
            }
            this.entries.set(key, {
                key, flow, expiresAt,
                firstSeen: existing?.firstSeen ?? now,
                lastSeen: now,
                samples: finite ? [] : nextSamples(existing?.samples ?? [], flow, end),
            });
            if (final) this.finished.add(key);
            this.nextExpiry = Math.min(this.nextExpiry, expiresAt);
            if (this.entries.size > this.maxEntries) {
                const evicted = this.finished.values().next().value ?? this.entries.keys().next().value!;
                this.evictionWarningUntil = Math.max(this.evictionWarningUntil, this.entries.get(evicted)!.expiresAt);
                this.entries.delete(evicted);
                this.finished.delete(evicted);
                this.evictionCount++;
            }
        }
    }

    /** Removes expired reports, including when the stream is quiet. Finite results never expire. */
    expireLiveEntries(now = Date.now()): boolean {
        if (now < this.nextExpiry) return false;
        this.nextExpiry = Infinity;
        let removed = false;
        for (const [key, entry] of this.entries) {
            if (entry.expiresAt <= now) {
                this.entries.delete(key);
                this.finished.delete(key);
                removed = true;
            } else {
                this.nextExpiry = Math.min(this.nextExpiry, entry.expiresAt);
            }
        }
        return removed;
    }

    get(key: string): FlowEntry | undefined {
        const entry = this.entries.get(key);
        if (entry) {
            this.entries.delete(key);
            entry.lastSeen = Date.now();
            this.entries.set(key, entry);
            if (this.finished.delete(key)) this.finished.add(key);
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
        return this.evictionWarningUntil > Date.now();
    }

    clear(): void {
        this.entries.clear();
        this.finished.clear();
        this.nextExpiry = Infinity;
        this.evictionCount = 0;
        this.evictionWarningUntil = 0;
    }
}
