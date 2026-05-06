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

import { useRef, useEffect, useMemo, useState, useCallback } from 'react';
import * as d3 from 'd3';
import { FlowEntry } from '../api/flow-store';
import {
    FlowType,
    NetworkPolicyRuleAction,
    flowTypeLabel,
    getProtocolName,
    formatPolicyInfo,
    formatBytes,
    Labels,
    destinationK8sServiceFilterKey,
} from '../api/flow-types';

const WELL_KNOWN_APP_LABELS = [
    'app.kubernetes.io/name',
    'app.kubernetes.io/instance',
    'app',
    'k8s-app',
    'name',
];

function getWorkloadName(namespace: string, podName: string, podLabels?: Labels): string {
    if (podLabels?.labels) {
        for (const label of WELL_KNOWN_APP_LABELS) {
            if (podLabels.labels[label]) {
                return `${namespace}/${podLabels.labels[label]}`;
            }
        }
    }
    const stripped = podName.replace(/-[a-z0-9]{5,10}(-[a-z0-9]{5})?$/, '');
    return `${namespace}/${stripped}`;
}

function getWorkloadShortName(fullName: string): string {
    const parts = fullName.split('/');
    return parts[parts.length - 1];
}

interface WorkloadNode {
    id: string;
    shortName: string;
    namespace: string;
    isExternal: boolean;
}

interface WorkloadEdge {
    source: string;
    target: string;
    connectionCount: number;
    totalBytesForward: number;
    totalBytesReverse: number;
    protoPorts: Map<number, Set<number>>;
    ingressPolicies: Set<string>;
    egressPolicies: Set<string>;
    ingressActions: Set<NetworkPolicyRuleAction>;
    egressActions: Set<NetworkPolicyRuleAction>;
    flowTypes: Set<FlowType>;
    firstSeen: number;
    lastSeen: number;
}

interface EdgeDetails {
    source: string;
    target: string;
    connectionCount: number;
    connectionRate: number;
    totalBytesForward: number;
    totalBytesReverse: number;
    bitRate: number;
    destPortsStr: string;
    ingressPolicies: string[];
    egressPolicies: string[];
    flowTypes: string[];
}

