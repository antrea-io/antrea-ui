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

/** Reconciles graph metadata without losing positions, velocities or pins.
 * New nodes start near an already placed neighbour (or the map centre). Whether the
 * retained nodes stay put while additions settle is up to the caller, see
 * pinMapNodes. The caller only needs to reheat when `added` is nonempty.
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

/** Fixes every node at its current position, except those already pinned.
 * Returns the nodes it pinned, so that the caller can release exactly those later.
 */
export function pinMapNodes<N extends SimulationNodeDatum>(nodes: N[]): N[] {
    const pinned: N[] = [];
    for (const node of nodes) {
        if (node.fx != null || node.fy != null) continue;
        node.fx = node.x;
        node.fy = node.y;
        pinned.push(node);
    }
    return pinned;
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

interface NamespaceNode extends PositionedNode {
    namespace: string;
    kind: string;
    userPinned?: boolean;
}

/** Seeds namespace expansion around the former aggregate and collapse at its centroid.
 * Only nearby unpinned nodes are displaced; distant coordinates and user drag pins survive.
 */
export function seedNamespaceTransition<N extends NamespaceNode>(previous: N[], nodes: N[], added: N[]): void {
    const old = new Map(previous.map(n => [n.id, n]));
    const regions = new Map<string, { x: number; y: number; radius: number }>();
    for (const node of added) {
        if (node.kind === 'workload') {
            const parent = old.get(`namespace:${node.namespace}`);
            if (!parent) continue;
            const siblings = added.filter(n => n.kind === 'workload' && n.namespace === node.namespace);
            const radius = siblings.length === 1 ? 0 : Math.max(80, Math.sqrt(siblings.length) * 50);
            const angle = siblings.indexOf(node) * Math.PI * 2 / siblings.length;
            node.x = (parent.x ?? 0) + radius * Math.cos(angle);
            node.y = (parent.y ?? 0) + radius * Math.sin(angle);
            regions.set(node.namespace, { x: parent.x ?? 0, y: parent.y ?? 0, radius: radius + 100 });
        } else if (node.kind === 'namespace') {
            const children = previous.filter(n => n.kind === 'workload' && n.namespace === node.namespace);
            if (!children.length) continue;
            node.x = children.reduce((sum, n) => sum + (n.x ?? 0), 0) / children.length;
            node.y = children.reduce((sum, n) => sum + (n.y ?? 0), 0) / children.length;
        }
    }
    for (const node of nodes) {
        if (added.includes(node) || node.userPinned) continue;
        for (const [namespace, region] of regions) {
            if (node.namespace === namespace && node.kind === 'workload') continue;
            const dx = (node.x ?? 0) - region.x, dy = (node.y ?? 0) - region.y;
            const distance = Math.hypot(dx, dy);
            if (distance >= region.radius) continue;
            node.x = region.x + (distance ? dx / distance : 1) * region.radius;
            node.y = region.y + (distance ? dy / distance : 0) * region.radius;
            node.vx = 0; node.vy = 0;
            // The caller tracks temporary pins and releases them after settling. Move an
            // existing pin with its node, but let the caller create and track any new one.
            if (node.fx != null) node.fx = node.x;
            if (node.fy != null) node.fy = node.y;
        }
    }
}
