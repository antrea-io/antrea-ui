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
 * Existing nodes stay put until separateFromNamespaces can move whole boundaries after settling.
 */
export function seedNamespaceTransition<N extends NamespaceNode>(previous: N[], added: N[]): void {
    const old = new Map(previous.map(n => [n.id, n]));
    for (const node of added) {
        if (node.kind === 'workload') {
            const parent = old.get(`namespace:${node.namespace}`);
            if (!parent) continue;
            const siblings = added.filter(n => n.kind === 'workload' && n.namespace === node.namespace);
            const radius = siblings.length === 1 ? 0 : Math.max(80, Math.sqrt(siblings.length) * 50);
            const angle = siblings.indexOf(node) * Math.PI * 2 / siblings.length;
            node.x = (parent.x ?? 0) + radius * Math.cos(angle);
            node.y = (parent.y ?? 0) + radius * Math.sin(angle);
        } else if (node.kind === 'namespace') {
            const children = previous.filter(n => n.kind === 'workload' && n.namespace === node.namespace);
            if (!children.length) continue;
            node.x = children.reduce((sum, n) => sum + (n.x ?? 0), 0) / children.length;
            node.y = children.reduce((sum, n) => sum + (n.y ?? 0), 0) / children.length;
        }
    }
}

export interface NamespaceSeparation<N> {
    /** Whether the node is drawn inside its namespace's boundary. */
    inBoundary(node: N): boolean;
    /** Half the width and height of the node's own box. */
    halfSize(node: N): { hw: number; hh: number };
    /** How far a boundary extends beyond its members, with extra room above for its header. */
    padding: { side: number; top: number; bottom: number };
    /** A namespace title can make the boundary wider than its members. */
    minBoundaryWidth?(namespace: string): number;
    /** The clear space to leave around a namespace that has just expanded. */
    gap: number;
}

interface Rect { left: number; top: number; right: number; bottom: number }

/** Bounds shared by namespace rendering and collision checks. Extra title width extends right. */
export function nodeBounds<N extends SimulationNodeDatum>(
    nodes: readonly N[], halfSize: (node: N) => { hw: number; hh: number },
    padding = { side: 0, top: 0, bottom: 0 }, minimumWidth = 0,
): Rect {
    const rect = { left: Infinity, top: Infinity, right: -Infinity, bottom: -Infinity };
    for (const n of nodes) {
        const { hw, hh } = halfSize(n);
        rect.left = Math.min(rect.left, (n.x ?? 0) - hw);
        rect.top = Math.min(rect.top, (n.y ?? 0) - hh);
        rect.right = Math.max(rect.right, (n.x ?? 0) + hw);
        rect.bottom = Math.max(rect.bottom, (n.y ?? 0) + hh);
    }
    rect.left -= padding.side; rect.right += padding.side;
    rect.top -= padding.top; rect.bottom += padding.bottom;
    rect.right = Math.max(rect.right, rect.left + minimumWidth);
    return rect;
}

/** Moves what an expanded namespace's boundary now covers out of its way. Whatever is not a member
 * of one of the expanded namespaces is moved as a unit, a whole boundary at a time, so other
 * namespaces keep their shape. Nodes the user dragged do not move, and neither do the expanded
 * namespaces themselves. A move can run into something else, so a few passes also pull apart
 * what the moves left overlapping.
 */
export function separateFromNamespaces<N extends NamespaceNode>(
    nodes: N[], expanded: ReadonlySet<string>, options: NamespaceSeparation<N>,
): void {
    const { inBoundary, halfSize, padding, gap } = options;
    const units = new Map<string, N[]>();
    for (const node of nodes) {
        const key = inBoundary(node) ? `namespace:${node.namespace}` : `node:${node.id}`;
        const members = units.get(key);
        if (members) members.push(node); else units.set(key, [node]);
    }
    const rects = new Map<string, Rect>();
    const rectOf = (key: string, members: N[]): Rect => {
        const cached = rects.get(key);
        if (cached) return cached;
        const boundary = key.startsWith('namespace:');
        const rect = nodeBounds(members, halfSize, boundary ? padding : undefined,
            boundary ? options.minBoundaryWidth?.(members[0].namespace) : undefined);
        rects.set(key, rect);
        return rect;
    };
    const fixed = [...units].filter(([key]) => key.startsWith('namespace:') && expanded.has(key.slice('namespace:'.length)));
    const movable = [...units].filter(([key]) => !fixed.some(([k]) => k === key));
    const shift = (key: string, members: N[], dx: number, dy: number) => {
        rects.delete(key);
        for (const n of members) {
            n.x = (n.x ?? 0) + dx; n.y = (n.y ?? 0) + dy;
            n.vx = 0; n.vy = 0;
            if (n.fx != null) n.fx += dx;
            if (n.fy != null) n.fy += dy;
        }
    };
    const pinned = (members: N[]) => members.some(n => n.userPinned);
    // The shortest translation that takes a clear of b, or null when they do not overlap.
    // Measure to the far edge even when one box contains the other: their intersection's
    // width or height would leave the inner box inside the outer one.
    const clearance = (a: Rect, b: Rect): { dx: number; dy: number } | null => {
        if (a.right <= b.left || a.left >= b.right || a.bottom <= b.top || a.top >= b.bottom) return null;
        const ax = (a.left + a.right) / 2, bx = (b.left + b.right) / 2;
        const ay = (a.top + a.bottom) / 2, by = (b.top + b.bottom) / 2;
        const dx = ax >= bx ? b.right - a.left : b.left - a.right;
        const dy = ay >= by ? b.bottom - a.top : b.top - a.bottom;
        return Math.abs(dx) < Math.abs(dy) ? { dx, dy: 0 } : { dx: 0, dy };
    };
    const inflate = (r: Rect, by: number): Rect =>
        ({ left: r.left - by, top: r.top - by, right: r.right + by, bottom: r.bottom + by });
    for (let pass = 0; pass < 8; pass++) {
        let moved = false;
        for (const [anchorKey, anchorMembers] of fixed) {
            const anchor = inflate(rectOf(anchorKey, anchorMembers), gap);
            for (const [key, members] of movable) {
                if (pinned(members)) continue;
                const move = clearance(rectOf(key, members), anchor);
                if (!move) continue;
                shift(key, members, move.dx, move.dy);
                moved = true;
            }
        }
        for (let i = 0; i < movable.length; i++) {
            for (let j = i + 1; j < movable.length; j++) {
                const [ki, a] = movable[i], [kj, b] = movable[j];
                const move = clearance(rectOf(ki, a), rectOf(kj, b));
                if (!move) continue;
                const aPinned = pinned(a), bPinned = pinned(b);
                if (aPinned && bPinned) continue;
                const share = aPinned || bPinned ? 1 : 0.5;
                if (!aPinned) shift(ki, a, move.dx * share, move.dy * share);
                if (!bPinned) shift(kj, b, -move.dx * share, -move.dy * share);
                moved = true;
            }
        }
        if (!moved) break;
    }
}