function buildGraph(entries: FlowEntry[]): { nodes: WorkloadNode[]; edges: WorkloadEdge[] } {
    const nodeMap = new Map<string, WorkloadNode>();
    const edgeMap = new Map<string, WorkloadEdge>();

    for (const entry of entries) {
        const { flow } = entry;
        const flowType = flow.k8s.flowType as FlowType;

        let srcId: string;
        let dstId: string;

        if (flowType === FlowType.FromExternal) {
            srcId = 'external';
            if (!nodeMap.has(srcId)) {
                nodeMap.set(srcId, { id: srcId, shortName: 'External', namespace: '', isExternal: true });
            }
        } else {
            srcId = getWorkloadName(
                flow.k8s.sourcePodNamespace,
                flow.k8s.sourcePodName,
                flow.k8s.sourcePodLabels,
            );
            if (!nodeMap.has(srcId)) {
                nodeMap.set(srcId, {
                    id: srcId,
                    shortName: getWorkloadShortName(srcId),
                    namespace: flow.k8s.sourcePodNamespace,
                    isExternal: false,
                });
            }
        }

        if (flowType === FlowType.ToExternal) {
            dstId = 'external';
            if (!nodeMap.has(dstId)) {
                nodeMap.set(dstId, { id: dstId, shortName: 'External', namespace: '', isExternal: true });
            }
        } else {
            // When the flow hits a ClusterIP Service, destinationServicePortName is the full
            // kube-proxy token (namespace/name:portName). Using it verbatim as part of the node id
            // splits one logical Service into multiple graph nodes per port. Collapse to
            // namespace/serviceName (same canonical token as the filter dropdown).
            if (flow.k8s.destinationServicePortName) {
                const key = destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName);
                if (key) {
                    dstId = key;
                } else {
                    dstId = getWorkloadName(
                        flow.k8s.destinationPodNamespace,
                        flow.k8s.destinationPodName,
                        flow.k8s.destinationPodLabels,
                    );
                }
            } else {
                dstId = getWorkloadName(
                    flow.k8s.destinationPodNamespace,
                    flow.k8s.destinationPodName,
                    flow.k8s.destinationPodLabels,
                );
            }
            if (!nodeMap.has(dstId)) {
                nodeMap.set(dstId, {
                    id: dstId,
                    shortName: getWorkloadShortName(dstId),
                    namespace: flow.k8s.destinationPodNamespace,
                    isExternal: false,
                });
            }
        }

        if (srcId === dstId) continue;

        const edgeKey = `${srcId}|${dstId}`;
        let edge = edgeMap.get(edgeKey);
        if (!edge) {
            edge = {
                source: srcId,
                target: dstId,
                connectionCount: 0,
                totalBytesForward: 0,
                totalBytesReverse: 0,
                protoPorts: new Map(),
                ingressPolicies: new Set(),
                egressPolicies: new Set(),
                ingressActions: new Set(),
                egressActions: new Set(),
                flowTypes: new Set(),
                firstSeen: entry.firstSeen,
                lastSeen: entry.lastSeen,
            };
            edgeMap.set(edgeKey, edge);
        }
        edge.connectionCount++;
        if (entry.firstSeen < edge.firstSeen) edge.firstSeen = entry.firstSeen;
        if (entry.lastSeen > edge.lastSeen) edge.lastSeen = entry.lastSeen;
        edge.totalBytesForward += flow.stats.octetTotalCount;
        edge.totalBytesReverse += flow.reverseStats.octetTotalCount;
        let protoSet = edge.protoPorts.get(flow.transport.protocolNumber);
        if (!protoSet) {
            protoSet = new Set();
            edge.protoPorts.set(flow.transport.protocolNumber, protoSet);
        }
        if (flow.transport.destinationPort) protoSet.add(flow.transport.destinationPort);
        if (flow.k8s.ingressNetworkPolicyName) {
            edge.ingressPolicies.add(formatPolicyInfo(flow.k8s.ingressNetworkPolicyName, flow.k8s.ingressNetworkPolicyRuleAction));
            edge.ingressActions.add(flow.k8s.ingressNetworkPolicyRuleAction);
        }
        if (flow.k8s.egressNetworkPolicyName) {
            edge.egressPolicies.add(formatPolicyInfo(flow.k8s.egressNetworkPolicyName, flow.k8s.egressNetworkPolicyRuleAction));
            edge.egressActions.add(flow.k8s.egressNetworkPolicyRuleAction);
        }
        edge.flowTypes.add(flowType);
    }

    return {
        nodes: Array.from(nodeMap.values()),
        edges: Array.from(edgeMap.values()),
    };
}

function computeRates(edge: WorkloadEdge): { connectionRate: number; bitRate: number } {
    const windowMs = edge.lastSeen - edge.firstSeen;
    if (windowMs <= 0) {
        return { connectionRate: 0, bitRate: 0 };
    }
    const windowSec = windowMs / 1000;
    return {
        connectionRate: edge.connectionCount / windowSec,
        bitRate: (edge.totalBytesForward + edge.totalBytesReverse) * 8 / windowSec,
    };
}

