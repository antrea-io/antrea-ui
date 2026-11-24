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

import { describe, expect, test } from 'vitest';
import { FlowStore } from './flow-store';
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
        older.transport.sourcePort = 54321;

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

    test('a live stream replaces a record even when its endTs is older', () => {
        const store = new FlowStore();
        const newer = makeFlow('10.0.0.2', 80);
        newer.endTs = '2026-03-25T00:05:00Z';
        const older = makeFlow('10.0.0.2', 80);
        older.id = 'older';

        store.upsertBatch([newer, older]);

        expect(store.getAll().map(e => e.flow.id)).toEqual(['older']);
    });

    test('upsert with same connection key deduplicates', () => {
        const store = new FlowStore();
        const flow1 = makeFlow('10.0.0.2', 80);
        const flow2 = makeFlow('10.0.0.2', 80);
        flow2.id = 'updated-flow';
        flow2.transport.sourcePort = 54321;

        store.upsert(flow1);
        store.upsert(flow2);

        expect(store.size()).toBe(1);
        const entries = store.getAll();
        expect(entries[0].flow.id).toBe('updated-flow');
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

describe('FlowStore finite queries from a source that sends deltas only', () => {
    // A record of the connection to dstPort, ended `minute` minutes into the window, that moved
    // `octets` bytes forward and `reverse` back since the previous record.
    function delta(dstPort: number, minute: number, octets: number, reverse = 0): Flow {
        const flow = makeFlow('10.0.0.2', dstPort);
        flow.id = `flow-${dstPort}-${minute}`;
        flow.endTs = new Date(Date.UTC(2026, 2, 25, 0, minute)).toISOString();
        flow.stats.octetDeltaCount = octets;
        flow.stats.packetDeltaCount = 1;
        flow.reverseStats.octetDeltaCount = reverse;
        flow.reverseStats.packetDeltaCount = reverse > 0 ? 1 : 0;
        return flow;
    }

    test('sums the deltas of every record of a connection, newest first', () => {
        const store = new FlowStore();
        const records = [delta(80, 3, 300, 30), delta(80, 2, 200, 20), delta(80, 1, 100, 10)];

        store.upsertBatch(records, true);

        const [entry] = store.getAll();
        expect(store.size()).toBe(1);
        expect(entry.flow.id).toBe('flow-80-3');
        expect(entry.flow.stats).toMatchObject({ octetTotalCount: 600, packetTotalCount: 3, octetDeltaCount: 300 });
        expect(entry.flow.reverseStats).toMatchObject({ octetTotalCount: 60, packetTotalCount: 3 });
        // The received records are not modified.
        expect(records[0].stats.octetTotalCount).toBe(0);
    });

    test('sums the same whatever the arrival order', () => {
        const forward = new FlowStore();
        const backward = new FlowStore();
        const records = [delta(80, 1, 100), delta(80, 2, 200), delta(80, 3, 300)];

        forward.upsertBatch(records, true);
        backward.upsertBatch([...records].reverse(), true);

        for (const store of [forward, backward]) {
            expect(store.getAll()[0].flow.id).toBe('flow-80-3');
            expect(store.getAll()[0].flow.stats.octetTotalCount).toBe(600);
        }
    });

    test('keeps the latest record when the records carry totals', () => {
        const store = new FlowStore();
        const older = delta(80, 1, 100);
        older.stats.octetTotalCount = 100;
        const newer = delta(80, 2, 200);
        newer.stats.octetTotalCount = 300;

        store.upsertBatch([newer, older], true);

        expect(store.getAll()[0].flow.stats.octetTotalCount).toBe(300);
    });

    test('a live stream is not summed', () => {
        const store = new FlowStore();
        store.upsertBatch([delta(80, 1, 100), delta(80, 2, 200)]);
        expect(store.getAll()[0].flow.stats.octetTotalCount).toBe(0);
        expect(store.getAll()[0].deltaSums).toBeUndefined();
    });

    test.each([
        ['newest first', [5, 4, 3, 2, 1]],
        ['oldest first', [1, 2, 3, 4, 5]],
        ['in no order', [3, 5, 1, 4, 2]],
    ])('keeps the newest connections when full, with records arriving %s', (_, minutes) => {
        const store = new FlowStore(2);

        store.upsertBatch(minutes.map(m => delta(1000 + m, m, 10)), true);

        expect(store.getAll().map(e => e.flow.id).sort()).toEqual(['flow-1004-4', 'flow-1005-5']);
        expect(store.getEvictionCount()).toBe(3);
        expect(store.hasEvicted()).toBe(true);
    });

    test('records of a stored connection still add to its sums when the store is full', () => {
        const store = new FlowStore(2);

        store.upsertBatch([delta(1, 5, 50), delta(2, 4, 40), delta(3, 1, 10), delta(1, 3, 30), delta(2, 2, 20)], true);

        const byPort = new Map(store.getAll().map(e => [e.flow.transport.destinationPort, e]));
        expect(byPort.get(1)!.flow.stats.octetTotalCount).toBe(80);
        expect(byPort.get(2)!.flow.stats.octetTotalCount).toBe(60);
        expect(byPort.has(3)).toBe(false);
        expect(store.getEvictionCount()).toBe(1);
    });

    test('stays correct when records keep updating the same connections', () => {
        const store = new FlowStore(3);
        const records: Flow[] = [];
        for (let minute = 1; minute <= 120; minute++) {
            for (let port = 1; port <= 3; port++) records.push(delta(port, minute % 60, 1));
        }
        records.push(delta(9, 0, 1));

        store.upsertBatch(records, true);

        expect(store.size()).toBe(3);
        expect(store.getAll().map(e => e.flow.transport.destinationPort).sort()).toEqual([1, 2, 3]);
        expect(store.getAll()[0].flow.stats.octetTotalCount).toBe(120);
    });

    test('clear forgets the eviction order', () => {
        const store = new FlowStore(1);
        store.upsertBatch([delta(1, 5, 1), delta(2, 1, 1)], true);
        store.clear();
        store.upsertBatch([delta(3, 1, 1), delta(4, 2, 1)], true);
        expect(store.getAll().map(e => e.flow.transport.destinationPort)).toEqual([4]);
    });
});
