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

import { LitElement, html, css, nothing } from 'lit';
import { property, state, query } from 'lit/decorators.js';
import * as d3 from 'd3';
import { pageStyles } from '../lib/styles';
import { FlowStore, FlowEntry, entryBitRate } from '../lib/flow-store';
import {
    FlowType,
    NetworkPolicyRuleAction,
    flowTypeLabel,
    getProtocolName,
    formatEndpoint,
    formatPolicyInfo,
    formatBytes,
    destinationK8sServiceFilterKey,
} from '../lib/flow-types';
import {
    FlowStreamClient,
    FlowStreamFilter,
    FlowFilterDirection,
    FlowTypeName,
    streamFilterKey,
} from '../lib/flow-stream';
import '../antrea-button';
import '../antrea-alert';

// ── Graph types & helpers ────────────────────────────────────────────────────

const WELL_KNOWN_APP_LABELS = ['app.kubernetes.io/name', 'app.kubernetes.io/instance', 'app', 'k8s-app', 'name'];

function getWorkloadName(ns: string, pod: string, labels?: Record<string, string>): string {
    if (labels) {
        for (const l of WELL_KNOWN_APP_LABELS) {
            if (labels[l]) return `${ns}/${labels[l]}`;
        }
    }
    return `${ns}/${pod.replace(/-[a-z0-9]{5,10}(-[a-z0-9]{5})?$/, '')}`;
}
function workloadShortName(id: string): string { return id.split('/').pop() ?? id; }

interface WorkloadNode { id: string; shortName: string; namespace: string; isExternal: boolean; }
interface WorkloadEdge {
    source: string; target: string;
    connectionCount: number; totalBytesForward: number; totalBytesReverse: number; bitRate: number;
    protoPorts: Map<number, Set<number>>;
    ingressPolicies: Set<string>; egressPolicies: Set<string>;
    ingressActions: Set<NetworkPolicyRuleAction>; egressActions: Set<NetworkPolicyRuleAction>;
    flowTypes: Set<FlowType>;
}
interface EdgeDetails {
    source: string; target: string;
    connectionCount: number; totalBytesForward: number; totalBytesReverse: number; bitRate: number;
    destPortsStr: string;
    ingressPolicies: string[]; egressPolicies: string[]; flowTypes: string[];
}
interface GraphData { nodes: WorkloadNode[]; edges: WorkloadEdge[]; edgeMap: Map<string, WorkloadEdge>; }

function buildGraph(entries: FlowEntry[]): GraphData {
    const nodeMap = new Map<string, WorkloadNode>();
    const edgeMap = new Map<string, WorkloadEdge>();
    for (const entry of entries) {
        const { flow } = entry;
        const flowType = flow.k8s.flowType as FlowType;
        let srcId: string;
        let dstId: string;
        if (flowType === FlowType.FromExternal) {
            srcId = 'external';
            if (!nodeMap.has(srcId)) nodeMap.set(srcId, { id: srcId, shortName: 'External', namespace: '', isExternal: true });
        } else {
            srcId = getWorkloadName(flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.k8s.sourcePodLabels);
            if (!nodeMap.has(srcId)) nodeMap.set(srcId, { id: srcId, shortName: workloadShortName(srcId), namespace: flow.k8s.sourcePodNamespace, isExternal: false });
        }
        if (flowType === FlowType.ToExternal) {
            dstId = 'external';
            if (!nodeMap.has(dstId)) nodeMap.set(dstId, { id: dstId, shortName: 'External', namespace: '', isExternal: true });
        } else {
            const svcKey = flow.k8s.destinationServicePortName ? destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName) : '';
            dstId = svcKey || getWorkloadName(flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.k8s.destinationPodLabels);
            if (!nodeMap.has(dstId)) nodeMap.set(dstId, { id: dstId, shortName: workloadShortName(dstId), namespace: flow.k8s.destinationPodNamespace, isExternal: false });
        }
        if (srcId === dstId) continue;
        const edgeKey = `${srcId}|${dstId}`;
        let edge = edgeMap.get(edgeKey);
        if (!edge) {
            edge = { source: srcId, target: dstId, connectionCount: 0, totalBytesForward: 0, totalBytesReverse: 0, bitRate: 0, protoPorts: new Map(), ingressPolicies: new Set(), egressPolicies: new Set(), ingressActions: new Set(), egressActions: new Set(), flowTypes: new Set() };
            edgeMap.set(edgeKey, edge);
        }
        edge.connectionCount++;
        edge.totalBytesForward += flow.stats.octetTotalCount;
        edge.totalBytesReverse += flow.reverseStats.octetTotalCount;
        edge.bitRate += entryBitRate(entry);
        let protoSet = edge.protoPorts.get(flow.transport.protocolNumber);
        if (!protoSet) { protoSet = new Set(); edge.protoPorts.set(flow.transport.protocolNumber, protoSet); }
        if (flow.transport.destinationPort) protoSet.add(flow.transport.destinationPort);
        if (flow.k8s.ingressNetworkPolicyName) { edge.ingressPolicies.add(formatPolicyInfo(flow.k8s.ingressNetworkPolicyName, flow.k8s.ingressNetworkPolicyRuleAction)); edge.ingressActions.add(flow.k8s.ingressNetworkPolicyRuleAction); }
        if (flow.k8s.egressNetworkPolicyName) { edge.egressPolicies.add(formatPolicyInfo(flow.k8s.egressNetworkPolicyName, flow.k8s.egressNetworkPolicyRuleAction)); edge.egressActions.add(flow.k8s.egressNetworkPolicyRuleAction); }
        edge.flowTypes.add(flowType);
    }
    return { nodes: Array.from(nodeMap.values()), edges: Array.from(edgeMap.values()), edgeMap };
}

