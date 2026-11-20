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
import { clusterMapNodes, pinMapNodes, reconcileMapNodes, seedNamespaceTransition, separateFromNamespaces } from './service-map-layout';
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


test('expansion seeds around the old namespace without moving existing nodes and collapses at centroid', () => {
    const parent = { id: 'namespace:a', namespace: 'a', kind: 'namespace', x: 500, y: 400 };
    const near = { id: 'namespace:b', namespace: 'b', kind: 'namespace', x: 510, y: 400 };
    const pinned = { id: 'namespace:c', namespace: 'c', kind: 'namespace', x: 510, y: 410, userPinned: true };
    const far = { id: 'namespace:d', namespace: 'd', kind: 'namespace', x: 1000, y: 900 };
    const result = reconcileMapNodes([parent, near, pinned, far], [
        { id: 'a/one', namespace: 'a', kind: 'workload' },
        { id: 'a/two', namespace: 'a', kind: 'workload' }, near, pinned, far,
    ], [], { x: 0, y: 0 });
    seedNamespaceTransition([parent, near, pinned, far], result.added);
    const children = result.added;
    expect(children[0].x! - 500).toBeCloseTo(80);
    expect(children[1].x! - 500).toBeCloseTo(-80);
    expect(near).toMatchObject({ x: 510, y: 400 });
    expect((near as PositionedNode).fx).toBeUndefined();
    expect((near as PositionedNode).fy).toBeUndefined();
    expect(pinned).toMatchObject({ x: 510, y: 410 });
    expect(far).toMatchObject({ x: 1000, y: 900 });
    const collapsed = { id: 'namespace:a', namespace: 'a', kind: 'namespace', x: 0, y: 0 };
    seedNamespaceTransition(result.nodes, [collapsed]);
    expect(collapsed.x).toBeCloseTo(500);
    expect(collapsed.y).toBeCloseTo(400);
});

describe('separateFromNamespaces', () => {
    interface N extends PositionedNode { namespace: string; kind: string; userPinned?: boolean }
    const node = (id: string, namespace: string, kind: string, x: number, y: number): N =>
        ({ id, namespace, kind, x, y });
    const options = {
        inBoundary: (n: N) => n.kind === 'workload',
        halfSize: () => ({ hw: 50, hh: 20 }),
        padding: { side: 10, top: 30, bottom: 10 },
        gap: 20,
    };
    const rect = (members: N[], boundary: boolean) => ({
        left: Math.min(...members.map(n => n.x!)) - 50 - (boundary ? 10 : 0),
        right: Math.max(...members.map(n => n.x!)) + 50 + (boundary ? 10 : 0),
        top: Math.min(...members.map(n => n.y!)) - 20 - (boundary ? 30 : 0),
        bottom: Math.max(...members.map(n => n.y!)) + 20 + (boundary ? 10 : 0),
    });
    const overlap = (a: ReturnType<typeof rect>, b: ReturnType<typeof rect>) =>
        a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;

    test('moves what the boundary covers clear of it, and leaves the rest and the namespace alone', () => {
        const members = [node('w1', 'a', 'workload', 0, 0), node('w2', 'a', 'workload', 200, 0)];
        const covered = node('ns-b', 'b', 'namespace', 100, 10);
        const far = node('ns-c', 'c', 'namespace', 2000, 2000);
        separateFromNamespaces([...members, covered, far], new Set(['a']), options);
        expect(members.map(n => [n.x, n.y])).toEqual([[0, 0], [200, 0]]);
        expect(overlap(rect([covered], false), rect(members, true))).toBe(false);
        expect([far.x, far.y]).toEqual([2000, 2000]);
    });

    test('moves another boundary as a whole', () => {
        const members = [node('w1', 'a', 'workload', 0, 0), node('w2', 'a', 'workload', 200, 0)];
        const other = [node('x1', 'b', 'workload', 100, 40), node('x2', 'b', 'workload', 300, 40)];
        const offsets = other.map(n => n.x! - other[0].x!);
        separateFromNamespaces([...members, ...other], new Set(['a']), options);
        expect(other.map(n => n.x! - other[0].x!)).toEqual(offsets);
        expect(other[0].y).toBe(other[1].y);
        expect(overlap(rect(other, true), rect(members, true))).toBe(false);
    });

    test.each([
        { x: 0, y: 0 }, { x: 200, y: 0 }, { x: -200, y: 0 },
        { x: 0, y: 200 }, { x: 0, y: -200 },
    ])('clears a node deeply inside a boundary from ($x, $y)', ({ x, y }) => {
        const members = [node('w1', 'a', 'workload', -1000, -1000), node('w2', 'a', 'workload', 1000, 1000)];
        const covered: N = { ...node('ns-b', 'b', 'namespace', x, y), fx: x, fy: y };
        separateFromNamespaces([...members, covered], new Set(['a']), options);
        const boundary = rect(members, true);
        const padded = {
            left: boundary.left - options.gap, right: boundary.right + options.gap,
            top: boundary.top - options.gap, bottom: boundary.bottom + options.gap,
        };
        expect(overlap(rect([covered], false), padded)).toBe(false);
        expect([covered.fx, covered.fy]).toEqual([covered.x, covered.y]);
        expect(members.map(n => [n.x, n.y])).toEqual([[-1000, -1000], [1000, 1000]]);
    });

    test('does not move a node the user dragged, with or without its pin', () => {
        const members = [node('w1', 'a', 'workload', 0, 0)];
        const dragged: N = { ...node('ns-b', 'b', 'namespace', 30, 10), userPinned: true, fx: 30, fy: 10 };
        separateFromNamespaces([...members, dragged], new Set(['a']), options);
        expect([dragged.x, dragged.y, dragged.fx, dragged.fy]).toEqual([30, 10, 30, 10]);
    });

    test('moves a temporary pin with its node', () => {
        const members = [node('w1', 'a', 'workload', 0, 0)];
        const covered: N = { ...node('ns-b', 'b', 'namespace', 30, 10), fx: 30, fy: 10 };
        separateFromNamespaces([...members, covered], new Set(['a']), options);
        expect([covered.fx, covered.fy]).toEqual([covered.x, covered.y]);
        expect([covered.x, covered.y]).not.toEqual([30, 10]);
    });

    test('pulls apart nodes that a move left overlapping', () => {
        const members = [node('w1', 'a', 'workload', 0, 0)];
        const first = node('ns-b', 'b', 'namespace', 0, 10);
        const second = node('ns-c', 'c', 'namespace', 0, 80);
        separateFromNamespaces([...members, first, second], new Set(['a']), options);
        expect(overlap(rect([first], false), rect([second], false))).toBe(false);
        expect(overlap(rect([first], false), rect(members, true))).toBe(false);
    });
});
