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

import { Flow, connectionKey } from './flow-types';

const DEFAULT_MAX_ENTRIES = 10_000;

export interface FlowEntry {
    key: string;
    flow: Flow;
    firstSeen: number;
    lastSeen: number;
}

/**
 * Bounded in-memory flow store with LRU eviction.
 * Deduplicates flows by connection key (5-tuple with source port masked).
 * When the store exceeds maxEntries, the oldest entries are evicted.
 */
export class FlowStore {
    private entries: Map<string, FlowEntry> = new Map();
    private maxEntries: number;
    private evictionCount = 0;

    constructor(maxEntries: number = DEFAULT_MAX_ENTRIES) {
        this.maxEntries = maxEntries;
    }

    upsert(flow: Flow): void {
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

    upsertBatch(flows: Flow[]): void {
        for (const flow of flows) {
            this.upsert(flow);
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
        this.evictionCount = 0;
    }
}
