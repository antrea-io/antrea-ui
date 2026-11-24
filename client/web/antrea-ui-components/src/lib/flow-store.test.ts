// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { FlowStore, LIVE_FLOW_WINDOW_MS } from './flow-store';
import {
    Flow,
    FlowType,
    FlowEndReason,
    IPVersion,
    NetworkPolicyType,
    NetworkPolicyRuleAction,
    connectionKey,
} from './flow-types';

function makeFlow(dstIP: string, dstPort: number, proto: number = 6): Flow {
    return {
        id: `flow-${dstIP}-${dstPort}`,
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        ip: {
            version: IPVersion.IPv4,
            source: '10.0.0.1',
            destination: dstIP,
        },
        transport: {
            protocolNumber: proto,
            sourcePort: 12345,
            destinationPort: dstPort,
        },
        k8s: {
            flowType: FlowType.InterNode,
            sourcePodNamespace: 'default',
            sourcePodName: 'pod-a',
            sourcePodUid: '',
            sourceNodeName: 'node-1',
            sourceNodeUid: '',
            destinationPodNamespace: 'default',
            destinationPodName: 'pod-b',
            destinationPodUid: '',
            destinationNodeName: 'node-2',
            destinationNodeUid: '',
            destinationClusterIp: '',
            destinationServicePort: 0,
            destinationServicePortName: '',
            destinationServiceUid: '',
            ingressNetworkPolicyType: NetworkPolicyType.Unspecified,
            ingressNetworkPolicyNamespace: '',
            ingressNetworkPolicyName: '',
            ingressNetworkPolicyUid: '',
            ingressNetworkPolicyRuleName: '',
            ingressNetworkPolicyRuleAction: NetworkPolicyRuleAction.NoAction,
            egressNetworkPolicyType: NetworkPolicyType.Unspecified,
            egressNetworkPolicyNamespace: '',
            egressNetworkPolicyName: '',
            egressNetworkPolicyUid: '',
            egressNetworkPolicyRuleName: '',
            egressNetworkPolicyRuleAction: NetworkPolicyRuleAction.NoAction,
            egressName: '',
            egressIp: '',
            egressNodeName: '',
            egressNodeUid: '',
            egressUid: '',
        },
        stats: { packetTotalCount: 0, packetDeltaCount: 0, octetTotalCount: 0, octetDeltaCount: 0 },
        reverseStats: { packetTotalCount: 0, packetDeltaCount: 0, octetTotalCount: 0, octetDeltaCount: 0 },
    };
}

beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-03-25T00:01:10Z'));
});
afterEach(() => { vi.useRealTimers(); });

