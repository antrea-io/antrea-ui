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

import { NodeLatencyStats } from '../api/nodelatency';
import {
    DOWN_STALENESS_MS,
    aggregateStats,
    buildLinks,
    buildModel,
    heatmapNodeOrder,
    mean,
    median,
    nodeHealthMap,
    nodeLinksFor,
    nodeNames,
    percentile,
    problemNodes,
    rankNodes,
    referenceTime,
} from './nodelatency-util';

describe('statistics helpers', () => {
    test('mean', () => {
        expect(mean([2, 4, 6])).toBe(4);
        expect(mean([])).toBeNaN();
    });

    test('median', () => {
        expect(median([3, 1, 2])).toBe(2);
        expect(median([1, 2, 3, 4])).toBe(2.5);
        expect(median([5])).toBe(5);
    });

    test('percentile interpolates', () => {
        expect(percentile([10, 20, 30, 40], 90)).toBeCloseTo(37, 5);
        expect(percentile([10, 20, 30, 40], 0)).toBe(10);
        expect(percentile([10, 20, 30, 40], 100)).toBe(40);
        expect(percentile([], 50)).toBeNaN();
    });
});

// Use dynamic timestamps so all fixtures are within the staleness window when the tests run.
const BASE_TIME = Date.now();
const sendTime = new Date(BASE_TIME - 30_000).toISOString();   // 30 s ago
const recvTime = new Date(BASE_TIME - 60_000).toISOString();   // 60 s ago
// A reply received well past the staleness window relative to now.
const staleRecvTime = new Date(BASE_TIME - DOWN_STALENESS_MS - 60_000).toISOString();

const stats: NodeLatencyStats[] = [
    {
        metadata: { name: 'node-a' },
        peerNodeLatencyStats: [
            {
                nodeName: 'node-b',
                targetIPLatencyStats: [
                    { targetIP: '10.0.0.2', lastMeasuredRTTNanoseconds: 2_000_000, lastSendTime: sendTime, lastRecvTime: recvTime },
                ],
            },
            {
                nodeName: 'node-c',
                targetIPLatencyStats: [
                    // RTT present but reply is stale -> down.
                    { targetIP: '10.0.0.3', lastMeasuredRTTNanoseconds: 9_000_000, lastSendTime: sendTime, lastRecvTime: staleRecvTime },
                ],
            },
        ],
    },
    {
        metadata: { name: 'node-b' },
        peerNodeLatencyStats: [
            {
                nodeName: 'node-a',
                targetIPLatencyStats: [
                    // Missing RTT -> down.
                    { targetIP: '10.0.0.1', lastSendTime: sendTime },
                    { targetIP: 'fd00::1', lastMeasuredRTTNanoseconds: 4_000_000, lastSendTime: sendTime, lastRecvTime: recvTime },
                ],
            },
        ],
    },
];

describe('node latency aggregation', () => {
    test('nodeNames returns the sorted union of sources and peers', () => {
        expect(nodeNames(stats)).toEqual(['node-a', 'node-b', 'node-c']);
    });

    test('referenceTime is at least the current time', () => {
        // sendTime is 30 s in the past, so Date.now() wins and the result must
        // be >= the time we started the test.
        expect(referenceTime(stats)).toBeGreaterThanOrEqual(BASE_TIME);
    });

    test('buildLinks collapses target IPs to the max up RTT and flags down links', () => {
        const links = buildLinks(stats);
        const ab = links.find(l => l.sourceNode === 'node-a' && l.targetNode === 'node-b')!;
        expect(ab.down).toBe(false);
        expect(ab.rttMs).toBe(2);

        const ac = links.find(l => l.sourceNode === 'node-a' && l.targetNode === 'node-c')!;
        expect(ac.down).toBe(true);

        // node-b -> node-a has one down (missing RTT) and one up (4ms) target; link is up at 4ms.
        const ba = links.find(l => l.sourceNode === 'node-b' && l.targetNode === 'node-a')!;
        expect(ba.down).toBe(false);
        expect(ba.rttMs).toBe(4);
    });

    test('aggregateStats summarises only up links', () => {
        const agg = aggregateStats(stats);
        expect(agg.nodeCount).toBe(3);
        expect(agg.measuredCount).toBe(2);
        expect(agg.downCount).toBe(1);
        expect(agg.meanMs).toBe(3);
        expect(agg.maxMs).toBe(4);
    });
});

