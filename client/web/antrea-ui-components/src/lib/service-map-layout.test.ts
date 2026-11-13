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
import { clusterMapNodes, reconcileMapNodes } from './service-map-layout';
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

    test('seeds additions near existing neighbours and fixes existing positions', () => {
        const old: PositionedNode = { id: 'a', x: 500, y: 400 };
        const result = reconcileMapNodes(
            [old], [{ id: 'a' }, { id: 'new' }, { id: 'isolated' }],
            [{ source: 'new', target: 'a' }], { x: 0, y: 0 },
        );
        expect(old).toMatchObject({ fx: 500, fy: 400 });
        const [neighbour, isolated] = result.added;
        expect(Math.hypot(neighbour.x! - old.x!, neighbour.y! - old.y!)).toBeCloseTo(80);
        expect(Math.hypot(isolated.x!, isolated.y!)).toBeCloseTo(80);
        expect(neighbour.fx).toBeUndefined();
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