describe('FlowStore', () => {
    test('upsert inserts a new entry', () => {
        const store = new FlowStore();
        store.upsert(makeFlow('10.0.0.2', 80));
        expect(store.size()).toBe(1);
    });

    test('a finite query keeps the record with the latest endTs whatever the arrival order', () => {
        const store = new FlowStore();
        const newer = makeFlow('10.0.0.2', 80);
        newer.id = 'newer';
        newer.endTs = '2026-03-25T00:05:00Z';
        const older = makeFlow('10.0.0.2', 80);
        older.id = 'older';

        store.upsertBatch([newer, older], true);

        expect(store.getAll().map(e => e.flow.id)).toEqual(['newer']);
    });

    test('a finite query still replaces a record with an equal or newer endTs', () => {
        const store = new FlowStore();
        const first = makeFlow('10.0.0.2', 80);
        const second = makeFlow('10.0.0.2', 80);
        second.id = 'second';

        store.upsertBatch([first, second], true);

        expect(store.getAll().map(e => e.flow.id)).toEqual(['second']);
    });

    test('replaying an earlier or simultaneous active report cannot reopen a finished connection', () => {
        const store = new FlowStore(1);
        const active = { ...makeFlow('10.0.0.2', 80), endReason: FlowEndReason.ActiveTimeout };
        const final = { ...active, endTs: new Date().toISOString(), endReason: FlowEndReason.EndOfFlow };
        store.upsertBatch([final, active, { ...active, endTs: final.endTs }]);
        expect(store.getAll().map(e => e.flow)).toEqual([final]);
    });

    test('live backfill outside the window consumes no capacity and causes no eviction warning', () => {
        const store = new FlowStore();
        const recent = makeFlow('10.0.0.2', 80);
        const dns = makeFlow('10.0.0.3', 53, 17);
        const endTs = new Date(Date.now() - LIVE_FLOW_WINDOW_MS).toISOString();
        const old = Array.from({ length: 10_001 }, (_, i) => ({
            ...dns, endTs, transport: { ...dns.transport, sourcePort: 32_768 + i },
        }));
        store.upsertBatch([...old, recent]);
        store.upsertBatch(old);
        expect(store.getAll().map(e => e.flow)).toEqual([recent]);
        expect(store.hasEvicted()).toBe(false);
    });

    test('a new report extends retention, while reading a connection does not', () => {
        const store = new FlowStore();
        const first = makeFlow('10.0.0.2', 80);
        const second = makeFlow('10.0.0.3', 443);
        store.upsertBatch([first, second]);
        vi.advanceTimersByTime(60_000);
        const refreshed = { ...second, endTs: new Date().toISOString() };
        store.upsert(refreshed);
        store.get(connectionKey(first));
        vi.setSystemTime(Date.parse(first.endTs) + LIVE_FLOW_WINDOW_MS);
        expect(store.expireLiveEntries()).toBe(true);
        expect(store.getAll().map(e => e.flow)).toEqual([refreshed]);
        expect(store.hasEvicted()).toBe(false);
    });

    test('a live batch frees expired entries before enforcing the limit', () => {
        const store = new FlowStore(1);
        store.upsert(makeFlow('10.0.0.2', 80));
        vi.advanceTimersByTime(LIVE_FLOW_WINDOW_MS);
        const recent = { ...makeFlow('10.0.0.3', 443), endTs: new Date().toISOString() };
        store.upsert(recent);
        expect(store.getAll().map(e => e.flow)).toEqual([recent]);
        expect(store.getEvictionCount()).toBe(0);
    });

    test.each([FlowEndReason.IdleTimeout, FlowEndReason.EndOfFlow, FlowEndReason.ForcedEnd, FlowEndReason.LackOfResources])(
        'final exports (%s) leave room for continuing connections between periodic reports', endReason => {
            const store = new FlowStore(2);
            const active = { ...makeFlow('10.0.0.2', 80), endReason: FlowEndReason.ActiveTimeout };
            store.upsert(active);
            for (let port = 1; port <= 10; port++) store.upsert({ ...makeFlow('10.0.0.3', port), endReason });
            const next = { ...makeFlow('10.0.0.4', 443), endReason: FlowEndReason.ActiveTimeout };
            store.upsert(next);
            store.upsert({ ...makeFlow('10.0.0.5', 443), endReason });
            expect(store.getAll().map(e => e.flow)).toEqual([active, next]);
            expect(store.hasEvicted()).toBe(true);
            vi.advanceTimersByTime(60_000);
            store.upsert({ ...active, endTs: new Date().toISOString() });
            expect(store.get(connectionKey(active))!.samples).toHaveLength(2);
            vi.setSystemTime(Date.parse(active.endTs) + LIVE_FLOW_WINDOW_MS);
            store.expireLiveEntries();
            expect(store.getAll().map(e => e.key)).toEqual([connectionKey(active)]);
            expect(store.hasEvicted()).toBe(false);
        },
    );

    test('finite results and their capacity warning survive the live window', () => {
        const store = new FlowStore(1);
        const historic = { ...makeFlow('10.0.0.3', 443), endTs: '2025-01-01T00:00:00Z' };
        store.upsertBatch([makeFlow('10.0.0.2', 80), historic], true);
        vi.advanceTimersByTime(LIVE_FLOW_WINDOW_MS * 2);
        expect(store.expireLiveEntries()).toBe(false);
        expect(store.getAll().map(e => e.flow)).toEqual([historic]);
        expect(store.hasEvicted()).toBe(true);
    });

    test('upsert with same connection key deduplicates', () => {
        const store = new FlowStore();
        const flow1 = makeFlow('10.0.0.2', 80);
        const flow2 = makeFlow('10.0.0.2', 80);
        flow2.id = 'updated-flow';

        store.upsert(flow1);
        store.upsert(flow2);

        expect(store.size()).toBe(1);
        const entries = store.getAll();
        expect(entries[0].flow.id).toBe('updated-flow');
    });

    test.each([false, true])('distinct source ports retain separate reports (finite=%s)', finite => {
        const store = new FlowStore();
        const first = makeFlow('10.0.0.2', 443);
        const second = makeFlow('10.0.0.2', 443);
        second.transport.sourcePort = 54321;
        store.upsertBatch([first, second, second], finite);
        expect(store.getAll().map(entry => entry.flow.transport.sourcePort)).toEqual([12345, 54321]);
    });

    test('upsert with different key grows the store', () => {
        const store = new FlowStore();
        store.upsert(makeFlow('10.0.0.2', 80));
        store.upsert(makeFlow('10.0.0.3', 443));
        expect(store.size()).toBe(2);
    });

    test('upsertBatch inserts multiple entries', () => {
        const store = new FlowStore();
        store.upsertBatch([
            makeFlow('10.0.0.2', 80),
            makeFlow('10.0.0.3', 443),
            makeFlow('10.0.0.4', 8080),
        ]);
        expect(store.size()).toBe(3);
    });

    test('LRU eviction when exceeding maxEntries', () => {
        const store = new FlowStore(3);
        const flowA = makeFlow('10.0.0.2', 80);
        const flowB = makeFlow('10.0.0.3', 443);
        const flowC = makeFlow('10.0.0.4', 8080);
        const flowD = makeFlow('10.0.0.5', 9090);

        store.upsert(flowA);
        store.upsert(flowB);
        store.upsert(flowC);
        expect(store.size()).toBe(3);
        expect(store.hasEvicted()).toBe(false);

        store.upsert(flowD);
        expect(store.size()).toBe(3);
        expect(store.hasEvicted()).toBe(true);
        expect(store.getEvictionCount()).toBe(1);

        const keys = store.getAll().map(e => e.key);
        expect(keys).not.toContain(connectionKey(flowA));
        expect(keys).toContain(connectionKey(flowD));
    });

    test('get promotes entry for LRU', () => {
        const store = new FlowStore(3);
        const flowA = makeFlow('10.0.0.2', 80);
        const flowB = makeFlow('10.0.0.3', 443);
        const flowC = makeFlow('10.0.0.4', 8080);
        const flowD = makeFlow('10.0.0.5', 9090);

        store.upsert(flowA);
        store.upsert(flowB);
        store.upsert(flowC);

        const keyA = connectionKey(flowA);
        store.get(keyA);

        store.upsert(flowD);

        const keys = store.getAll().map(e => e.key);
        const keyB = connectionKey(flowB);
        expect(keys).not.toContain(keyB);
        expect(keys).toContain(keyA);
    });

    test('clear resets the store', () => {
        const store = new FlowStore(3);
        store.upsert(makeFlow('10.0.0.2', 80));
        store.upsert(makeFlow('10.0.0.3', 443));

        store.upsert(makeFlow('10.0.0.4', 8080));
        store.upsert(makeFlow('10.0.0.5', 9090));
        expect(store.hasEvicted()).toBe(true);

        store.clear();
        expect(store.size()).toBe(0);
        expect(store.getEvictionCount()).toBe(0);
        expect(store.hasEvicted()).toBe(false);
        expect(store.getAll()).toEqual([]);
    });

    test('getAll returns all entries', () => {
        const store = new FlowStore();
        store.upsert(makeFlow('10.0.0.2', 80));
        store.upsert(makeFlow('10.0.0.3', 443));

        const entries = store.getAll();
        expect(entries).toHaveLength(2);
        expect(entries[0].key).toBeDefined();
        expect(entries[0].flow).toBeDefined();
        expect(entries[0].lastSeen).toBeGreaterThan(0);
    });

    test('get returns undefined for missing key', () => {
        const store = new FlowStore();
        expect(store.get('nonexistent')).toBeUndefined();
    });
});


test('finite query snapshots preserve the latest total without becoming live rate samples', () => {
    const store = new FlowStore();
    const first = makeFlow('10.0.0.2', 80);
    first.stats.octetTotalCount = 100;
    store.upsert(first, true);
    const last = { ...first, endTs: '2026-03-25T00:02:00Z', stats: { ...first.stats, octetTotalCount: 500 } };
    store.upsert(last, true);
    expect(store.getAll()).toHaveLength(1);
    expect(store.getAll()[0].flow.stats.octetTotalCount).toBe(500);
    expect(store.getAll()[0].samples).toEqual([]);
});