function formatBitRate(bitsPerSec: number): string {
    if (bitsPerSec === 0) return '0 bps';
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps'];
    const i = Math.min(Math.floor(Math.log(bitsPerSec) / Math.log(1000)), units.length - 1);
    const value = bitsPerSec / Math.pow(1000, i);
    return `${value.toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

function edgeToDetails(edge: WorkloadEdge): EdgeDetails {
    const { connectionRate, bitRate } = computeRates(edge);
    const destPortsList: string[] = [];
    Array.from(edge.protoPorts.entries()).forEach(([proto, ports]) => {
        const protoName = getProtocolName(proto);
        if (ports.size > 0) {
            const sortedPorts = Array.from(ports).sort((a, b) => a - b).join(', ');
            destPortsList.push(`${protoName}(${sortedPorts})`);
        } else {
            destPortsList.push(protoName);
        }
    });
    return {
        source: edge.source,
        target: edge.target,
        connectionCount: edge.connectionCount,
        connectionRate,
        totalBytesForward: edge.totalBytesForward,
        totalBytesReverse: edge.totalBytesReverse,
        bitRate,
        destPortsStr: destPortsList.join(', '),
        ingressPolicies: Array.from(edge.ingressPolicies),
        egressPolicies: Array.from(edge.egressPolicies),
        flowTypes: Array.from(edge.flowTypes).map(ft => flowTypeLabel[ft] ?? 'Unknown'),
    };
}

const EDGE_COLOR_ALLOW = '#3ebd93';
const EDGE_COLOR_DROP = '#e45454';
const EDGE_COLOR_DEFAULT = '#6a9fb5';

function getEdgeColor(edge: WorkloadEdge): string {
    const allActions = new Set([...edge.ingressActions, ...edge.egressActions]);
    if (allActions.has(NetworkPolicyRuleAction.Drop) || allActions.has(NetworkPolicyRuleAction.Reject)) {
        return EDGE_COLOR_DROP;
    }
    if (allActions.has(NetworkPolicyRuleAction.Allow)) {
        return EDGE_COLOR_ALLOW;
    }
    return EDGE_COLOR_DEFAULT;
}

function getEdgeLabel(edge: WorkloadEdge): string {
    const protocols = Array.from(edge.protoPorts.keys()).map(getProtocolName);
    const ports = Array.from(edge.protoPorts.values()).flatMap(s => Array.from(s)).sort((a, b) => a - b);
    if (protocols.length === 0 && ports.length === 0) return '';
    const proto = protocols[0] || '?';
    const port = ports[0];
    let label = port ? `${proto}/${port}` : proto;
    const extras = Math.max(protocols.length, ports.length) - 1;
    if (extras > 0) label += ` +${extras}`;
    return label;
}

interface D3Node extends d3.SimulationNodeDatum {
    id: string;
    shortName: string;
    namespace: string;
    isExternal: boolean;
}

interface D3Link extends d3.SimulationLinkDatum<D3Node> {
    edgeKey: string;
    connectionCount: number;
    color: string;
    label: string;
    isBidirectional: boolean;
    curveOffset: number;
}

const WIDTH = 1100;
const HEIGHT = 700;
const NODE_RX = 8;
const NODE_PADDING_X = 14;
const NODE_PADDING_Y = 8;
const EXTERNAL_SIZE = 20;

interface ServiceMapProps {
    entries: FlowEntry[];
}

function EdgeDetailsPanel({ details, onClose }: { details: EdgeDetails; onClose: () => void }) {
    return (
        <div style={{
            position: 'absolute',
            top: '10px',
            right: '10px',
            background: 'var(--cds-alias-object-container-background, #1b2a32)',
            border: '1px solid var(--cds-alias-object-border-color, #565656)',
            borderRadius: '4px',
            padding: '16px',
            minWidth: '280px',
            maxWidth: '350px',
            zIndex: 10,
        }}>
            <div cds-layout="horizontal justify:space-between align:vertical-center" style={{ marginBottom: '12px' }}>
                <span cds-text="section">Connection Stats</span>
                <button
                    onClick={onClose}
                    style={{
                        background: 'none',
                        border: 'none',
                        color: 'var(--cds-global-typography-color-400, #fff)',
                        cursor: 'pointer',
                        fontSize: '18px',
                    }}
                >
                    ✕
                </button>
            </div>
            <div cds-layout="vertical gap:sm">
                <div><strong>Source:</strong> {getWorkloadShortName(details.source)}</div>
                <div><strong>Target:</strong> {getWorkloadShortName(details.target)}</div>
                <div><strong>Connections:</strong> {details.connectionCount}{details.connectionRate > 0 ? ` (${details.connectionRate.toFixed(2)}/s)` : ''}</div>
                <div><strong>Bytes (Fwd):</strong> {formatBytes(details.totalBytesForward)}</div>
                <div><strong>Bytes (Rev):</strong> {formatBytes(details.totalBytesReverse)}</div>
                {details.bitRate > 0 && (
                    <div><strong>Bit Rate:</strong> {formatBitRate(details.bitRate)}</div>
                )}
                <div><strong>Dest Ports:</strong> {details.destPortsStr || '-'}</div>
                {details.ingressPolicies.length > 0 && (
                    <div><strong>Ingress Policies:</strong> {details.ingressPolicies.join(', ')}</div>
                )}
                {details.egressPolicies.length > 0 && (
                    <div><strong>Egress Policies:</strong> {details.egressPolicies.join(', ')}</div>
                )}
                <div><strong>Flow Types:</strong> {details.flowTypes.join(', ') || '-'}</div>
            </div>
        </div>
    );
}

function computeTextWidth(text: string, fontSize: number): number {
    return text.length * fontSize * 0.6;
}

function curvedPath(sx: number, sy: number, tx: number, ty: number, offset: number): string {
    const dx = tx - sx;
    const dy = ty - sy;
    const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const nx = -dy / len;
    const ny = dx / len;
    const mx = (sx + tx) / 2 + nx * offset;
    const my = (sy + ty) / 2 + ny * offset;
    return `M${sx},${sy} Q${mx},${my} ${tx},${ty}`;
}

function quadMidpoint(sx: number, sy: number, tx: number, ty: number, offset: number): [number, number] {
    const dx = tx - sx;
    const dy = ty - sy;
    const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const nx = -dy / len;
    const ny = dx / len;
    return [
        (sx + tx) / 2 + nx * offset,
        (sy + ty) / 2 + ny * offset,
    ];
}

export default function ServiceMap({ entries }: ServiceMapProps) {
    const svgRef = useRef<SVGSVGElement>(null);
    const tooltipRef = useRef<HTMLDivElement>(null);
    const simulationRef = useRef<d3.Simulation<D3Node, D3Link> | null>(null);
    const [selectedEdge, setSelectedEdge] = useState<EdgeDetails | null>(null);

    const graph = useMemo(() => buildGraph(entries), [entries]);

    const prevTopologyRef = useRef<string>('');

    const topologyKey = useMemo(() => {
        const nodeIds = graph.nodes.map(n => n.id).sort().join(',');
        const edgeIds = graph.edges.map(e => `${e.source}|${e.target}`).sort().join(',');
        return `${nodeIds}::${edgeIds}`;
    }, [graph]);

    const topologyChanged = topologyKey !== prevTopologyRef.current;

    const showTooltip = useCallback((event: MouseEvent, edge: WorkloadEdge) => {
        const tip = tooltipRef.current;
        if (!tip) return;

        const protoPortParts: string[] = [];
        edge.protoPorts.forEach((pSet, proto) => {
            const pName = getProtocolName(proto);
            if (pSet.size > 0) {
                const sortedPorts = Array.from(pSet).sort((a, b) => a - b);
                protoPortParts.push(`${pName}(${sortedPorts.join(', ')})`);
            } else {
                protoPortParts.push(pName);
            }
        });
        const protoPort = protoPortParts.join(', ') || '-';

        const policyLines: string[] = [];
        for (const p of edge.ingressPolicies) {
            const isAllow = !p.includes('Drop') && !p.includes('Reject');
            policyLines.push(`<span style="color:${isAllow ? EDGE_COLOR_ALLOW : EDGE_COLOR_DROP}">${isAllow ? '&#10003;' : '&#10007;'}</span> Ingress: ${p}`);
        }
        for (const p of edge.egressPolicies) {
            const isAllow = !p.includes('Drop') && !p.includes('Reject');
            policyLines.push(`<span style="color:${isAllow ? EDGE_COLOR_ALLOW : EDGE_COLOR_DROP}">${isAllow ? '&#10003;' : '&#10007;'}</span> Egress: ${p}`);
        }

        const { connectionRate, bitRate } = computeRates(edge);
        const rateInfo = connectionRate > 0 ? ` (${connectionRate.toFixed(2)}/s)` : '';
        const bitRateInfo = bitRate > 0 ? `<div>Throughput: ${formatBitRate(bitRate)}</div>` : '';

        tip.innerHTML = `
            <div style="font-weight:600;margin-bottom:4px">${getWorkloadShortName(edge.source)} &rarr; ${getWorkloadShortName(edge.target)}</div>
            <div>${protoPort}</div>
            <div>${edge.connectionCount} connection${edge.connectionCount !== 1 ? 's' : ''}${rateInfo}</div>
            <div>&#8593; ${formatBytes(edge.totalBytesForward)} &nbsp; &#8595; ${formatBytes(edge.totalBytesReverse)}</div>
            ${bitRateInfo}
            ${policyLines.length > 0 ? '<hr style="border-color:#555;margin:4px 0"/>' + policyLines.join('<br/>') : ''}
        `;
        tip.style.left = `${event.pageX + 12}px`;
        tip.style.top = `${event.pageY + 12}px`;
        tip.style.opacity = '1';
        tip.style.pointerEvents = 'none';
    }, []);

    const hideTooltip = useCallback(() => {
        const tip = tooltipRef.current;
        if (tip) tip.style.opacity = '0';
    }, []);

    useEffect(() => {
        if (!svgRef.current) return;

        const svg = d3.select(svgRef.current);

        if (topologyChanged) {
            prevTopologyRef.current = topologyKey;

            svg.selectAll('*').remove();

            const bidirectionalPairs = new Set<string>();
            const edgeKeys = new Set(graph.edges.map(e => `${e.source}|${e.target}`));
            for (const e of graph.edges) {
                if (edgeKeys.has(`${e.target}|${e.source}`)) {
                    bidirectionalPairs.add([e.source, e.target].sort().join('|'));
                }
            }

            const defs = svg.append('defs');

            [EDGE_COLOR_ALLOW, EDGE_COLOR_DROP, EDGE_COLOR_DEFAULT].forEach(color => {
                defs.append('marker')
                    .attr('id', `arrowhead-${color.replace('#', '')}`)
                    .attr('viewBox', '0 -5 10 10')
                    .attr('refX', 10)
                    .attr('refY', 0)
                    .attr('markerWidth', 7)
                    .attr('markerHeight', 7)
                    .attr('orient', 'auto')
                    .append('path')
                    .attr('d', 'M0,-4L10,0L0,4')
                    .attr('fill', color);
            });

            const d3Nodes: D3Node[] = graph.nodes.map(n => ({
                ...n,
                x: undefined,
                y: undefined,
            }));

            const nodeById = new Map(d3Nodes.map(n => [n.id, n]));

            const CURVE_OFFSET = 30;
            const d3Links: D3Link[] = graph.edges.map(e => {
                const color = getEdgeColor(e);
                const pairKey = [e.source, e.target].sort().join('|');
                const isBi = bidirectionalPairs.has(pairKey);
                const isFirst = isBi && e.source < e.target;
                return {
                    source: nodeById.get(e.source)!,
                    target: nodeById.get(e.target)!,
                    edgeKey: `${e.source}|${e.target}`,
                    connectionCount: e.connectionCount,
                    color,
                    label: getEdgeLabel(e),
                    isBidirectional: isBi,
                    curveOffset: isBi ? (isFirst ? CURVE_OFFSET : -CURVE_OFFSET) : 0,
                };
            });

            const container = svg.append('g');

            const zoom = d3.zoom<SVGSVGElement, unknown>()
                .scaleExtent([0.3, 3])
                .on('zoom', (event) => {
                    container.attr('transform', event.transform);
                });
            svg.call(zoom);

            const namespaces = new Map<string, D3Node[]>();
            for (const n of d3Nodes) {
                if (n.isExternal || !n.namespace) continue;
                let arr = namespaces.get(n.namespace);
                if (!arr) {
                    arr = [];
                    namespaces.set(n.namespace, arr);
                }
                arr.push(n);
            }

            const nsHulls = container.append('g').attr('class', 'ns-hulls');
            const nsGroups: { ns: string; nodes: D3Node[]; path: d3.Selection<SVGPathElement, unknown, null, undefined>; label: d3.Selection<SVGTextElement, unknown, null, undefined> }[] = [];
            for (const [ns, nodes] of namespaces) {
                if (nodes.length < 1) continue;
                const path = nsHulls.append('path')
                    .attr('fill', 'rgba(106,159,181,0.08)')
                    .attr('stroke', 'rgba(106,159,181,0.25)')
                    .attr('stroke-width', 1)
                    .attr('stroke-dasharray', '4,2');
                const label = nsHulls.append('text')
                    .text(ns)
                    .attr('fill', 'rgba(106,159,181,0.5)')
                    .attr('font-size', '10px')
                    .attr('font-weight', '600');
                nsGroups.push({ ns, nodes, path, label });
            }

            const linkGroup = container.append('g');
            const linkPaths = linkGroup.selectAll<SVGPathElement, D3Link>('path')
                .data(d3Links)
                .join('path')
                .attr('fill', 'none')
                .attr('stroke', d => d.color)
                .attr('stroke-width', d => Math.min(1.5 + Math.log2(d.connectionCount + 1), 6))
                .attr('stroke-opacity', 0.6)
                .attr('marker-end', d => `url(#arrowhead-${d.color.replace('#', '')})`)
                .style('cursor', 'pointer')
                .on('mouseenter', function (event, d) {
                    d3.select(this)
                        .attr('stroke-opacity', 1)
                        .attr('stroke-width', Math.min(1.5 + Math.log2(d.connectionCount + 1), 6) + 1.5);
                    const edgeData = graph.edges.find(e => `${e.source}|${e.target}` === d.edgeKey);
                    if (edgeData) showTooltip(event, edgeData);
                })
                .on('mousemove', function (event) {
                    const tip = tooltipRef.current;
                    if (tip) {
                        tip.style.left = `${event.pageX + 12}px`;
                        tip.style.top = `${event.pageY + 12}px`;
                    }
                })
                .on('mouseleave', function (_event, d) {
                    d3.select(this)
                        .attr('stroke-opacity', 0.6)
                        .attr('stroke-width', Math.min(1.5 + Math.log2(d.connectionCount + 1), 6));
                    hideTooltip();
                })
                .on('click', (_event, d) => {
                    const edgeData = graph.edges.find(e => `${e.source}|${e.target}` === d.edgeKey);
                    if (edgeData) setSelectedEdge(edgeToDetails(edgeData));
                });

            const edgeLabelGroup = container.append('g');
            const edgeLabels = edgeLabelGroup.selectAll<SVGGElement, D3Link>('g')
                .data(d3Links.filter(d => d.label))
                .join('g');

            edgeLabels.append('rect')
                .attr('rx', 3)
                .attr('ry', 3)
                .attr('fill', 'rgba(23,36,43,0.85)')
                .attr('stroke', d => d.color)
                .attr('stroke-width', 0.5)
                .attr('stroke-opacity', 0.5);

            edgeLabels.append('text')
                .text(d => d.label)
                .attr('text-anchor', 'middle')
                .attr('dominant-baseline', 'central')
                .attr('fill', '#c5d1d8')
                .attr('font-size', '9px')
                .attr('pointer-events', 'none');

            const nodeGroup = container.append('g');
            const node = nodeGroup
                .selectAll<SVGGElement, D3Node>('g')
                .data(d3Nodes)
                .join('g')
                .style('cursor', 'grab')
                .call(d3.drag<SVGGElement, D3Node>()
                    .on('start', (event, d) => {
                        if (!event.active) simulation.alphaTarget(0.3).restart();
                        d.fx = d.x;
                        d.fy = d.y;
                    })
                    .on('drag', (event, d) => {
                        d.fx = event.x;
                        d.fy = event.y;
                    })
                    .on('end', (event, d) => {
                        if (!event.active) simulation.alphaTarget(0);
                        d.fx = null;
                        d.fy = null;
                    })
                );

            node.each(function (d) {
                const g = d3.select(this);
                if (d.isExternal) {
                    const s = EXTERNAL_SIZE;
                    g.append('polygon')
                        .attr('points', `0,${-s} ${s},0 0,${s} ${-s},0`)
                        .attr('fill', '#3d2c1e')
                        .attr('stroke', '#c4956a')
                        .attr('stroke-width', 2);
                    g.append('text')
                        .text(d.shortName)
                        .attr('dy', s + 14)
                        .attr('text-anchor', 'middle')
                        .attr('fill', '#c4956a')
                        .attr('font-size', '11px')
                        .attr('font-weight', '600');
                } else {
                    const nameW = computeTextWidth(d.shortName, 12);
                    const nsW = computeTextWidth(d.namespace, 9);
                    const w = Math.max(nameW, nsW) + NODE_PADDING_X * 2;
                    const h = 38 + NODE_PADDING_Y;

                    g.append('rect')
                        .attr('x', -w / 2)
                        .attr('y', -h / 2)
                        .attr('width', w)
                        .attr('height', h)
                        .attr('rx', NODE_RX)
                        .attr('ry', NODE_RX)
                        .attr('fill', '#1e3a4c')
                        .attr('stroke', '#6a9fb5')
                        .attr('stroke-width', 1.5);

                    g.append('text')
                        .text(d.shortName)
                        .attr('dy', -3)
                        .attr('text-anchor', 'middle')
                        .attr('fill', '#e0e8ec')
                        .attr('font-size', '12px')
                        .attr('font-weight', '600');

                    g.append('text')
                        .text(d.namespace)
                        .attr('dy', 13)
                        .attr('text-anchor', 'middle')
                        .attr('fill', 'rgba(106,159,181,0.7)')
                        .attr('font-size', '9px');
                }
            });

            const HULL_PADDING = 50;

            function updateHulls() {
                for (const { nodes, path, label } of nsGroups) {
                    if (nodes.length === 1) {
                        const n = nodes[0];
                        const px = HULL_PADDING;
                        const py = HULL_PADDING;
                        path.attr('d', `M${n.x! - px},${n.y! - py} L${n.x! + px},${n.y! - py} L${n.x! + px},${n.y! + py} L${n.x! - px},${n.y! + py} Z`);
                        label.attr('x', n.x! - px + 6).attr('y', n.y! - py + 12);
                        continue;
                    }
                    const points: [number, number][] = [];
                    for (const n of nodes) {
                        const x = n.x!;
                        const y = n.y!;
                        points.push([x - HULL_PADDING, y - HULL_PADDING]);
                        points.push([x + HULL_PADDING, y - HULL_PADDING]);
                        points.push([x + HULL_PADDING, y + HULL_PADDING]);
                        points.push([x - HULL_PADDING, y + HULL_PADDING]);
                    }
                    const hull = d3.polygonHull(points);
                    if (hull) {
                        path.attr('d', `M${hull.map(p => p.join(',')).join('L')}Z`);
                        const minX = d3.min(hull, p => p[0])!;
                        const minY = d3.min(hull, p => p[1])!;
                        label.attr('x', minX + 6).attr('y', minY + 12);
                    }
                }
            }

            function nodeRadius(d: D3Node): number {
                if (d.isExternal) return EXTERNAL_SIZE + 4;
                const nameW = computeTextWidth(d.shortName, 12);
                const nsW = computeTextWidth(d.namespace, 9);
                return Math.max(nameW, nsW) / 2 + NODE_PADDING_X + 4;
            }

            function shortenEdge(sx: number, sy: number, tx: number, ty: number, shrink: number): [number, number, number, number] {
                const dx = tx - sx;
                const dy = ty - sy;
                const len = Math.sqrt(dx * dx + dy * dy) || 1;
                const ux = dx / len;
                const uy = dy / len;
                return [sx + ux * shrink, sy + uy * shrink, tx - ux * shrink, ty - uy * shrink];
            }

            const simulation = d3.forceSimulation(d3Nodes)
                .force('link', d3.forceLink<D3Node, D3Link>(d3Links).id(d => d.id).distance(220))
                .force('charge', d3.forceManyBody().strength(-800))
                .force('center', d3.forceCenter(WIDTH / 2, HEIGHT / 2))
                .force('collision', d3.forceCollide<D3Node>().radius(d => nodeRadius(d) + 30))
                .on('tick', () => {
                    linkPaths.attr('d', d => {
                        const s = d.source as D3Node;
                        const t = d.target as D3Node;
                        const sR = nodeRadius(s);
                        const tR = nodeRadius(t);
                        const [sx, sy] = shortenEdge(s.x!, s.y!, t.x!, t.y!, Math.min(sR, 20));
                        const [, , tx2, ty2] = shortenEdge(s.x!, s.y!, t.x!, t.y!, tR);
                        return curvedPath(sx, sy, tx2, ty2, d.curveOffset);
                    });

                    edgeLabels.attr('transform', d => {
                        const s = d.source as D3Node;
                        const t = d.target as D3Node;
                        const [mx, my] = quadMidpoint(s.x!, s.y!, t.x!, t.y!, d.curveOffset);
                        return `translate(${mx},${my})`;
                    });

                    edgeLabels.each(function () {
                        const g = d3.select(this);
                        const textEl = g.select('text');
                        const bbox = (textEl.node() as SVGTextElement)?.getBBox();
                        if (bbox) {
                            g.select('rect')
                                .attr('x', -bbox.width / 2 - 3)
                                .attr('y', -bbox.height / 2 - 1)
                                .attr('width', bbox.width + 6)
                                .attr('height', bbox.height + 2);
                        }
                    });

                    node.attr('transform', d => `translate(${d.x},${d.y})`);

                    updateHulls();
                });

            simulationRef.current = simulation;
        } else {
            const container = svg.select('g');
            container.selectAll<SVGPathElement, D3Link>('path')
                .filter(function () {
                    return d3.select(this).attr('fill') === 'none';
                })
                .attr('stroke-width', d => {
                    if (!d) return 1;
                    const edgeData = graph.edges.find(e => `${e.source}|${e.target}` === (d as D3Link).edgeKey);
                    return edgeData ? Math.min(1.5 + Math.log2(edgeData.connectionCount + 1), 6) : 1;
                });
        }
    }, [graph, topologyChanged, topologyKey, showTooltip, hideTooltip]);

    useEffect(() => {
        return () => {
            simulationRef.current?.stop();
        };
    }, []);

    const handleCloseDetails = useCallback(() => setSelectedEdge(null), []);

    return (
        <div style={{ position: 'relative' }}>
            <svg
                ref={svgRef}
                width={WIDTH}
                height={HEIGHT}
                style={{
                    border: '1px solid var(--cds-alias-object-border-color, #565656)',
                    borderRadius: '4px',
                    background: 'var(--cds-alias-object-container-background-dark, #17242b)',
                }}
            />
            <div
                ref={tooltipRef}
                style={{
                    position: 'fixed',
                    opacity: 0,
                    background: 'rgba(20,32,40,0.95)',
                    border: '1px solid #555',
                    borderRadius: '4px',
                    padding: '8px 10px',
                    fontSize: '11px',
                    color: '#d0dae0',
                    lineHeight: '1.5',
                    maxWidth: '320px',
                    zIndex: 100,
                    transition: 'opacity 150ms ease',
                    pointerEvents: 'none',
                }}
            />
            {/* Legend */}
            <div style={{
                position: 'absolute',
                bottom: '10px',
                left: '10px',
                background: 'rgba(20,32,40,0.9)',
                border: '1px solid rgba(86,86,86,0.5)',
                borderRadius: '4px',
                padding: '8px 12px',
                fontSize: '10px',
                color: '#adbbc4',
                lineHeight: '1.8',
            }}>
                <div style={{ fontWeight: 600, marginBottom: '2px' }}>Legend</div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    <svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke={EDGE_COLOR_ALLOW} strokeWidth="2" /></svg>
                    <span>Allow</span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    <svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke={EDGE_COLOR_DROP} strokeWidth="2" /></svg>
                    <span>Drop / Reject</span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    <svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke={EDGE_COLOR_DEFAULT} strokeWidth="2" /></svg>
                    <span>No policy</span>
                </div>
                <div style={{ marginTop: '2px', color: '#8899a4' }}>
                    Line thickness = connection count
                </div>
            </div>
            {selectedEdge && (
                <EdgeDetailsPanel details={selectedEdge} onClose={handleCloseDetails} />
            )}
        </div>
    );
}
