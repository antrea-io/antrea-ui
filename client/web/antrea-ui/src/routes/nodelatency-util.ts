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

import { NodeLatencyStats, TargetIPLatencyStats } from '../api/nodelatency';

// A link is considered "down" when its most recent reply (lastRecvTime) lags the
// reference time (the newest send across the cluster) by more than this threshold.
// NodeLatencyMonitor pings every 60s by default, so 3 minutes tolerates a couple of
// missed probes before flagging a link.
export const DOWN_STALENESS_MS = 3 * 60 * 1000;

export interface NodeLink {
    sourceNode: string
    targetNode: string
    // Representative RTT in milliseconds (max across target IPs), undefined if unmeasured.
    rttMs?: number
    down: boolean
    targets: TargetIPLatencyStats[]
}

function parseTime(t: string | undefined): number | undefined {
    if (!t) return undefined;
    const ms = Date.parse(t);
    return isNaN(ms) ? undefined : ms;
}

// referenceTime is the latest lastSendTime observed across all measurements; using the
// cluster's own clock avoids false positives from skew between the browser and cluster.
// Returns the reference time used to determine whether a link is stale.
// We take the MAX of the cluster's newest lastSendTime and the current browser
// time. This means:
//   - When the cluster clock is ahead (skewed), cluster time wins and healthy
//     links are not falsely flagged as down.
//   - When the monitor has stopped sending probes entirely, Date.now() advances
//     while the cluster timestamps stay frozen, so stale links eventually cross
//     DOWN_STALENESS_MS and are correctly flagged as down.
export function referenceTime(stats: NodeLatencyStats[]): number {
    let ref = 0;
    stats.forEach(stat => stat.peerNodeLatencyStats?.forEach(peer => peer.targetIPLatencyStats?.forEach(t => {
        const sent = parseTime(t.lastSendTime);
        if (sent !== undefined && sent > ref) ref = sent;
    })));
    return Math.max(ref, Date.now());
}

export function isTargetDown(target: TargetIPLatencyStats, refTime: number): boolean {
    if (!target.lastMeasuredRTTNanoseconds || target.lastMeasuredRTTNanoseconds <= 0) return true;
    const recv = parseTime(target.lastRecvTime);
    if (recv === undefined) return true;
    return refTime - recv > DOWN_STALENESS_MS;
}

export function buildLinks(stats: NodeLatencyStats[], refTime: number = referenceTime(stats)): NodeLink[] {
    const links: NodeLink[] = [];
    stats.forEach(stat => {
        stat.peerNodeLatencyStats?.forEach(peer => {
            const targets = peer.targetIPLatencyStats ?? [];
            let maxRttNs: number | undefined;
            let anyUp = false;
            targets.forEach(t => {
                if (t.lastMeasuredRTTNanoseconds !== undefined && t.lastMeasuredRTTNanoseconds > 0 && !isTargetDown(t, refTime)) {
                    anyUp = true;
                    if (maxRttNs === undefined || t.lastMeasuredRTTNanoseconds > maxRttNs) maxRttNs = t.lastMeasuredRTTNanoseconds;
                }
            });
            links.push({
                sourceNode: stat.metadata.name,
                targetNode: peer.nodeName,
                rttMs: maxRttNs === undefined ? undefined : maxRttNs / 1e6,
                down: !anyUp,
                targets,
            });
        });
    });
    return links;
}

export function nodeNames(stats: NodeLatencyStats[]): string[] {
    const names = new Set<string>();
    stats.forEach(stat => {
        names.add(stat.metadata.name);
        stat.peerNodeLatencyStats?.forEach(peer => names.add(peer.nodeName));
    });
    return Array.from(names).sort();
}

export function mean(values: number[]): number {
    if (values.length === 0) return NaN;
    return values.reduce((a, b) => a + b, 0) / values.length;
}

// Linear-interpolation percentile (p in [0, 100]) over the supplied values.
export function percentile(values: number[], p: number): number {
    if (values.length === 0) return NaN;
    const sorted = [...values].sort((a, b) => a - b);
    if (sorted.length === 1) return sorted[0];
    const rank = (p / 100) * (sorted.length - 1);
    const lo = Math.floor(rank);
    const hi = Math.ceil(rank);
    if (lo === hi) return sorted[lo];
    return sorted[lo] + (sorted[hi] - sorted[lo]) * (rank - lo);
}

export function median(values: number[]): number {
    return percentile(values, 50);
}

export interface AggregatedStats {
    nodeCount: number
    measuredCount: number
    downCount: number
    meanMs: number
    medianMs: number
    p90Ms: number
    maxMs: number
}

export function aggregateStats(
    stats: NodeLatencyStats[],
    links: NodeLink[] = buildLinks(stats),
    nodes: string[] = nodeNames(stats),
): AggregatedStats {
    const upRtts = links
        .filter((l): l is NodeLink & { rttMs: number } => !l.down && l.rttMs !== undefined)
        .map(l => l.rttMs);
    return {
        nodeCount: nodes.length,
        measuredCount: upRtts.length,
        downCount: links.filter(l => l.down).length,
        meanMs: mean(upRtts),
        medianMs: median(upRtts),
        p90Ms: percentile(upRtts, 90),
        maxMs: upRtts.reduce((a, b) => a > b ? a : b, NaN),
    };
}