describe('node health and problem detection', () => {
    test('nodeHealthMap counts ingress/egress totals and down links', () => {
        const links = buildLinks(stats);
        const health = nodeHealthMap(links, nodeNames(stats));
        const a = health.get('node-a')!;
        expect(a).toMatchObject({ egressTotal: 2, egressDown: 1, ingressTotal: 1, ingressDown: 0, maxRttMs: 4 });
        const c = health.get('node-c')!;
        expect(c).toMatchObject({ egressTotal: 0, egressDown: 0, ingressTotal: 1, ingressDown: 1, maxRttMs: 0 });
    });

    test('problemNodes applies the down threshold', () => {
        const health = nodeHealthMap(buildLinks(stats), nodeNames(stats));
        expect(problemNodes(health, 2)).toHaveLength(0);
        expect(problemNodes(health, 1).map(h => h.node)).toEqual(['node-a', 'node-c']);
    });

    test('rankNodes orders by down count then latency', () => {
        const health = nodeHealthMap(buildLinks(stats), nodeNames(stats));
        expect(rankNodes(nodeNames(stats), health)).toEqual(['node-a', 'node-c', 'node-b']);
    });

    test('nodeLinksFor splits egress and ingress for a node', () => {
        const { egress, ingress } = nodeLinksFor(buildLinks(stats), 'node-a');
        expect(egress.map(l => l.targetNode).sort()).toEqual(['node-b', 'node-c']);
        expect(ingress.map(l => l.sourceNode)).toEqual(['node-b']);
    });

    test('buildModel exposes a coherent shared model', () => {
        const model = buildModel(stats, 1);
        expect(model.agg.nodeCount).toBe(3);
        expect(model.problemNodeSet.has('node-a')).toBe(true);
        expect(model.rankedNodes[0]).toBe('node-a');
        expect(model.linkByKey.get('node-a|node-b')?.rttMs).toBe(2);
    });
});

describe('large cluster scaling', () => {
    // Ring of 300 nodes (each up-link to the next), with node000 also failing to reach
    // three peers so it is the highest-severity node.
    function makeLargeCluster(n: number): NodeLatencyStats[] {
        const name = (i: number) => `node${String(i).padStart(3, '0')}`;
        const out: NodeLatencyStats[] = [];
        for (let i = 0; i < n; i++) {
            const peers = [{
                nodeName: name((i + 1) % n),
                targetIPLatencyStats: [{
                    targetIP: `10.0.0.${i % 255}`,
                    lastMeasuredRTTNanoseconds: 1_000_000,
                    lastSendTime: sendTime,
                    lastRecvTime: recvTime,
                }],
            }];
            if (i === 0) {
                for (let d = 1; d <= 3; d++) {
                    peers.push({
                        nodeName: name(100 + d),
                        targetIPLatencyStats: [{ targetIP: `10.1.0.${d}`, lastMeasuredRTTNanoseconds: 0, lastSendTime: sendTime, lastRecvTime: recvTime }],
                    });
                }
            }
            out.push({ metadata: { name: name(i) }, peerNodeLatencyStats: peers });
        }
        return out;
    }

    test('heatmapNodeOrder returns all nodes ranked worst-first', () => {
        const model = buildModel(makeLargeCluster(300));
        expect(model.nodes).toHaveLength(300);
        const order = heatmapNodeOrder(model, false);
        expect(order).toHaveLength(300);
        expect(order[0]).toBe('node000');
    });

    test('heatmapNodeOrder with restrictToProblem returns only problem nodes', () => {
        const model = buildModel(makeLargeCluster(300));
        const order = heatmapNodeOrder(model, true);
        expect(order).toEqual(model.problemNodes.map(p => p.node));
        expect(order).toContain('node000');
    });
});
