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

import type { SimulationNodeDatum } from 'd3';

/** PositionedNode is the stable identity shared by the drawing and simulation. */
export interface PositionedNode extends SimulationNodeDatum { id: string; }

/** Reconciles graph metadata without losing positions, velocities or drag pins.
 * New nodes start near an already placed neighbour (or the map centre). Existing
 * nodes stay fixed while additions settle, so sparse topology churn cannot move
 * the entire map. The caller only needs to reheat when `added` is nonempty.
 */
export function reconcileMapNodes<N extends PositionedNode>(
    previous: N[], incoming: N[], edges: { source: string; target: string }[],
    center: { x: number; y: number },
): { nodes: N[]; added: N[] } {
    const old = new Map(previous.map(n => [n.id, n]));
    const added: N[] = [];
    const nodes = incoming.map(data => {
        const existing = old.get(data.id);
        if (existing) return Object.assign(existing, data);
        const node = { ...data };
        added.push(node);
        return node;
    });
    if (!previous.length || !added.length) return { nodes, added };

    const retained = new Map(nodes.filter(n => old.has(n.id)).map(n => [n.id, n]));
    const neighbours = new Map<string, N[]>();
    for (const edge of edges) {
        for (const [id, peerId] of [[edge.source, edge.target], [edge.target, edge.source]]) {
            const peer = retained.get(peerId);
            if (!peer) continue;
            const list = neighbours.get(id) ?? [];
            list.push(peer);
            neighbours.set(id, list);
        }
    }
    for (const node of retained.values()) {
        node.fx = node.x;
        node.fy = node.y;
    }
    for (const node of added) {
        const peers = neighbours.get(node.id) ?? [];
        const x = peers.length ? peers.reduce((sum, n) => sum + (n.x ?? center.x), 0) / peers.length : center.x;
        const y = peers.length ? peers.reduce((sum, n) => sum + (n.y ?? center.y), 0) / peers.length : center.y;
        // Stable, separated seeds avoid the many-body singularity at a neighbour's position.
        let hash = 0;
        for (const char of node.id) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
        const angle = (hash % 360) * Math.PI / 180;
        node.x = x + Math.cos(angle) * 80;
        node.y = y + Math.sin(angle) * 80;
        node.vx = 0;
        node.vy = 0;
    }
    return { nodes, added };
}

/** Applies the same normalized centroid to every member of each namespace. */
export function clusterMapNodes<N extends SimulationNodeDatum>(
    nodes: N[], namespace: (node: N) => string | undefined, alpha: number,
): void {
    const centroids = new Map<string, { x: number; y: number; count: number }>();
    for (const node of nodes) {
        const ns = namespace(node);
        if (!ns) continue;
        const c = centroids.get(ns) ?? { x: 0, y: 0, count: 0 };
        c.x += node.x ?? 0;
        c.y += node.y ?? 0;
        c.count++;
        centroids.set(ns, c);
    }
    for (const c of centroids.values()) { c.x /= c.count; c.y /= c.count; }
    for (const node of nodes) {
        const ns = namespace(node);
        if (!ns) continue;
        const c = centroids.get(ns)!;
        node.vx = (node.vx ?? 0) + (c.x - (node.x ?? 0)) * 0.15 * alpha;
        node.vy = (node.vy ?? 0) + (c.y - (node.y ?? 0)) * 0.15 * alpha;
    }
}