// A node is flagged as a "problem" when its number of down ingress or egress links
// reaches this threshold (i.e. it is failing to reach, or be reached by, multiple peers).
export const PROBLEM_DOWN_THRESHOLD = 2;


export interface NodeHealth {
    node: string
    egressDown: number
    egressTotal: number
    ingressDown: number
    ingressTotal: number
    // Worst up RTT (ms) across all of the node's links, 0 if it has no measured links.
    maxRttMs: number
}

export function linkKey(sourceNode: string, targetNode: string): string {
    return `${sourceNode}|${targetNode}`;
}

export function nodeHealthMap(links: NodeLink[], nodes: string[]): Map<string, NodeHealth> {
    const health = new Map<string, NodeHealth>();
    const ensure = (n: string): NodeHealth => {
        let h = health.get(n);
        if (!h) {
            h = { node: n, egressDown: 0, egressTotal: 0, ingressDown: 0, ingressTotal: 0, maxRttMs: 0 };
            health.set(n, h);
        }
        return h;
    };
    nodes.forEach(ensure);
    links.forEach(l => {
        const src = ensure(l.sourceNode);
        const dst = ensure(l.targetNode);
        src.egressTotal++;
        dst.ingressTotal++;
        if (l.down) {
            src.egressDown++;
            dst.ingressDown++;
        } else if (l.rttMs !== undefined) {
            if (l.rttMs > src.maxRttMs) src.maxRttMs = l.rttMs;
            if (l.rttMs > dst.maxRttMs) dst.maxRttMs = l.rttMs;
        }
    });
    return health;
}

function downTotal(h: NodeHealth): number {
    return h.egressDown + h.ingressDown;
}

export function problemNodes(health: Map<string, NodeHealth>, threshold: number = PROBLEM_DOWN_THRESHOLD): NodeHealth[] {
    return Array.from(health.values())
        .filter(h => h.egressDown >= threshold || h.ingressDown >= threshold)
        .sort((a, b) => downTotal(b) - downTotal(a) || b.maxRttMs - a.maxRttMs || a.node.localeCompare(b.node));
}

// rankNodes orders nodes by severity: most down links first, then highest latency, then
// name. The heatmap uses this so the most interesting nodes survive truncation.
export function rankNodes(nodes: string[], health: Map<string, NodeHealth>): string[] {
    return [...nodes].sort((a, b) => {
        const ha = health.get(a);
        const hb = health.get(b);
        const da = ha ? downTotal(ha) : 0;
        const db = hb ? downTotal(hb) : 0;
        if (da !== db) return db - da;
        const ra = ha ? ha.maxRttMs : 0;
        const rb = hb ? hb.maxRttMs : 0;
        if (ra !== rb) return rb - ra;
        return a.localeCompare(b);
    });
}

export interface NodeLatencyModel {
    nodes: string[]
    links: NodeLink[]
    linkByKey: Map<string, NodeLink>
    health: Map<string, NodeHealth>
    problemNodes: NodeHealth[]
    problemNodeSet: Set<string>
    rankedNodes: string[]
    agg: AggregatedStats
}

export function filterActiveStats(stats: NodeLatencyStats[], refTime: number = referenceTime(stats)): NodeLatencyStats[] {
    return stats.filter(stat => {
        return stat.peerNodeLatencyStats?.some(peer =>
            peer.targetIPLatencyStats?.some(target => {
                const sent = parseTime(target.lastSendTime);
                return sent !== undefined && refTime - sent < DOWN_STALENESS_MS;
            })
        );
    });
}

export function buildModel(stats: NodeLatencyStats[], threshold: number = PROBLEM_DOWN_THRESHOLD): NodeLatencyModel {
    const activeStats = filterActiveStats(stats);
    const refTime = referenceTime(activeStats);
    const links = buildLinks(activeStats, refTime);
    const nodes = nodeNames(activeStats);
    const linkByKey = new Map<string, NodeLink>();
    links.forEach(l => linkByKey.set(linkKey(l.sourceNode, l.targetNode), l));
    const health = nodeHealthMap(links, nodes);
    const problems = problemNodes(health, threshold);
    return {
        nodes,
        links,
        linkByKey,
        health,
        problemNodes: problems,
        problemNodeSet: new Set(problems.map(p => p.node)),
        rankedNodes: rankNodes(nodes, health),
        agg: aggregateStats(activeStats, links, nodes),
    };
}

export interface NodeLinks {
    egress: NodeLink[]
    ingress: NodeLink[]
}

// nodeLinksFor returns the links originating from (egress) and terminating at (ingress)
// the given node. Ingress links are derived by scanning every reporting node's stats.
export function nodeLinksFor(links: NodeLink[], node: string): NodeLinks {
    return {
        egress: links.filter(l => l.sourceNode === node),
        ingress: links.filter(l => l.targetNode === node),
    };
}

export function heatmapNodeOrder(model: NodeLatencyModel, restrictToProblem: boolean): string[] {
    if (restrictToProblem) {
        return model.rankedNodes.filter(n => model.problemNodeSet.has(n));
    }
    return model.rankedNodes;
}
