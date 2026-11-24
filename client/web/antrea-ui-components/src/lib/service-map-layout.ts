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
 * New nodes start near retained namespace members, a connected neighbour, or the map centre. Whether the
 * retained nodes stay put while additions settle is up to the caller, see
 * pinMapNodes. The caller only needs to reheat when `added` is nonempty.
 */
export function reconcileMapNodes<N extends PositionedNode>(
    previous: N[], incoming: N[], edges: { source: string; target: string }[],
    center: { x: number; y: number }, namespace?: (node: N) => string | undefined,
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
    const centers = namespace && namespaceCenters(retained.values(), namespace);
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
        const ns = namespace?.(node);
        const origin = ns ? centers?.get(ns) : undefined;
        const x = origin?.x ?? (peers.length ? peers.reduce((sum, n) => sum + (n.x ?? center.x), 0) / peers.length : center.x);
        const y = origin?.y ?? (peers.length ? peers.reduce((sum, n) => sum + (n.y ?? center.y), 0) / peers.length : center.y);
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

function namespaceCenters<N extends SimulationNodeDatum>(nodes: Iterable<N>, namespace: (node: N) => string | undefined) {
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
    return centroids;
}

/** Applies the same normalized centroid to every member of each namespace. */
export function clusterMapNodes<N extends SimulationNodeDatum>(
    nodes: N[], namespace: (node: N) => string | undefined, alpha: number,
): void {
    const centroids = namespaceCenters(nodes, namespace);
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
    beforeMove?(node: N): void;
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

// Find the nearest free coordinate along one axis. Touching edges are allowed.
function nearestFree(value: number, blocked: [number, number][]): number {
    let lower = -Infinity, upper = -Infinity;
    for (const [start, end] of blocked.sort((a, b) => a[0] - b[0])) {
        if (start >= upper) {
            if (value < upper) break;
            lower = start;
        }
        upper = Math.max(upper, end);
    }
    if (value <= lower || value >= upper) return value;
    return value - lower < upper - value ? lower : upper;
}

/** Keep current nodes in place while restoring positions changed by the user. Reserve
 * available original positions first, so one displaced node cannot take another's place.
 */
export function restoreMapPositions<N extends PositionedNode>(
    nodes: N[], positions: ReadonlyMap<string, { x: number; y: number }>,
    bounds: (node: N) => Rect,
): void {
    if (!positions.size) return;
    const occupied = nodes.filter(n => !positions.has(n.id)).map(bounds);
    const displaced: N[] = [];
    for (const n of nodes) {
        const original = positions.get(n.id);
        if (!original) continue;
        n.x = original.x; n.y = original.y;
        n.vx = 0; n.vy = 0;
        const box = bounds(n);
        if (occupied.some(r => r.left < box.right && r.right > box.left && r.top < box.bottom && r.bottom > box.top)) {
            displaced.push(n);
        } else {
            occupied.push(box);
        }
    }
    for (const n of displaced) {
        const box = bounds(n);
        const horizontal: [number, number][] = [], vertical: [number, number][] = [];
        for (const r of occupied) {
            if (r.top < box.bottom && r.bottom > box.top) horizontal.push([r.left - box.right - 1, r.right - box.left + 1]);
            if (r.left < box.right && r.right > box.left) vertical.push([r.top - box.bottom - 1, r.bottom - box.top + 1]);
        }
        const dx = nearestFree(0, horizontal), dy = nearestFree(0, vertical);
        if (Math.abs(dx) < Math.abs(dy)) n.x = n.x! + dx;
        else n.y = n.y! + dy;
        occupied.push(bounds(n));
    }
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
            options.beforeMove?.(n);
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
