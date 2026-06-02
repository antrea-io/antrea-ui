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