function formatBitRate(bps: number): string {
    if (bps === 0) return '0 bps';
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps'];
    const i = Math.min(Math.floor(Math.log(bps) / Math.log(1000)), units.length - 1);
    return `${(bps / Math.pow(1000, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

function edgeToDetails(edge: WorkloadEdge): EdgeDetails {
    const destPortsList: string[] = [];
    edge.protoPorts.forEach((ports, proto) => {
        const name = getProtocolName(proto);
        destPortsList.push(ports.size > 0 ? `${name}(${Array.from(ports).sort((a, b) => a - b).join(', ')})` : name);
    });
    return {
        source: edge.source, target: edge.target,
        connectionCount: edge.connectionCount, totalBytesForward: edge.totalBytesForward, totalBytesReverse: edge.totalBytesReverse, bitRate: edge.bitRate,
        destPortsStr: destPortsList.join(', '),
        ingressPolicies: Array.from(edge.ingressPolicies),
        egressPolicies: Array.from(edge.egressPolicies),
        flowTypes: Array.from(edge.flowTypes).map(ft => flowTypeLabel[ft] ?? 'Unknown'),
    };
}

const EDGE_ALLOW = '#3ebd93';
const EDGE_DROP = '#e45454';
const EDGE_DEFAULT = '#6a9fb5';

function edgeColor(edge: WorkloadEdge): string {
    const all = new Set([...edge.ingressActions, ...edge.egressActions]);
    if (all.has(NetworkPolicyRuleAction.Drop) || all.has(NetworkPolicyRuleAction.Reject)) return EDGE_DROP;
    if (all.has(NetworkPolicyRuleAction.Allow)) return EDGE_ALLOW;
    return EDGE_DEFAULT;
}

function edgeLabel(edge: WorkloadEdge): string {
    const protocols = Array.from(edge.protoPorts.keys()).map(getProtocolName);
    const ports = Array.from(edge.protoPorts.values()).flatMap(s => Array.from(s)).sort((a, b) => a - b);
    if (!protocols.length && !ports.length) return '';
    let label = ports[0] ? `${protocols[0] ?? '?'}/${ports[0]}` : (protocols[0] ?? '?');
    const extras = Math.max(protocols.length, ports.length) - 1;
    if (extras > 0) label += ` +${extras}`;
    return label;
}

// ── D3 node geometry ─────────────────────────────────────────────────────────

interface D3Node extends d3.SimulationNodeDatum { id: string; shortName: string; namespace: string; isExternal: boolean; }
interface D3Link extends d3.SimulationLinkDatum<D3Node> { edgeKey: string; connectionCount: number; color: string; label: string; curveOffset: number; }

const HEIGHT = 900;
const NODE_RX = 8;
const NODE_PADDING_X = 14;
const NODE_PADDING_Y = 8;
const EXTERNAL_SIZE = 20;
const CURVE_OFFSET = 30;

function textWidth(text: string, size: number): number { return text.length * size * 0.6; }

function nodeHalfSize(d: D3Node): { hw: number; hh: number } {
    if (d.isExternal) return { hw: EXTERNAL_SIZE, hh: EXTERNAL_SIZE };
    const w = Math.max(textWidth(d.shortName, 12), textWidth(d.namespace, 9)) + NODE_PADDING_X * 2;
    return { hw: w / 2, hh: (38 + NODE_PADDING_Y) / 2 };
}

function nodeBoundary(d: D3Node, tx: number, ty: number, gap: number): [number, number] {
    const cx = d.x ?? 0; const cy = d.y ?? 0;
    const dx = tx - cx; const dy = ty - cy;
    const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const ux = dx / len; const uy = dy / len;
    const { hw, hh } = nodeHalfSize(d);
    let scale: number;
    if (d.isExternal) scale = hw / (Math.abs(ux) + Math.abs(uy));
    else if (Math.abs(ux) * hh > Math.abs(uy) * hw) scale = hw / Math.abs(ux);
    else scale = hh / Math.abs(uy);
    return [cx + ux * (scale + gap), cy + uy * (scale + gap)];
}

function curvedPath(sx: number, sy: number, tx: number, ty: number, offset: number): string {
    const dx = tx - sx; const dy = ty - sy; const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const nx = -dy / len; const ny = dx / len;
    const mx = (sx + tx) / 2 + nx * offset; const my = (sy + ty) / 2 + ny * offset;
    return `M${sx},${sy} Q${mx},${my} ${tx},${ty}`;
}

function quadMidpoint(sx: number, sy: number, tx: number, ty: number, offset: number): [number, number] {
    const dx = tx - sx; const dy = ty - sy; const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const nx = -dy / len; const ny = dx / len;
    return [(sx + tx) / 2 + nx * offset, (sy + ty) / 2 + ny * offset];
}

function nodeCollideRadius(d: D3Node): number {
    if (d.isExternal) return EXTERNAL_SIZE + 4;
    return Math.max(textWidth(d.shortName, 12), textWidth(d.namespace, 9)) / 2 + NODE_PADDING_X + 4;
}

// ── Sort / filter helpers (flow list) ────────────────────────────────────────

type SortField = 'lastSeen' | 'source' | 'destination' | 'destinationService' | 'protocol' | 'destPort' | 'bytesFwd' | 'bytesRev' | 'ingressPolicy' | 'egressPolicy' | 'flowType';
type SortDir = 'asc' | 'desc';

function sortValue(entry: FlowEntry, field: SortField): string | number {
    const { flow } = entry;
    switch (field) {
        case 'lastSeen': return new Date(flow.endTs).getTime();
        case 'source': return formatEndpoint(flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.ip.source);
        case 'destination': return formatEndpoint(flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.ip.destination);
        case 'destinationService': return destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName) || flow.k8s.destinationServicePortName;
        case 'protocol': return flow.transport.protocolNumber;
        case 'destPort': return flow.transport.destinationPort;
        case 'bytesFwd': return flow.stats.octetTotalCount;
        case 'bytesRev': return flow.reverseStats.octetTotalCount;
        case 'ingressPolicy': return flow.k8s.ingressNetworkPolicyName;
        case 'egressPolicy': return flow.k8s.egressNetworkPolicyName;
        case 'flowType': return flow.k8s.flowType;
    }
}

function matchesText(entry: FlowEntry, text: string): boolean {
    if (!text) return true;
    const lower = text.toLowerCase();
    const { flow } = entry;
    return [
        flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.ip.source,
        flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.ip.destination,
        flow.k8s.destinationServicePortName, destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName),
        getProtocolName(flow.transport.protocolNumber), flow.transport.destinationPort.toString(),
        flowTypeLabel[flow.k8s.flowType as FlowType] ?? '',
        flow.k8s.ingressNetworkPolicyName, flow.k8s.egressNetworkPolicyName,
    ].some(s => s.toLowerCase().includes(lower));
}

const COLUMNS: { field: SortField; label: string }[] = [
    { field: 'lastSeen', label: 'Last Seen' },
    { field: 'source', label: 'Source' },
    { field: 'destination', label: 'Destination' },
    { field: 'destinationService', label: 'Dest Service' },
    { field: 'protocol', label: 'Protocol' },
    { field: 'destPort', label: 'Dest Port' },
    { field: 'bytesFwd', label: 'Bytes (Fwd)' },
    { field: 'bytesRev', label: 'Bytes (Rev)' },
    { field: 'ingressPolicy', label: 'Ingress Policy' },
    { field: 'egressPolicy', label: 'Egress Policy' },
    { field: 'flowType', label: 'Flow Type' },
];

// ── Component ─────────────────────────────────────────────────────────────────

export class AntreaFlowVisibilityPage extends LitElement {
    static styles = [pageStyles, css`
        .filter-bar { display: flex; flex-direction: column; gap: 0.75rem; }
        .filter-row { display: flex; align-items: flex-end; gap: 1rem; flex-wrap: wrap; }
        .filter-actions { display: flex; gap: 0.5rem; align-items: flex-end; flex-shrink: 0; }
        .status-row { display: flex; align-items: center; gap: 1.5rem; flex-wrap: wrap; font-size: 0.75rem; color: var(--antrea-color-text-muted, #adbbc4); }
        .status-dot { width: 8px; height: 8px; border-radius: 50%; display: inline-block; flex-shrink: 0; }

        .multiselect { position: relative; min-width: 160px; }
        .multiselect-label { display: block; font-size: 11px; color: var(--antrea-color-text-muted, #adbbc4); margin-bottom: 4px; }
        .multiselect-btn {
            width: 100%; padding: 6px 28px 6px 10px;
            background: var(--antrea-color-bg, #1b2a32);
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: 4px; color: var(--antrea-color-text, #e9ecef);
            font-size: 13px; text-align: left; cursor: pointer;
            white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
            position: relative;
        }
        .multiselect-chevron { position: absolute; right: 8px; top: 50%; transform: translateY(-50%); font-size: 10px; pointer-events: none; }
        .multiselect-dropdown {
            position: absolute; top: 100%; left: 0; right: 0;
            max-height: 200px; overflow-y: auto;
            background: var(--antrea-color-bg, #1b2a32);
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: 0 0 4px 4px; z-index: 50;
        }
        .multiselect-option {
            display: flex; align-items: center; gap: 8px;
            padding: 5px 10px; cursor: pointer; font-size: 12px;
            color: var(--antrea-color-text, #e9ecef);
        }
        .multiselect-option.selected { background: var(--antrea-color-bg-hover, #2e3f4d); }

        .flow-list-header { display: flex; align-items: center; gap: 1rem; }
        .flow-list-scroll { max-height: 70vh; overflow-x: auto; overflow-y: auto; }
        .flow-filter-input {
            padding: 6px 12px;
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: 4px;
            background: var(--antrea-color-bg, #1b2a32);
            color: var(--antrea-color-text, #e9ecef);
            min-width: 300px;
            font-size: 0.875rem;
        }

        .map-container { position: relative; width: 100%; }
        .map-svg { display: block; border: 1px solid var(--antrea-color-border, #314351); border-radius: 4px; background: var(--antrea-color-bg-dark, #17242b); }
        .map-tooltip {
            position: fixed; opacity: 0;
            background: rgba(20,32,40,0.95); border: 1px solid #555;
            border-radius: 4px; padding: 8px 10px; font-size: 11px;
            color: #d0dae0; line-height: 1.5; max-width: 320px;
            z-index: 100; transition: opacity 150ms ease; pointer-events: none;
        }
        .map-legend {
            position: absolute; bottom: 10px; left: 10px;
            background: rgba(20,32,40,0.9); border: 1px solid rgba(86,86,86,0.5);
            border-radius: 4px; padding: 8px 12px; font-size: 10px;
            color: #adbbc4; line-height: 1.8;
        }
        .map-legend-title { font-weight: 600; margin-bottom: 2px; }
        .legend-row { display: flex; align-items: center; gap: 6px; }
        .edge-details {
            position: absolute; top: 10px; right: 10px;
            background: var(--antrea-color-bg, #1b2a32);
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: 4px; padding: 16px; min-width: 280px; max-width: 350px; z-index: 10;
        }
        .edge-details-close {
            position: absolute; top: 8px; right: 8px;
            background: none; border: none; color: var(--antrea-color-text-muted, #adbbc4);
            cursor: pointer; font-size: 14px; line-height: 1; padding: 2px;
        }
        .edge-details-section-label {
            font-weight: 500; text-transform: uppercase; letter-spacing: 0.05em;
            color: var(--antrea-color-text-muted, #adbbc4); font-size: 11px; margin-bottom: 12px;
        }
        .edge-details-rows { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.8125rem; }
        .warn { color: var(--antrea-color-warning, #f5a623); }
    `];

    @property() token = '';

    // View
    @state() private _viewMode: 'list' | 'map' = 'list';
    @state() private _paused = false;

    // Stream
    @state() private _entries: FlowEntry[] = [];
    @state() private _connected = false;
    @state() private _error: string | null = null;
    @state() private _droppedCount = 0;
    @state() private _evictionWarning = false;

    // Filters (applied)
    @state() private _filter: FlowStreamFilter = {};

    // Filter UI state
    @state() private _pendingNs: string[] = [];
    @state() private _pendingPods: string[] = [];
    @state() private _pendingServices: string[] = [];
    @state() private _pendingFlowType: FlowTypeName | '' = '';
    @state() private _pendingDirection: FlowFilterDirection = 'both';
    @state() private _pendingIps = '';
    @state() private _pendingPodLabel = '';
    @state() private _nsOpen = false;
    @state() private _podOpen = false;
    @state() private _svcOpen = false;

    // Flow list
    @state() private _sortField: SortField = 'lastSeen';
    @state() private _sortDir: SortDir = 'desc';
    @state() private _textFilter = '';

    // Service map
    @state() private _svgWidth = 1100;
    @state() private _selectedEdgeKey: string | null = null;

    @query('#graph-svg') private _svgEl?: SVGSVGElement;
    @query('#graph-tooltip') private _tooltipEl?: HTMLDivElement;

    // Non-reactive refs
    private _store = new FlowStore();
    private _client: FlowStreamClient | null = null;
    private _simulation: d3.Simulation<D3Node, D3Link> | null = null;
    private _filterKey = streamFilterKey({});
    private _refreshTimer: ReturnType<typeof setInterval> | null = null;
    private _ro: ResizeObserver | null = null;
    private _graphRef: GraphData = { nodes: [], edges: [], edgeMap: new Map() };
    private _prevTopologyKey = '';

    private _handleDocClick = (e: PointerEvent) => {
        const path = e.composedPath();
        if (!path.some(el => el instanceof Element && (el as Element).closest?.('.multiselect'))) {
            this._nsOpen = false; this._podOpen = false; this._svcOpen = false;
        }
    };

    // ── Lifecycle ─────────────────────────────────────────────────────────────

    override connectedCallback() {
        super.connectedCallback();
        this._refreshTimer = setInterval(() => {
            if (this._store.size() > 0) this._entries = this._store.getAll();
        }, 5000);
        this._startStream();
        window.addEventListener('pointerdown', this._handleDocClick);
    }

    override disconnectedCallback() {
        super.disconnectedCallback();
        this._client?.stop();
        this._client = null;
        this._simulation?.stop();
        this._ro?.disconnect();
        if (this._refreshTimer) { clearInterval(this._refreshTimer); this._refreshTimer = null; }
        window.removeEventListener('pointerdown', this._handleDocClick);
    }

    override updated(changed: Map<string, unknown>) {
        // Setup ResizeObserver once the DOM is ready
        if (changed.has('_viewMode') && this._viewMode === 'map') {
            this._setupResizeObserver();
            this._buildServiceMap();
        }
        // Rebuild map when entries or SVG width change
        if ((changed.has('_entries') || changed.has('_svgWidth')) && this._viewMode === 'map') {
            this._buildServiceMap();
        }
        if (changed.has('token') && this.token) {
            this._client?.updateToken(this.token);
        }
    }

    // ── Stream management ─────────────────────────────────────────────────────

    private _startStream() {
        if (this._paused) return;
        this._client?.stop();
        this._client = new FlowStreamClient(this.token, this._filter, {
            onFlows: flows => {
                this._store.upsertBatch(flows);
                this._entries = this._store.getAll();
                this._evictionWarning = this._store.hasEvicted();
            },
            onError: err => { this._error = err.message; },
            onDropped: count => { this._droppedCount = count; },
            onConnected: () => { this._connected = true; this._error = null; },
            onDisconnected: () => { this._connected = false; },
            onAuthError: () => {
                this.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
            },
        });
        this._client.start();
    }

    private _stopStream() {
        this._client?.stop();
        this._client = null;
        this._connected = false;
    }

    private _applyFilter(filter: FlowStreamFilter) {
        const newKey = streamFilterKey(filter);
        if (newKey !== this._filterKey) {
            this._filterKey = newKey;
            this._filter = filter;
            this._store.clear();
            this._entries = [];
            this._evictionWarning = false;
            this._droppedCount = 0;
            this._selectedEdgeKey = null;
        }
        if (!this._paused) {
            this._client?.stop();
            this._client = null;
            this._startStream();
        }
    }

    // ── Filter actions ────────────────────────────────────────────────────────

    private _onApplyFilters() {
        this._nsOpen = false; this._podOpen = false; this._svcOpen = false;
        const filter: FlowStreamFilter = {};
        if (this._pendingNs.length) filter.namespaces = this._pendingNs;
        if (this._pendingPods.length) filter.pods = this._pendingPods;
        if (this._pendingPodLabel.trim()) filter.podLabelSelector = this._pendingPodLabel.trim();
        if (this._pendingServices.length) {
            const svcNames = this._pendingServices.map(s => destinationK8sServiceFilterKey(s)).filter(Boolean);
            if (svcNames.length) filter.services = svcNames;
        }
        if (this._pendingFlowType) filter.flowTypes = [this._pendingFlowType];
        const ips = this._pendingIps.split(',').map(s => s.trim()).filter(Boolean);
        if (ips.length) filter.ips = ips;
        if (this._pendingDirection !== 'both') filter.direction = this._pendingDirection;
        this._applyFilter(filter);
    }

    private _onResetFilters() {
        this._nsOpen = false; this._podOpen = false; this._svcOpen = false;
        this._pendingNs = []; this._pendingPods = []; this._pendingServices = [];
        this._pendingFlowType = ''; this._pendingDirection = 'both';
        this._pendingIps = ''; this._pendingPodLabel = '';
        this._applyFilter({});
    }

    private _onPauseToggle() {
        this._paused = !this._paused;
        if (this._paused) this._stopStream();
        else this._startStream();
    }

    private _onClear() {
        this._store.clear();
        this._entries = [];
        this._evictionWarning = false;
        this._droppedCount = 0;
        this._selectedEdgeKey = null;
    }

    // ── Available filter options (derived from current entries) ───────────────

    private get _availableNs(): string[] {
        const ns = new Set<string>();
        for (const e of this._entries) {
            if (e.flow.k8s.sourcePodNamespace) ns.add(e.flow.k8s.sourcePodNamespace);
            if (e.flow.k8s.destinationPodNamespace) ns.add(e.flow.k8s.destinationPodNamespace);
        }
        return Array.from(ns).sort();
    }

    private get _availablePods(): string[] {
        const pods = new Set<string>();
        for (const e of this._entries) {
            if (e.flow.k8s.sourcePodName) pods.add(e.flow.k8s.sourcePodName);
            if (e.flow.k8s.destinationPodName) pods.add(e.flow.k8s.destinationPodName);
        }
        return Array.from(pods).sort();
    }

    private get _availableServices(): string[] {
        const svcs = new Set<string>();
        for (const e of this._entries) {
            const k = destinationK8sServiceFilterKey(e.flow.k8s.destinationServicePortName);
            if (k) svcs.add(k);
        }
        return Array.from(svcs).sort();
    }

    // ── Service map ───────────────────────────────────────────────────────────

    private _setupResizeObserver() {
        this._ro?.disconnect();
        const host = this.shadowRoot?.querySelector('.map-container')?.parentElement ?? this;
        this._ro = new ResizeObserver(entries => {
            const w = entries[0]?.contentRect?.width ?? (host as HTMLElement).clientWidth;
            if (w && w > 0) this._svgWidth = Math.floor(w);
        });
        this._ro.observe(host as Element);
        const w = (host as HTMLElement).clientWidth;
        if (w && w > 0) this._svgWidth = Math.floor(w);
    }

    private _buildServiceMap() {
        const svg = this._svgEl;
        if (!svg) return;

        const graph = buildGraph(this._entries);
        this._graphRef = graph;

        const nodeIds = graph.nodes.map(n => n.id).sort().join(',');
        const edgeIds = graph.edges.map(e => `${e.source}|${e.target}`).sort().join(',');
        const topologyKey = `${nodeIds}::${edgeIds}::${this._svgWidth}`;

        if (topologyKey === this._prevTopologyKey) {
            // Only update edge widths
            const sel = d3.select(svg);
            sel.selectAll<SVGPathElement, D3Link>('path[fill="none"]')
                .attr('stroke-width', d => {
                    if (!d?.edgeKey) return 1;
                    const e = graph.edgeMap.get(d.edgeKey);
                    return e ? Math.min(1.5 + Math.log2(e.connectionCount + 1), 6) : 1;
                });
            return;
        }
        this._prevTopologyKey = topologyKey;
        this._simulation?.stop();
        this._simulation = null;

        const d3svg = d3.select(svg);
        d3svg.selectAll('*').remove();

        if (graph.nodes.length === 0) return;

        const bidirectionalPairs = new Set<string>();
        const edgeKeys = new Set(graph.edges.map(e => `${e.source}|${e.target}`));
        for (const e of graph.edges) {
            if (edgeKeys.has(`${e.target}|${e.source}`)) bidirectionalPairs.add([e.source, e.target].sort().join('|'));
        }

        const defs = d3svg.append('defs');
        for (const color of [EDGE_ALLOW, EDGE_DROP, EDGE_DEFAULT]) {
            defs.append('marker')
                .attr('id', `arrowhead-${color.replace('#', '')}`)
                .attr('viewBox', '-10 -5 10 10').attr('refX', 0).attr('refY', 0)
                .attr('markerWidth', 7).attr('markerHeight', 7).attr('orient', 'auto')
                .append('path').attr('d', 'M-10,-4L0,0L-10,4').attr('fill', color);
        }

        const d3Nodes: D3Node[] = graph.nodes.map(n => ({ ...n, x: undefined, y: undefined }));
        const nodeById = new Map(d3Nodes.map(n => [n.id, n]));

        const d3Links: D3Link[] = graph.edges.map(e => {
            const pairKey = [e.source, e.target].sort().join('|');
            const isBi = bidirectionalPairs.has(pairKey);
            return {
                source: nodeById.get(e.source)!,
                target: nodeById.get(e.target)!,
                edgeKey: `${e.source}|${e.target}`,
                connectionCount: e.connectionCount,
                color: edgeColor(e),
                label: edgeLabel(e),
                curveOffset: isBi ? (e.source < e.target ? CURVE_OFFSET : -CURVE_OFFSET) : 0,
            };
        });

        const container = d3svg.append('g');
        const zoom = d3.zoom<SVGSVGElement, unknown>().scaleExtent([0.3, 3])
            .on('zoom', event => container.attr('transform', event.transform));
        d3svg.call(zoom);

        // Namespace hulls
        const nsMap = new Map<string, D3Node[]>();
        for (const n of d3Nodes) {
            if (n.isExternal || !n.namespace) continue;
            let arr = nsMap.get(n.namespace);
            if (!arr) { arr = []; nsMap.set(n.namespace, arr); }
            arr.push(n);
        }
        const nsHulls = container.append('g').attr('class', 'ns-hulls');
        const nsGroups: { nodes: D3Node[]; path: d3.Selection<SVGPathElement, unknown, null, undefined>; label: d3.Selection<SVGTextElement, unknown, null, undefined>; }[] = [];
        for (const [ns, nodes] of nsMap) {
            if (nodes.length < 1) continue;
            const path = nsHulls.append('path').attr('fill', 'rgba(106,159,181,0.08)').attr('stroke', 'rgba(106,159,181,0.25)').attr('stroke-width', 1).attr('stroke-dasharray', '4,2');
            const label = nsHulls.append('text').text(ns).attr('fill', 'rgba(106,159,181,0.5)').attr('font-size', '10px').attr('font-weight', '600');
            nsGroups.push({ nodes, path, label });
        }

        // Edge paths
        const linkPaths = container.append('g').selectAll<SVGPathElement, D3Link>('path')
            .data(d3Links).join('path')
            .attr('fill', 'none')
            .attr('stroke', d => d.color)
            .attr('stroke-width', d => Math.min(1.5 + Math.log2(d.connectionCount + 1), 6))
            .attr('stroke-opacity', 0.6)
            .attr('marker-end', d => `url(#arrowhead-${d.color.replace('#', '')})`)
            .style('cursor', 'pointer')
            .on('mouseenter', (event, d) => {
                d3.select(event.currentTarget as Element)
                    .attr('stroke-opacity', 1)
                    .attr('stroke-width', Math.min(1.5 + Math.log2(d.connectionCount + 1), 6) + 1.5);
                const edge = this._graphRef.edgeMap.get(d.edgeKey);
                if (edge) this._showTooltip(event, edge);
            })
            .on('mousemove', event => {
                if (this._tooltipEl) {
                    this._tooltipEl.style.left = `${event.pageX + 12}px`;
                    this._tooltipEl.style.top = `${event.pageY + 12}px`;
                }
            })
            .on('mouseleave', (event, d) => {
                d3.select(event.currentTarget as Element)
                    .attr('stroke-opacity', 0.6)
                    .attr('stroke-width', Math.min(1.5 + Math.log2(d.connectionCount + 1), 6));
                this._hideTooltip();
            })
            .on('click', (_event, d) => {
                const edge = this._graphRef.edgeMap.get(d.edgeKey);
                if (edge) this._selectedEdgeKey = d.edgeKey;
            });

        // Edge labels
        const edgeLabelGroups = container.append('g').selectAll<SVGGElement, D3Link>('g')
            .data(d3Links.filter(d => d.label)).join('g');
        edgeLabelGroups.append('rect').attr('rx', 3).attr('ry', 3).attr('fill', 'rgba(23,36,43,0.85)').attr('stroke', d => d.color).attr('stroke-width', 0.5).attr('stroke-opacity', 0.5);
        edgeLabelGroups.append('text').text(d => d.label).attr('text-anchor', 'middle').attr('dominant-baseline', 'central').attr('fill', '#c5d1d8').attr('font-size', '9px').attr('pointer-events', 'none');

        // Nodes
        const nodeGroup = container.append('g').selectAll<SVGGElement, D3Node>('g')
            .data(d3Nodes).join('g')
            .style('cursor', 'grab')
            .call(d3.drag<SVGGElement, D3Node>()
                .on('start', (event, d) => { if (!event.active) simulation.alphaTarget(0.3).restart(); d.fx = d.x; d.fy = d.y; })
                .on('drag', (event, d) => { d.fx = event.x; d.fy = event.y; })
                .on('end', (event, d) => { if (!event.active) simulation.alphaTarget(0); d.fx = d.x; d.fy = d.y; })
            )
            .on('dblclick', (_event, d) => {
                d.fx = null; d.fy = null;
                simulation.alphaTarget(0.3).restart();
                setTimeout(() => simulation.alphaTarget(0), 1500);
            });

        nodeGroup.each(function (this: SVGGElement, d: D3Node) {
            const g = d3.select(this);
            if (d.isExternal) {
                const s = EXTERNAL_SIZE;
                g.append('polygon').attr('points', `0,${-s} ${s},0 0,${s} ${-s},0`).attr('fill', '#3d2c1e').attr('stroke', '#c4956a').attr('stroke-width', 2);
                g.append('text').text(d.shortName).attr('dy', s + 14).attr('text-anchor', 'middle').attr('fill', '#c4956a').attr('font-size', '11px').attr('font-weight', '600');
            } else {
                const { hw, hh } = nodeHalfSize(d);
                g.append('rect').attr('x', -hw).attr('y', -hh).attr('width', hw * 2).attr('height', hh * 2).attr('rx', NODE_RX).attr('ry', NODE_RX).attr('fill', '#1e3a4c').attr('stroke', '#6a9fb5').attr('stroke-width', 1.5);
                g.append('text').text(d.shortName).attr('dy', -3).attr('text-anchor', 'middle').attr('fill', '#e0e8ec').attr('font-size', '12px').attr('font-weight', '600');
                g.append('text').text(d.namespace).attr('dy', 13).attr('text-anchor', 'middle').attr('fill', 'rgba(106,159,181,0.7)').attr('font-size', '9px');
            }
        });

        const HULL_PADDING = 50;
        function updateHulls() {
            for (const { nodes, path, label } of nsGroups) {
                if (nodes.length === 1) {
                    const n = nodes[0];
                    const px = HULL_PADDING; const py = HULL_PADDING;
                    path.attr('d', `M${n.x! - px},${n.y! - py} L${n.x! + px},${n.y! - py} L${n.x! + px},${n.y! + py} L${n.x! - px},${n.y! + py} Z`);
                    label.attr('x', n.x! - px + 6).attr('y', n.y! - py + 12);
                    continue;
                }
                const points: [number, number][] = [];
                for (const n of nodes) {
                    const [x, y] = [n.x!, n.y!];
                    points.push([x - HULL_PADDING, y - HULL_PADDING], [x + HULL_PADDING, y - HULL_PADDING], [x + HULL_PADDING, y + HULL_PADDING], [x - HULL_PADDING, y + HULL_PADDING]);
                }
                const hull = d3.polygonHull(points);
                if (hull) {
                    path.attr('d', `M${hull.map(p => p.join(',')).join('L')}Z`);
                    label.attr('x', d3.min(hull, p => p[0])! + 6).attr('y', d3.min(hull, p => p[1])! + 12);
                }
            }
        }

        function clusterForce(alpha: number) {
            const centroids = new Map<string, { x: number; y: number; count: number }>();
            for (const n of d3Nodes) {
                if (!n.namespace || n.isExternal) continue;
                const c = centroids.get(n.namespace);
                if (c) { c.x += n.x!; c.y += n.y!; c.count++; }
                else centroids.set(n.namespace, { x: n.x!, y: n.y!, count: 1 });
            }
            const strength = 0.15 * alpha;
            for (const n of d3Nodes) {
                if (!n.namespace || n.isExternal) continue;
                const c = centroids.get(n.namespace)!;
                c.x /= c.count; c.y /= c.count;
                n.vx! += (c.x - n.x!) * strength;
                n.vy! += (c.y - n.y!) * strength;
            }
        }

        const simulation = d3.forceSimulation<D3Node>(d3Nodes)
            .force('link', d3.forceLink<D3Node, D3Link>(d3Links).id(d => d.id).distance(120))
            .force('charge', d3.forceManyBody<D3Node>().strength(-400))
            .force('center', d3.forceCenter<D3Node>(this._svgWidth / 2, HEIGHT / 2))
            .force('collision', d3.forceCollide<D3Node>().radius(d => nodeCollideRadius(d) + 20))
            .force('cluster', clusterForce as unknown as d3.Force<D3Node, D3Link>)
            .on('tick', () => {
                linkPaths.attr('d', d => {
                    const s = d.source as D3Node; const t = d.target as D3Node;
                    const [sx, sy] = nodeBoundary(s, t.x!, t.y!, 1);
                    const [tx, ty] = nodeBoundary(t, s.x!, s.y!, 3);
                    return curvedPath(sx, sy, tx, ty, d.curveOffset);
                });
                edgeLabelGroups.attr('transform', d => {
                    const s = d.source as D3Node; const t = d.target as D3Node;
                    const [mx, my] = quadMidpoint(s.x!, s.y!, t.x!, t.y!, d.curveOffset);
                    return `translate(${mx},${my})`;
                });
                edgeLabelGroups.each(function (this: SVGGElement) {
                    const g = d3.select(this);
                    const bbox = (g.select('text').node() as SVGTextElement)?.getBBox();
                    if (bbox) {
                        g.select('rect').attr('x', -bbox.width / 2 - 3).attr('y', -bbox.height / 2 - 1).attr('width', bbox.width + 6).attr('height', bbox.height + 2);
                    }
                });
                nodeGroup.attr('transform', d => `translate(${d.x},${d.y})`);
                updateHulls();
            });

        this._simulation = simulation;
    }

    private _showTooltip(event: MouseEvent, edge: WorkloadEdge) {
        const tip = this._tooltipEl;
        if (!tip) return;
        const policyLines: string[] = [];
        const ingressAllow = !edge.ingressActions.has(NetworkPolicyRuleAction.Drop) && !edge.ingressActions.has(NetworkPolicyRuleAction.Reject);
        const egressAllow = !edge.egressActions.has(NetworkPolicyRuleAction.Drop) && !edge.egressActions.has(NetworkPolicyRuleAction.Reject);
        for (const p of edge.ingressPolicies) policyLines.push(`<span style="color:${ingressAllow ? EDGE_ALLOW : EDGE_DROP}">${ingressAllow ? '✓' : '✗'}</span> Ingress: ${p}`);
        for (const p of edge.egressPolicies) policyLines.push(`<span style="color:${egressAllow ? EDGE_ALLOW : EDGE_DROP}">${egressAllow ? '✓' : '✗'}</span> Egress: ${p}`);
        const portParts: string[] = [];
        edge.protoPorts.forEach((pSet, proto) => {
            const name = getProtocolName(proto);
            portParts.push(pSet.size > 0 ? `${name}(${Array.from(pSet).sort((a, b) => a - b).join(', ')})` : name);
        });
        tip.innerHTML = `
            <div style="font-weight:600;margin-bottom:4px">${workloadShortName(edge.source)} &rarr; ${workloadShortName(edge.target)}</div>
            <div>${portParts.join(', ') || '-'}</div>
            <div>${edge.connectionCount} connection${edge.connectionCount !== 1 ? 's' : ''}</div>
            <div>&#8593; ${formatBytes(edge.totalBytesForward)} &nbsp; &#8595; ${formatBytes(edge.totalBytesReverse)}</div>
            ${edge.bitRate > 0 ? `<div>Throughput: ${formatBitRate(edge.bitRate)}</div>` : ''}
            ${policyLines.length > 0 ? '<hr style="border-color:#555;margin:4px 0"/>' + policyLines.join('<br/>') : ''}
        `;
        tip.style.left = `${event.pageX + 12}px`;
        tip.style.top = `${event.pageY + 12}px`;
        tip.style.opacity = '1';
    }

    private _hideTooltip() {
        if (this._tooltipEl) this._tooltipEl.style.opacity = '0';
    }

    // ── Render helpers ────────────────────────────────────────────────────────

    private _renderMultiSelect(
        label: string,
        options: string[],
        selected: string[],
        open: boolean,
        onToggleOpen: () => void,
        onToggle: (v: string) => void,
    ) {
        const displayText = selected.length === 0 ? 'All' : selected.length <= 2 ? selected.join(', ') : `${selected.slice(0, 2).join(', ')} +${selected.length - 2}`;
        return html`
            <div class="multiselect" @pointerdown=${(e: Event) => e.stopPropagation()}>
                <span class="multiselect-label">${label}</span>
                <button type="button" class="multiselect-btn" @click=${onToggleOpen}>
                    ${displayText}
                    <span class="multiselect-chevron">&#9662;</span>
                </button>
                ${open && options.length > 0 ? html`
                    <div class="multiselect-dropdown">
                        ${options.map(opt => html`
                            <label class="multiselect-option ${selected.includes(opt) ? 'selected' : ''}">
                                <input type="checkbox" .checked=${selected.includes(opt)} @change=${() => onToggle(opt)} style="accent-color: var(--antrea-color-primary, #0079b8)" />
                                ${opt}
                            </label>
                        `)}
                    </div>
                ` : nothing}
            </div>
        `;
    }

    private _toggleSelection(current: string[], value: string): string[] {
        return current.includes(value) ? current.filter(v => v !== value) : [...current, value];
    }

    private _renderFilters() {
        const statusColor = this._connected ? 'var(--antrea-color-success, #60b515)' : 'var(--antrea-color-danger, #f54f47)';
        const statusText = this._connected ? 'Connected' : (this._paused ? 'Paused' : 'Disconnected');
        return html`
            <div class="filter-bar">
                <div class="filter-row">
                    ${this._renderMultiSelect('Namespaces', this._availableNs, this._pendingNs, this._nsOpen, () => { this._nsOpen = !this._nsOpen; this._podOpen = false; this._svcOpen = false; }, v => { this._pendingNs = this._toggleSelection(this._pendingNs, v); })}
                    ${this._renderMultiSelect('Pod Names', this._availablePods, this._pendingPods, this._podOpen, () => { this._podOpen = !this._podOpen; this._nsOpen = false; this._svcOpen = false; }, v => { this._pendingPods = this._toggleSelection(this._pendingPods, v); })}
                    ${this._renderMultiSelect('Service Names', this._availableServices, this._pendingServices, this._svcOpen, () => { this._svcOpen = !this._svcOpen; this._nsOpen = false; this._podOpen = false; }, v => { this._pendingServices = this._toggleSelection(this._pendingServices, v); })}

                    <div class="field-group" style="min-width:140px">
                        <label class="field-label">Flow Type</label>
                        <select class="field-select" .value=${this._pendingFlowType} @change=${(e: Event) => { this._pendingFlowType = (e.target as HTMLSelectElement).value as FlowTypeName | ''; }}>
                            <option value="">All</option>
                            <option value="intra-node">${flowTypeLabel[FlowType.IntraNode]}</option>
                            <option value="inter-node">${flowTypeLabel[FlowType.InterNode]}</option>
                            <option value="to-external">${flowTypeLabel[FlowType.ToExternal]}</option>
                            <option value="from-external">${flowTypeLabel[FlowType.FromExternal]}</option>
                        </select>
                    </div>
                    <div class="field-group" style="min-width:120px">
                        <label class="field-label">Direction</label>
                        <select class="field-select" .value=${this._pendingDirection} @change=${(e: Event) => { this._pendingDirection = (e.target as HTMLSelectElement).value as FlowFilterDirection; }}>
                            <option value="both">Both</option>
                            <option value="from">From</option>
                            <option value="to">To</option>
                        </select>
                    </div>
                    <div class="field-group" style="min-width:160px">
                        <label class="field-label">IPs (comma-separated)</label>
                        <input class="field-input" type="text" .value=${this._pendingIps} placeholder="10.0.0.1, 10.0.0.0/24" @input=${(e: Event) => { this._pendingIps = (e.target as HTMLInputElement).value; }} />
                    </div>
                    <div class="field-group" style="min-width:180px">
                        <label class="field-label">Pod Label Selector</label>
                        <input class="field-input" type="text" .value=${this._pendingPodLabel} placeholder="app=frontend,version!=v2" @input=${(e: Event) => { this._pendingPodLabel = (e.target as HTMLInputElement).value; }} />
                    </div>

                    <div class="filter-actions">
                        <antrea-button type="button" @click=${this._onApplyFilters}>Apply Filters</antrea-button>
                        <antrea-button type="button" action="outline" @click=${this._onResetFilters}>Reset</antrea-button>
                        <antrea-button type="button" action="outline" @click=${this._onPauseToggle}>${this._paused ? 'Resume' : 'Pause'}</antrea-button>
                        <antrea-button type="button" action="outline" @click=${this._onClear}>Clear</antrea-button>
                    </div>
                </div>
                <div class="status-row">
                    <span style="display:inline-flex;align-items:center;gap:6px">
                        <span class="status-dot" style="background-color:${statusColor}"></span>
                        ${statusText}
                    </span>
                    <span>${this._entries.length} connections</span>
                    ${this._droppedCount > 0 ? html`<span class="warn">${this._droppedCount} flows dropped (buffer overflow)</span>` : nothing}
                    ${this._evictionWarning ? html`<span class="warn">Store limit reached, oldest entries evicted</span>` : nothing}
                </div>
            </div>
        `;
    }

    private _renderFlowList() {
        let filtered = this._entries;
        if (this._textFilter) filtered = filtered.filter(e => matchesText(e, this._textFilter));
        const sorted = [...filtered].sort((a, b) => {
            const aVal = sortValue(a, this._sortField);
            const bVal = sortValue(b, this._sortField);
            const cmp = typeof aVal === 'number' && typeof bVal === 'number' ? aVal - bVal : String(aVal).localeCompare(String(bVal));
            return this._sortDir === 'asc' ? cmp : -cmp;
        });
        const onSort = (field: SortField) => {
            if (this._sortField === field) this._sortDir = this._sortDir === 'asc' ? 'desc' : 'asc';
            else { this._sortField = field; this._sortDir = 'asc'; }
        };
        return html`
            <div style="display:flex;flex-direction:column;gap:1rem;max-width:100%">
                <div class="flow-list-header">
                    <input class="flow-filter-input" type="text" placeholder="Filter flows…" .value=${this._textFilter}
                        @input=${(e: Event) => { this._textFilter = (e.target as HTMLInputElement).value; }} />
                    <span class="text-muted">${sorted.length} connections</span>
                </div>
                <div class="flow-list-scroll">
                    <table class="data-table" style="min-width:1200px">
                        <thead><tr>${COLUMNS.map(col => html`
                            <th @click=${() => onSort(col.field)}>
                                ${col.label}${this._sortField === col.field ? (this._sortDir === 'asc' ? ' ▲' : ' ▼') : ''}
                            </th>`)}</tr></thead>
                        <tbody>${sorted.map(entry => {
                            const { flow } = entry;
                            return html`<tr>
                                <td>${new Date(flow.endTs).toLocaleTimeString()}</td>
                                <td>${formatEndpoint(flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.ip.source)}</td>
                                <td>${formatEndpoint(flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.ip.destination)}</td>
                                <td title=${flow.k8s.destinationServicePortName || nothing}>${destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName) || flow.k8s.destinationServicePortName || '-'}</td>
                                <td>${getProtocolName(flow.transport.protocolNumber)}</td>
                                <td>${flow.transport.destinationPort}</td>
                                <td>${formatBytes(flow.stats.octetTotalCount)}</td>
                                <td>${formatBytes(flow.reverseStats.octetTotalCount)}</td>
                                <td>${formatPolicyInfo(flow.k8s.ingressNetworkPolicyName, flow.k8s.ingressNetworkPolicyRuleAction) || '-'}</td>
                                <td>${formatPolicyInfo(flow.k8s.egressNetworkPolicyName, flow.k8s.egressNetworkPolicyRuleAction) || '-'}</td>
                                <td>${flowTypeLabel[flow.k8s.flowType as FlowType] ?? 'Unknown'}</td>
                            </tr>`;
                        })}</tbody>
                    </table>
                </div>
            </div>
        `;
    }

    private _renderEdgeDetails() {
        if (!this._selectedEdgeKey) return nothing;
        const edge = this._graphRef.edgeMap.get(this._selectedEdgeKey);
        if (!edge) return nothing;
        const d = edgeToDetails(edge);
        return html`
            <div class="edge-details">
                <button class="edge-details-close" @click=${() => { this._selectedEdgeKey = null; }}>✕</button>
                <div class="edge-details-section-label">Connection Stats</div>
                <div class="edge-details-rows">
                    <div><strong>Source:</strong> ${workloadShortName(d.source)}</div>
                    <div><strong>Target:</strong> ${workloadShortName(d.target)}</div>
                    <div><strong>Connections:</strong> ${d.connectionCount}</div>
                    <div><strong>Bytes (Fwd):</strong> ${formatBytes(d.totalBytesForward)}</div>
                    <div><strong>Bytes (Rev):</strong> ${formatBytes(d.totalBytesReverse)}</div>
                    ${d.bitRate > 0 ? html`<div><strong>Bit Rate:</strong> ${formatBitRate(d.bitRate)}</div>` : nothing}
                    <div><strong>Dest Ports:</strong> ${d.destPortsStr || '-'}</div>
                    ${d.ingressPolicies.length ? html`<div><strong>Ingress Policies:</strong> ${d.ingressPolicies.join(', ')}</div>` : nothing}
                    ${d.egressPolicies.length ? html`<div><strong>Egress Policies:</strong> ${d.egressPolicies.join(', ')}</div>` : nothing}
                    <div><strong>Flow Types:</strong> ${d.flowTypes.join(', ') || '-'}</div>
                </div>
            </div>
        `;
    }

    private _renderServiceMap() {
        return html`
            <div class="map-container">
                <svg id="graph-svg" class="map-svg" width=${this._svgWidth} height=${HEIGHT}></svg>
                <div id="graph-tooltip" class="map-tooltip"></div>
                <div class="map-legend">
                    <div class="map-legend-title">Legend</div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke=${EDGE_ALLOW} stroke-width="2"/></svg><span>Allow</span></div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke=${EDGE_DROP} stroke-width="2"/></svg><span>Drop / Reject</span></div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" stroke=${EDGE_DEFAULT} stroke-width="2"/></svg><span>No policy</span></div>
                    <div style="margin-top:2px;color:#8899a4">Line thickness = connection count</div>
                </div>
                ${this._renderEdgeDetails()}
            </div>
        `;
    }

    override render() {
        return html`
            <main>
                <div class="page-layout" style="max-width:100%">
                    <div class="row">
                        <p class="page-title">Flow Visibility</p>
                        <div class="btn-group">
                            <antrea-button type="button" action=${this._viewMode === 'list' ? 'solid' : 'outline'} @click=${() => { this._viewMode = 'list'; }}>Flow List</antrea-button>
                            <antrea-button type="button" action=${this._viewMode === 'map' ? 'solid' : 'outline'} @click=${() => { this._viewMode = 'map'; this._setupResizeObserver(); }}>Service Map</antrea-button>
                        </div>
                    </div>

                    ${this._renderFilters()}

                    ${this._error ? html`<antrea-alert status="danger">${this._error}</antrea-alert>` : nothing}

                    ${this._viewMode === 'list' ? this._renderFlowList() : this._renderServiceMap()}
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-flow-visibility-page', AntreaFlowVisibilityPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-flow-visibility-page': AntreaFlowVisibilityPage; }
}
