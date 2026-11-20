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
import { clusterMapNodes, pinMapNodes, reconcileMapNodes, seedNamespaceTransition } from './service-map-layout';
import type { PositionedNode } from './service-map-layout';

describe('incremental map layout', () => {
    test('reuses node identity, velocity and pins while refreshing metadata and removing stale nodes', () => {
        const retained = { id: 'a', x: 40, y: 80, vx: 2, vy: 3, fx: 40, fy: 80, detail: 'old' };
        const result = reconcileMapNodes(
            [retained, { id: 'gone', detail: '' }], [{ id: 'a', detail: 'new' }], [], { x: 0, y: 0 },
        );
        expect(result.nodes).toHaveLength(1);
        expect(result.nodes[0]).toBe(retained);
        expect(retained).toMatchObject({ x: 40, y: 80, vx: 2, vy: 3, fx: 40, fy: 80, detail: 'new' });
        expect(result.added).toEqual([]);
    });

    test('seeds additions near existing neighbours without pinning anything', () => {
        const old: PositionedNode = { id: 'a', x: 500, y: 400 };
        const result = reconcileMapNodes(
            [old], [{ id: 'a' }, { id: 'new' }, { id: 'isolated' }],
            [{ source: 'new', target: 'a' }], { x: 0, y: 0 },
        );
        expect(old.fx).toBeUndefined();
        const [neighbour, isolated] = result.added;
        expect(Math.hypot(neighbour.x! - old.x!, neighbour.y! - old.y!)).toBeCloseTo(80);
        expect(Math.hypot(isolated.x!, isolated.y!)).toBeCloseTo(80);
        expect(neighbour.fx).toBeUndefined();
    });

    test('pins only the nodes that are not pinned yet and reports them', () => {
        const free = { id: 'free', x: 1, y: 2 };
        const dragged = { id: 'dragged', x: 5, y: 6, fx: 3, fy: 4 };
        expect(pinMapNodes([free, dragged])).toEqual([free]);
        expect(free).toMatchObject({ fx: 1, fy: 2 });
        expect(dragged).toMatchObject({ fx: 3, fy: 4 });
    });

    test('normalizes each namespace centroid once, independently of member order', () => {
        const nodes = [
            { id: 'a', ns: 'one', x: 100, y: 200, vx: 0, vy: 0 },
            { id: 'b', ns: 'one', x: 300, y: 400, vx: 0, vy: 0 },
            { id: 'c', ns: 'two', x: 900, y: 900, vx: 0, vy: 0 },
            { id: 'external', ns: '', x: 0, y: 0, vx: 0, vy: 0 },
        ];
        const reversed = nodes.map(n => ({ ...n })).reverse();
        clusterMapNodes(nodes, n => n.ns, 1);
        clusterMapNodes(reversed, n => n.ns, 1);
        expect(nodes[0]).toMatchObject({ vx: 15, vy: 15 });
        expect(nodes[1]).toMatchObject({ vx: -15, vy: -15 });
        expect(nodes[2]).toMatchObject({ vx: 0, vy: 0 });
        expect(nodes[3]).toMatchObject({ vx: 0, vy: 0 });
        expect(reversed.reverse()).toEqual(nodes);
    });
});


test('expansion seeds around the old namespace, displaces only nearby unpinned nodes, and collapses at centroid', () => {
    const parent = { id: 'namespace:a', namespace: 'a', kind: 'namespace', x: 500, y: 400 };
    const near = { id: 'namespace:b', namespace: 'b', kind: 'namespace', x: 510, y: 400 };
    const pinned = { id: 'namespace:c', namespace: 'c', kind: 'namespace', x: 510, y: 410, userPinned: true };
    const far = { id: 'namespace:d', namespace: 'd', kind: 'namespace', x: 1000, y: 900 };
    const result = reconcileMapNodes([parent, near, pinned, far], [
        { id: 'a/one', namespace: 'a', kind: 'workload' },
        { id: 'a/two', namespace: 'a', kind: 'workload' }, near, pinned, far,
    ], [], { x: 0, y: 0 });
    seedNamespaceTransition([parent, near, pinned, far], result.nodes, result.added);
    const children = result.added;
    expect(children[0].x! - 500).toBeCloseTo(80);
    expect(children[1].x! - 500).toBeCloseTo(-80);
    expect(near.x).toBe(680);
    expect((near as PositionedNode).fx).toBeUndefined();
    expect((near as PositionedNode).fy).toBeUndefined();
    expect(pinned).toMatchObject({ x: 510, y: 410 });
    expect(far).toMatchObject({ x: 1000, y: 900 });
    const collapsed = { id: 'namespace:a', namespace: 'a', kind: 'namespace', x: 0, y: 0 };
    seedNamespaceTransition(result.nodes, [collapsed, far], [collapsed]);
    expect(collapsed.x).toBeCloseTo(500);
    expect(collapsed.y).toBeCloseTo(400);
});
