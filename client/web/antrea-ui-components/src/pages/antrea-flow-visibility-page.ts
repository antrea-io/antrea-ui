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

import { html, css, nothing } from 'lit';
import type { TemplateResult } from 'lit';
import { state, query, property } from 'lit/decorators.js';
import * as d3 from 'd3';
import { pageStyles } from '../lib/styles.js';
import { SessionAwarePage } from '../lib/session-aware-page.js';
import { FlowStore, FlowEntry, entryBitRate } from '../lib/flow-store.js';
import {
    FlowType,
    NetworkPolicyRuleAction,
    flowTypeLabel,
    getProtocolName,
    formatEndpoint,
    formatPolicyInfo,
    formatBytes,
    destinationK8sServiceFilterKey,
    endpointView,
    destinationServiceView,
    endpointRedacted,
} from '../lib/flow-types.js';
import type { Flow } from '../lib/flow-types.js';
import type { EndpointView } from '../lib/flow-types.js';
import {
    FlowStreamClient,
    FlowStreamFilter,
    FlowPeerFilter,
    FlowStreamScope,
    FlowFilterDirection,
    FlowTypeName,
    streamFilterKey,
} from '../lib/flow-stream.js';
import { flowNamespaces, observableNamespaces } from '../lib/flow-namespaces-api.js';
import type { FlowNamespacesResponse } from '../lib/flow-namespaces-api.js';
import '../antrea-button';
import '../antrea-alert';

// ── Graph types & helpers ────────────────────────────────────────────────────

/**
 * Detail payload for the `antrea-edge-selected` event, dispatched whenever the
 * selected edge in the service map changes (or is cleared, as `null`). Also
 * passed to each registered `edgeExtraRenderers` function so plugins can
 * render content into the edge details card keyed off the selection. See
 * `@antrea/ui-plugin-sdk`'s `registerEdgeExtraRenderer`.
 */
export interface EdgeSelection {
    source: string;
    target: string;
    destPorts: string;
    ingressPolicyNames: string[];
    egressPolicyNames: string[];
    protected: boolean;
}

/** A function a plugin registers (via `@antrea/ui-plugin-sdk`'s `registerEdgeExtraRenderer`)
 * to render extra content into the edge details card. Returns `null` to render nothing for
 * this selection. */
export type EdgeExtraRenderer = (selection: EdgeSelection) => Node | null;

/** A single column of the flow list table. `render` may return a Lit template as well as a
 * string — the built-in endpoint columns do, to mark a withheld cell — so a plugin returning a
 * plain string keeps working unchanged. */
export interface FlowTableColumn {
    key: string;
    label: string;
    render(entry: FlowEntry): string | TemplateResult;
}

/** A function a plugin registers (via `@antrea/ui-plugin-sdk`'s
 * `registerFlowTableColumnsProcessor`) to insert, remove, update, or reorder flow list table
 * columns — modeled on Headlamp's `registerResourceTableColumnsProcessor`. */
export type FlowTableColumnsProcessor = (columns: FlowTableColumn[]) => FlowTableColumn[];

const FLOW_VISIBILITY_FORBIDDEN_MESSAGE =
    'You are not authorized to observe flows in this scope. The Flow Aggregator authorizes each ' +
    'flow stream with Kubernetes RBAC, against the "flows" resource in API group ' +
    'observability.antrea.io (see antrea-ui/docs/authentication.md).';

// The observed-namespace selector's value for cluster scope. Not a namespace name, so it cannot
// collide with one: a namespace may not contain an underscore.
const SCOPE_CLUSTER_WIDE = '__cluster_wide__';

// Query parameters the scope travels in, so a refresh or a shared link keeps it. They are named
// after the backend's own stream parameters deliberately — the same two names, with the same
// meaning, on the page URL and on the SSE request.
const OBSERVED_NS_PARAM = 'observedNamespace';
const CLUSTER_WIDE_PARAM = 'clusterWide';

// Shown when no scope is selected. The page opens no stream at all in that state: the Flow
// Aggregator requires every stream to name exactly one scope, and defaulting to cluster-wide
// would ask for a grant most users do not hold.
const SELECT_SCOPE_MESSAGE =
    'Select a namespace to observe. Flows are authorized per namespace, so a stream has to name ' +
    'the one namespace — or the whole cluster — it observes.';

// Shown once the namespace list has resolved and turned out to be empty: a real answer, not a
// failure, so it is an explanation rather than an error toast.
const NO_OBSERVABLE_NAMESPACES_MESSAGE =
    'You are not authorized to observe flows in any namespace. Flow visibility needs "watch" on ' +
    'the "flows" resource in API group observability.antrea.io, in the namespace you want to ' +
    'observe (see antrea-ui/docs/authentication.md).';

// Shown when the backend could not enumerate every candidate namespace, in which case a
// namespace's absence from the list is not proof the user cannot observe flows in it.
//
// Says why, and says what to do about it. Kubernetes cannot be asked which namespaces a subject
// may access, so the list is built by finding role bindings that name the user - which misses a
// grant held through a group, and any namespace the user cannot list. The escape hatch is real:
// _restoreScopeFromURL honours a namespace from the URL whether or not it is listed, because the
// Flow Aggregator authorizes the stream either way.
const INCOMPLETE_NAMESPACES_NOTE =
    'This list may be missing namespaces you can observe: Kubernetes cannot report which ' +
    'namespaces a user may access, so the list is built from the role bindings that name you. ' +
    'Use the "Namespace not listed" box to name one directly.';

const FLOW_NAMESPACES_ERROR_MESSAGE =
    'Could not load the namespaces you may observe flows in. Reload the page to try again.';

// A pseudo-option in the peer-namespace menu, filtered client-side. It contains a space, so it
// cannot collide with a namespace name. A server-side filter could not express it: upstream runs
// its authorization before applying filters, so a filter only ever matches fields that survived
// redaction, and the peers this selects are exactly the ones whose names did not. It is the only
// way to ask how much of a namespace's traffic goes somewhere the user cannot see.
const UNIDENTIFIED_PEERS_OPTION = 'Unidentified peers';

// Said on the two controls that match on an identity the Flow tier clears. Not a limitation to
// engineer around: filters run on the redacted record, which is also what stops them being an
// existence oracle - guessing a hidden workload's name returns the same empty result as guessing
// wrong.
const IDENTIFIED_ONLY_HINT = 'Matches identified endpoints only.';

// Said in the peer-namespace menu, which is built from the flows received so far rather than
// from a cluster-wide list, so it fills in as they arrive.
const PEER_NAMESPACE_HINT = 'Peer namespaces seen in current flows.';

const FLOW_VISIBILITY_DISABLED_MESSAGE =
    'Flow visibility is disabled on this Antrea UI server. Install or upgrade the chart with ' +
    '--set flowAggregator.enabled=true and a reachable flowAggregator.address (see antrea-ui/hack/deploy-kind.sh).';

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

/**
 * What a node stands for. A boolean `isExternal` could not express the two collapsed forms, and
 * everything that branches on this has to handle all four: `nodeHalfSize`, `nodeBoundary`,
 * `nodeCollideRadius`, the node-rendering branch and the three hull-membership filters. Missing
 * one is a layout bug, not a visual nit.
 *
 * - `workload`          — a workload in this cluster, identified.
 * - `external`          — an aggregate of peers outside the cluster. Nothing about it was
 *                         withheld: there was never a Kubernetes identity to withhold.
 * - `undisclosedNamespace` — a namespace whose name survived redaction but whose contents did
 *                         not. Rendered as an empty namespace hull, because in this map's
 *                         grammar a dashed outline is a namespace and a rounded rect is a
 *                         workload, so a box labelled `ns-c` would read as a workload called
 *                         `ns-c`.
 * - `undisclosed`       — a peer inside the cluster whose namespace was cleared too, which
 *                         happens only on a denied connection. One per identified counterpart
 *                         workload, never one shared node: a shared node would assert that every
 *                         denied flow reaching it went to the same place, which is false and
 *                         unknowable, and a force layout would pull the one thing nobody can see
 *                         into the centre of the graph.
 */
type NodeKind = 'workload' | 'external' | 'undisclosedNamespace' | 'undisclosed';

interface WorkloadNode {
    id: string;
    shortName: string;
    namespace: string;
    kind: NodeKind;
    /** The second line of the label. The namespace for a workload, and for a collapsed form the
     * distinct peer addresses behind it - a count that leaks nothing, since those addresses are
     * already in the records the client holds (redactFlow never touches Flow.IP). */
    detail: string;
    /** Distinct peer addresses collapsed into this node, which `detail` is derived from once the
     * graph is complete. Deliberately not a workload or Pod count: it is a lower bound, it
     * over-counts a Pod that restarted with a new address, and for a host-network Pod it is a
     * Node address. */
    addresses?: Set<string>;
}
interface WorkloadEdge {
    source: string; target: string;
    connectionCount: number; totalBytesForward: number; totalBytesReverse: number; bitRate: number;
    protoPorts: Map<number, Set<number>>;
    ingressPolicies: Set<string>; egressPolicies: Set<string>;
    // Raw policy names (unlike ingressPolicies/egressPolicies, which are "name (Action)" display
    // strings) — needed to link to a policy by exact name, e.g. from a downstream host page.
    ingressPolicyNames: Set<string>; egressPolicyNames: Set<string>;
    ingressActions: Set<NetworkPolicyRuleAction>; egressActions: Set<NetworkPolicyRuleAction>;
    flowTypes: Set<FlowType>;
    /** The distinct flows behind this edge, which connectionCount is derived from. */
    flowIds: Set<string>;
}
interface EdgeDetails {
    source: string; target: string;
    connectionCount: number; totalBytesForward: number; totalBytesReverse: number; bitRate: number;
    destPortsStr: string;
    ingressPolicies: string[]; egressPolicies: string[]; flowTypes: string[];
}
interface GraphData { nodes: WorkloadNode[]; edges: WorkloadEdge[]; edgeMap: Map<string, WorkloadEdge>; nodeMap: Map<string, WorkloadNode>; }

/** One end of a flow, before it is turned into a node: a collapsed end's node id can depend on
 * the other end's, so the two have to be described before either is resolved. */
type EndpointDesc =
    | { kind: 'external' }
    | { kind: 'workload'; id: string; namespace: string; shortName: string }
    | { kind: 'undisclosedNamespace'; namespace: string; address: string }
    | { kind: 'undisclosed'; address: string };

function describeEndpoint(flow: Flow, side: 'source' | 'destination'): EndpointDesc {
    const k8s = flow.k8s;
    const isSource = side === 'source';
    const flowType = k8s.flowType as FlowType;
    if (isSource ? flowType === FlowType.FromExternal : flowType === FlowType.ToExternal) {
        return { kind: 'external' };
    }
    const disclosure = isSource ? k8s.sourceDisclosure : k8s.destinationDisclosure;
    const namespace = isSource ? k8s.sourcePodNamespace : k8s.destinationPodNamespace;
    const address = isSource ? flow.ip.source : flow.ip.destination;
    if (endpointRedacted(disclosure)) {
        // The namespace survives only for an allowed connection: redactFlow clears it as well
        // when the connection was denied, which is what splits the two collapsed forms. One real
        // namespace can therefore appear twice in a graph - under its name for its allowed
        // flows, among the anonymous nodes for its denied ones - and must not be merged back by
        // correlating addresses, which would reconstruct the IP-to-namespace map redaction
        // exists to prevent.
        return namespace
            ? { kind: 'undisclosedNamespace', namespace, address }
            : { kind: 'undisclosed', address };
    }
    const podName = isSource ? k8s.sourcePodName : k8s.destinationPodName;
    const labels = isSource ? k8s.sourcePodLabels : k8s.destinationPodLabels;
    const svcKey = !isSource && k8s.destinationServicePortName
        ? destinationK8sServiceFilterKey(k8s.destinationServicePortName) : '';
    const workloadId = svcKey || getWorkloadName(namespace, podName, labels);
    const shortName = workloadShortName(workloadId);
    if (shortName) return { kind: 'workload', id: workloadId, namespace, shortName };
    // No workload name to show: getWorkloadName returns "ns/" for an endpoint with no Pod name
    // and no labels, which used to give a node an empty label sized by textWidth("") - a
    // malformed sliver. The address is always there to fall back on, at every tier.
    return {
        kind: 'workload',
        id: namespace ? `${namespace}/${address}` : address,
        namespace,
        shortName: address || 'unknown',
    };
}

/** The node id of an end that is identified, or null for one that is collapsed and so has to be
 * keyed on its counterpart. */
function identifiedNodeId(desc: EndpointDesc): string | null {
    if (desc.kind === 'external') return 'external';
    if (desc.kind === 'workload') return desc.id;
    return null;
}

// ':' cannot appear in a workload node id, which is always "namespace/name", so neither prefix
// can collide with a real workload.
function collapsedNodeId(desc: EndpointDesc, counterpartId: string | null): string {
    if (desc.kind === 'undisclosedNamespace') return `undisclosed-namespace:${desc.namespace}`;
    // Keyed on the identified counterpart, which the caller holds at Full or Identity, so the
    // key itself discloses nothing about the far end. A record only ever reaches the client
    // because one of its ends is in scope, so the fallback is for a record that should not
    // exist rather than a case with a sensible grouping.
    return `undisclosed-peer:${counterpartId ?? (desc.kind === 'undisclosed' ? desc.address : '')}`;
}

function nodeFor(desc: EndpointDesc, id: string): WorkloadNode {
    switch (desc.kind) {
        case 'external':
            return { id, shortName: 'External', namespace: '', kind: 'external', detail: '' };
        case 'workload':
            return { id, shortName: desc.shortName, namespace: desc.namespace, kind: 'workload', detail: desc.namespace };
        case 'undisclosedNamespace':
            return { id, shortName: desc.namespace, namespace: desc.namespace, kind: 'undisclosedNamespace', detail: '', addresses: new Set() };
        case 'undisclosed':
            return { id, shortName: 'undisclosed', namespace: '', kind: 'undisclosed', detail: '', addresses: new Set() };
    }
}

/** The second line of a collapsed node's label: the number of distinct peer addresses behind it.
 * "addresses" deliberately, not "endpoints" - Endpoints is a Kubernetes resource and upstream
 * uses "endpoint" for one end of a flow, so either reading of that word would be wrong here. */
function addressesLabel(addresses: Set<string>): string {
    if (addresses.size === 1) return Array.from(addresses)[0];
    return `${addresses.size} addresses`;
}

function buildGraph(entries: FlowEntry[]): GraphData {
    const nodeMap = new Map<string, WorkloadNode>();
    const edgeMap = new Map<string, WorkloadEdge>();
    for (const entry of entries) {
        const { flow } = entry;
        const flowType = flow.k8s.flowType as FlowType;
        const srcDesc = describeEndpoint(flow, 'source');
        const dstDesc = describeEndpoint(flow, 'destination');
        // Only the unidentified side collapses. A flow from an in-scope workload to a peer the
        // caller may not identify keeps its source at workload granularity: collapsing both ends
        // would destroy the one thing the caller is entitled to see, which of their own
        // workloads is talking outward.
        const srcIdentified = identifiedNodeId(srcDesc);
        const dstIdentified = identifiedNodeId(dstDesc);
        const srcId = srcIdentified ?? collapsedNodeId(srcDesc, dstIdentified);
        const dstId = dstIdentified ?? collapsedNodeId(dstDesc, srcIdentified);
        for (const [id, desc] of [[srcId, srcDesc], [dstId, dstDesc]] as [string, EndpointDesc][]) {
            let node = nodeMap.get(id);
            if (!node) { node = nodeFor(desc, id); nodeMap.set(id, node); }
            if (node.addresses) {
                const address = desc.kind === 'undisclosedNamespace' || desc.kind === 'undisclosed' ? desc.address : '';
                if (address) node.addresses.add(address);
            }
        }
        if (srcId === dstId) continue;
        const edgeKey = `${srcId}|${dstId}`;
        let edge = edgeMap.get(edgeKey);
        if (!edge) {
            edge = { source: srcId, target: dstId, flowIds: new Set(), connectionCount: 0, totalBytesForward: 0, totalBytesReverse: 0, bitRate: 0, protoPorts: new Map(), ingressPolicies: new Set(), egressPolicies: new Set(), ingressPolicyNames: new Set(), egressPolicyNames: new Set(), ingressActions: new Set(), egressActions: new Set(), flowTypes: new Set() };
            edgeMap.set(edgeKey, edge);
        }
        // Distinct flow IDs, not records: several records can describe the same flow, and a
        // collapsed node aggregates whatever number of peers happens to sit behind it.
        edge.flowIds.add(flow.id);
        edge.totalBytesForward += flow.stats.octetTotalCount;
        edge.totalBytesReverse += flow.reverseStats.octetTotalCount;
        edge.bitRate += entryBitRate(entry);
        let protoSet = edge.protoPorts.get(flow.transport.protocolNumber);
        if (!protoSet) { protoSet = new Set(); edge.protoPorts.set(flow.transport.protocolNumber, protoSet); }
        if (flow.transport.destinationPort) protoSet.add(flow.transport.destinationPort);
        // Keyed on what formatPolicyInfo makes of the pair rather than on the name alone: at the
        // Flow tier the name is cleared but the action survives, and dropping the edge's policy
        // information because the name went missing would lose the one field redaction
        // deliberately preserved - including the colour edgeRole gives the edge. The raw-name
        // sets stay keyed on the name, since there is no name to link to.
        // Each policy's identity follows its own side's tier: the ingress policy is evaluated on
        // the destination, the egress policy on the source.
        const ingressInfo = formatPolicyInfo(flow.k8s.ingressNetworkPolicyName, flow.k8s.ingressNetworkPolicyRuleAction, flow.k8s.destinationDisclosure);
        if (ingressInfo) {
            edge.ingressPolicies.add(ingressInfo);
            if (flow.k8s.ingressNetworkPolicyName) edge.ingressPolicyNames.add(flow.k8s.ingressNetworkPolicyName);
            edge.ingressActions.add(flow.k8s.ingressNetworkPolicyRuleAction);
        }
        const egressInfo = formatPolicyInfo(flow.k8s.egressNetworkPolicyName, flow.k8s.egressNetworkPolicyRuleAction, flow.k8s.sourceDisclosure);
        if (egressInfo) {
            edge.egressPolicies.add(egressInfo);
            if (flow.k8s.egressNetworkPolicyName) edge.egressPolicyNames.add(flow.k8s.egressNetworkPolicyName);
            edge.egressActions.add(flow.k8s.egressNetworkPolicyRuleAction);
        }
        edge.flowTypes.add(flowType);
    }
    for (const edge of edgeMap.values()) edge.connectionCount = edge.flowIds.size;
    for (const node of nodeMap.values()) {
        if (node.addresses) node.detail = addressesLabel(node.addresses);
    }
    return { nodes: Array.from(nodeMap.values()), edges: Array.from(edgeMap.values()), edgeMap, nodeMap };
}

function formatBitRate(bps: number): string {
    if (bps === 0) return '0 bps';
    const units = ['bps', 'Kbps', 'Mbps', 'Gbps'];
    const i = Math.min(Math.floor(Math.log(bps) / Math.log(1000)), units.length - 1);
    return `${(bps / Math.pow(1000, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

/** How an edge's endpoint reads in the details card and the edge tooltip. Node ids are internal
 * (a collapsed node's is prefixed to keep it from colliding with a workload's), so the node's own
 * label is what to show; the ids themselves stay on EdgeSelection, which is a plugin contract. */
function nodeLabel(graph: GraphData, id: string): string {
    const node = graph.nodeMap.get(id);
    if (!node) return workloadShortName(id);
    return node.detail && node.kind !== 'workload' ? `${node.shortName} (${node.detail})` : node.shortName;
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

function edgeToSelection(edge: WorkloadEdge): EdgeSelection {
    return {
        source: edge.source,
        target: edge.target,
        destPorts: edgeToDetails(edge).destPortsStr,
        ingressPolicyNames: Array.from(edge.ingressPolicyNames),
        egressPolicyNames: Array.from(edge.egressPolicyNames),
        protected: edge.ingressPolicies.size > 0 || edge.egressPolicies.size > 0,
    };
}

type EdgeRole = 'allow' | 'drop' | 'default';
const EDGE_ROLES: EdgeRole[] = ['allow', 'drop', 'default'];
// Set via .style()/inline style rather than .attr() so var() resolves through the normal CSS
// cascade (and through the shadow boundary, like the rest of this library's theming) — unlike
// the rest of the library, the service map (D3-driven SVG) had no theming surface at all before
// this; downstream shells can override these on the host to match their own palette.
const EDGE_COLOR_VAR: Record<EdgeRole, string> = {
    allow: 'var(--antrea-color-edge-allow, #3ebd93)',
    drop: 'var(--antrea-color-edge-drop, #e45454)',
    default: 'var(--antrea-color-edge-default, #6a9fb5)',
};
// Same rationale as EDGE_COLOR_VAR above: set via .style() so var() resolves.
const MAP_COLOR_VAR = {
    nsHullFill: 'var(--antrea-color-map-ns-hull-fill, rgba(106,159,181,0.08))',
    nsHullStroke: 'var(--antrea-color-map-ns-hull-stroke, rgba(106,159,181,0.25))',
    nsLabel: 'var(--antrea-color-map-ns-label, rgba(106,159,181,0.5))',
    edgeLabelBg: 'var(--antrea-color-map-edge-label-bg, rgba(23,36,43,0.85))',
    edgeLabelText: 'var(--antrea-color-map-edge-label-text, #c5d1d8)',
    externalFill: 'var(--antrea-color-map-external-fill, #3d2c1e)',
    externalStroke: 'var(--antrea-color-map-external-stroke, #c4956a)',
    nodeFill: 'var(--antrea-color-map-node-fill, #1e3a4c)',
    nodeStroke: 'var(--antrea-color-map-node-stroke, #6a9fb5)',
    nodeText: 'var(--antrea-color-map-node-text, #e0e8ec)',
    nodeNamespaceText: 'var(--antrea-color-map-node-namespace-text, rgba(106,159,181,0.7))',
    // A colour of its own for the fully anonymous peer: it reuses External's diamond because the
    // two are structurally the same, but they must not read as the same thing.
    undisclosedFill: 'var(--antrea-color-map-undisclosed-fill, #33283d)',
    undisclosedStroke: 'var(--antrea-color-map-undisclosed-stroke, #a98bc4)',
} as const;

function edgeRole(edge: WorkloadEdge): EdgeRole {
    const all = new Set([...edge.ingressActions, ...edge.egressActions]);
    if (all.has(NetworkPolicyRuleAction.Drop) || all.has(NetworkPolicyRuleAction.Reject)) return 'drop';
    if (all.has(NetworkPolicyRuleAction.Allow)) return 'allow';
    return 'default';
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

interface D3Node extends d3.SimulationNodeDatum { id: string; shortName: string; namespace: string; kind: NodeKind; detail: string; }

/** Whether this node is drawn as a diamond: an aggregate of unbounded cardinality with no
 * namespace, excluded from the hulls and not drillable. `undisclosed` reuses External's shape
 * because it is structurally the same object - in a different colour, because External means
 * outside the cluster and this means inside it and withheld. */
function isDiamond(d: { kind: NodeKind }): boolean {
    return d.kind === 'external' || d.kind === 'undisclosed';
}

/** Whether this node belongs to a namespace hull. A real hull is a d3.polygonHull over its
 * members' positions, so the collapsed namespace - which by definition has no members - is not
 * one of them: it is a fixed-size simulation node rendered in the hull's style, so that the
 * layout places it and edges terminate on it. */
function inNamespaceHull(d: { kind: NodeKind; namespace: string }): boolean {
    return d.kind === 'workload' && !!d.namespace;
}
interface D3Link extends d3.SimulationLinkDatum<D3Node> { edgeKey: string; connectionCount: number; role: EdgeRole; label: string; curveOffset: number; }

const HEIGHT = 900;
const NODE_RX = 8;
const NODE_PADDING_X = 14;
const NODE_PADDING_Y = 8;
const EXTERNAL_SIZE = 20;
const CURVE_OFFSET = 30;
// The collapsed namespace is a fixed-size node, not a hull over members it does not have, so it
// needs a size of its own: wide and shallow enough to read as a small, empty namespace outline
// rather than as an oversized workload.
const COLLAPSED_NS_MIN_WIDTH = 150;
const COLLAPSED_NS_HEIGHT = 56;
const LOCK_SIZE = 11;

function textWidth(text: string, size: number): number { return text.length * size * 0.6; }

function nodeHalfSize(d: D3Node): { hw: number; hh: number } {
    if (isDiamond(d)) return { hw: EXTERNAL_SIZE, hh: EXTERNAL_SIZE };
    const w = Math.max(textWidth(d.shortName, 12), textWidth(d.detail, 9)) + NODE_PADDING_X * 2 + (d.kind === 'undisclosedNamespace' ? LOCK_SIZE + 6 : 0);
    if (d.kind === 'undisclosedNamespace') {
        return { hw: Math.max(w, COLLAPSED_NS_MIN_WIDTH) / 2, hh: (COLLAPSED_NS_HEIGHT + NODE_PADDING_Y) / 2 };
    }
    return { hw: w / 2, hh: (38 + NODE_PADDING_Y) / 2 };
}

function nodeBoundary(d: D3Node, tx: number, ty: number, gap: number): [number, number] {
    const cx = d.x ?? 0; const cy = d.y ?? 0;
    const dx = tx - cx; const dy = ty - cy;
    const len = Math.sqrt(dx * dx + dy * dy) || 1;
    const ux = dx / len; const uy = dy / len;
    const { hw, hh } = nodeHalfSize(d);
    let scale: number;
    if (isDiamond(d)) scale = hw / (Math.abs(ux) + Math.abs(uy));
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
    if (isDiamond(d)) return EXTERNAL_SIZE + 4;
    const { hw } = nodeHalfSize(d);
    return hw + 4;
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

// A padlock, drawn rather than set as a character: the codebase's own affordances are drawn or
// are plain glyphs, and the Unicode lock is an emoji in most fonts.
const LOCK_PATH = 'M5 7.2V5.4a3 3 0 0 1 6 0v1.8h.4c.6 0 1.1.5 1.1 1.1v4.6c0 .6-.5 1.1-1.1 1.1H4.6c-.6 0-1.1-.5-1.1-1.1V8.3c0-.6.5-1.1 1.1-1.1H5zm1.4 0h3.2V5.4a1.6 1.6 0 0 0-3.2 0v1.8z';

/** Draws the same padlock into an SVG node group, at (x, y) in the group's own coordinates. */
function appendLock(g: d3.Selection<SVGGElement, unknown, null, undefined>, x: number, y: number, color: string) {
    g.append('path').attr('d', LOCK_PATH)
        .attr('transform', `translate(${x},${y}) scale(${LOCK_SIZE / 16})`)
        .style('fill', color);
}

/** The one definition of what a collapsed namespace's tooltip says, so the visible tooltip and
 * the accessible name cannot drift apart. Actionable on purpose: it names the grant that would
 * disclose the namespace, which is the whole reason this node gets a tooltip and the fully
 * anonymous one does not. */
function collapsedNamespaceTooltip(namespace: string): string {
    return `Workloads in ${namespace} are not shown. Seeing them requires get flows/identity on ${namespace}.`;
}

/** Renders one cell of the list that redaction may have emptied. The mark goes in the cell and
 * not on the row: a row-level marker cannot say *which* field was withheld, and it leaves a
 * withheld Dest Service cell looking simply empty. A tooltip is attached only where the view
 * carries one, i.e. only where it is actionable. */
function renderRedactable(view: EndpointView): string | TemplateResult {
    const addr = view.detail ? html`<div class="endpoint-addr">${view.detail}</div>` : nothing;
    if (!view.redacted) return view.detail ? html`${view.text}${addr}` : view.text;
    const label = view.tooltip ?? 'Withheld: you are not authorized to identify this endpoint.';
    const mark = html`<span class="redacted" title=${label}
        >${view.text}<svg class="lock" viewBox="0 0 16 16" width="11" height="11" role="img" aria-label=${label}
            ><path d=${LOCK_PATH} fill="currentColor"></path></svg
    ></span>`;
    return html`${mark}${addr}`;
}

// `field` marks a column as sortable via the existing SortField/sortValue() machinery — columns
// a FlowTableColumnsProcessor inserts don't have one, so they render but aren't sortable.
const BASE_COLUMNS: (FlowTableColumn & { field?: SortField })[] = [
    { key: 'lastSeen', field: 'lastSeen', label: 'Last Seen', render: e => new Date(e.flow.endTs).toLocaleTimeString() },
    { key: 'source', field: 'source', label: 'Source', render: e => renderRedactable(endpointView(e.flow.k8s.sourceDisclosure, e.flow.k8s.sourcePodNamespace, e.flow.k8s.sourcePodName, e.flow.ip.source, e.flow.k8s.flowType as FlowType === FlowType.FromExternal)) },
    { key: 'destination', field: 'destination', label: 'Destination', render: e => renderRedactable(endpointView(e.flow.k8s.destinationDisclosure, e.flow.k8s.destinationPodNamespace, e.flow.k8s.destinationPodName, e.flow.ip.destination, e.flow.k8s.flowType as FlowType === FlowType.ToExternal)) },
    { key: 'destinationService', field: 'destinationService', label: 'Dest Service', render: e => renderRedactable(destinationServiceView(e.flow.k8s.destinationDisclosure, e.flow.k8s.destinationServicePortName, e.flow.k8s.flowType as FlowType === FlowType.ToExternal)) },
    { key: 'protocol', field: 'protocol', label: 'Protocol', render: e => getProtocolName(e.flow.transport.protocolNumber) },
    { key: 'destPort', field: 'destPort', label: 'Dest Port', render: e => String(e.flow.transport.destinationPort) },
    { key: 'bytesFwd', field: 'bytesFwd', label: 'Bytes (Fwd)', render: e => formatBytes(e.flow.stats.octetTotalCount) },
    { key: 'bytesRev', field: 'bytesRev', label: 'Bytes (Rev)', render: e => formatBytes(e.flow.reverseStats.octetTotalCount) },
    { key: 'ingressPolicy', field: 'ingressPolicy', label: 'Ingress Policy', render: e => formatPolicyInfo(e.flow.k8s.ingressNetworkPolicyName, e.flow.k8s.ingressNetworkPolicyRuleAction, e.flow.k8s.destinationDisclosure) || '-' },
    { key: 'egressPolicy', field: 'egressPolicy', label: 'Egress Policy', render: e => formatPolicyInfo(e.flow.k8s.egressNetworkPolicyName, e.flow.k8s.egressNetworkPolicyRuleAction, e.flow.k8s.sourceDisclosure) || '-' },
    { key: 'flowType', field: 'flowType', label: 'Flow Type', render: e => flowTypeLabel[e.flow.k8s.flowType as FlowType] ?? 'Unknown' },
];

// ── Component ─────────────────────────────────────────────────────────────────

export class AntreaFlowVisibilityPage extends SessionAwarePage {
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
        .multiselect-hint { padding: 5px 10px; font-size: 11px; color: var(--antrea-color-text-muted, #adbbc4); }

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
            background: var(--antrea-color-map-tooltip-bg, rgba(20,32,40,0.95));
            border: 1px solid var(--antrea-color-map-tooltip-border, #555);
            border-radius: 4px; padding: 8px 10px; font-size: 11px;
            color: var(--antrea-color-map-tooltip-text, #d0dae0); line-height: 1.5; max-width: 320px;
            z-index: 100; transition: opacity 150ms ease; pointer-events: none;
        }
        .map-legend {
            position: absolute; bottom: 10px; left: 10px;
            background: var(--antrea-color-map-legend-bg, rgba(20,32,40,0.9));
            border: 1px solid var(--antrea-color-map-legend-border, rgba(86,86,86,0.5));
            border-radius: 4px; padding: 8px 12px; font-size: 10px;
            color: var(--antrea-color-map-legend-text, #adbbc4); line-height: 1.8;
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
        .edge-extra { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--antrea-color-border, #314351); }
        .warn { color: var(--antrea-color-warning, #f5a623); }
        .redacted { display: inline-flex; align-items: center; gap: 4px; color: var(--antrea-color-text-muted, #adbbc4); }
        .redacted .lock { flex-shrink: 0; opacity: 0.8; }
        .endpoint-addr {
            font-family: var(--antrea-font-mono, ui-monospace, Menlo, monospace);
            font-size: 0.6875rem; color: var(--antrea-color-text-disabled, #6a7f8e);
        }
    `];

    // View. A property, not @state: Flow List and Service Map are second-level pages under Flow
    // Visibility in the sidebar (see antrea-nav-group in @antrea/ui-components and nav.tsx), so
    // the host drives this from the URL — there is no in-page control for it any more.
    @property() viewMode: 'list' | 'map' = 'list';
    @state() private _paused = false;

    // Scope: which namespace this page observes flows in, or the whole cluster. Exactly one of
    // these is ever set, and neither being set is the initial state - see _scope.
    @state() private _observedNs = '';
    @state() private _clusterWide = false;
    // The selector's options (GET /api/v1/flows/namespaces). Null until it resolves, which is
    // what _flowNamespacesLoaded distinguishes from a failure.
    @state() private _flowNamespaces: FlowNamespacesResponse | null = null;
    @state() private _flowNamespacesLoaded = false;

    // Stream
    @state() private _entries: FlowEntry[] = [];
    @state() private _connected = false;
    @state() private _error: string | null = null;
    // Set on 501: Flow Aggregator integration is off for this deployment, a fixed fact about the
    // server that no filter or scope change on this page can affect. Terminal for the session,
    // and must keep _startStream from re-opening the stream on any later filter change.
    private _integrationDisabled = false;
    // Set to _filterKey on 403: FA refused this user the scope that request asked for. Unlike
    // _integrationDisabled this is not terminal - authorization is per-scope, so it only needs to
    // block _startStream from retrying the exact same request that was just refused, not every
    // request forever. _applyFilter changing _filterKey (a peer-filter edit, or eventually a
    // different scope once the observed-namespace selector exists) clears it implicitly, since
    // the comparison in _startStream stops matching.
    private _forbiddenFilterKey: string | null = null;
    @state() private _droppedCount = 0;
    @state() private _evictionWarning = false;

    // The applied peer filters, without a scope. The scope lives in _observedNs/_clusterWide and
    // is folded in by _composeFilter, which is the only thing that can build a FlowStreamFilter -
    // the type cannot express one without a scope, and the backend answers a scope-less request
    // with a 400.
    private _peerFilter: FlowPeerFilter = {};
    // The filter the open stream was built from, or null when no scope is selected and so no
    // stream is open. Not reactive: nothing renders it.
    private _filter: FlowStreamFilter | null = null;

    // Filter UI state
    @state() private _pendingNs: string[] = [];
    // The applied half of the "Unidentified peers" pseudo-option. Client-side, so unlike the
    // real filters it changes nothing about the open stream.
    @state() private _unidentifiedPeersOnly = false;
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

    // Plugin extension points — populated by the host from its plugin registry (see
    // `@antrea/ui-plugin-sdk`). Never set by manifest/attribute, so `attribute: false`.
    @property({ attribute: false }) edgeExtraRenderers: EdgeExtraRenderer[] = [];
    @property({ attribute: false }) flowTableColumnsProcessors: FlowTableColumnsProcessor[] = [];

    // Non-reactive refs
    private _store = new FlowStore();
    private _client: FlowStreamClient | null = null;
    private _simulation: d3.Simulation<D3Node, D3Link> | null = null;
    // The key of the request the open stream was built from, or '' when there is none. Compared
    // against itself to decide whether a change actually needs a reconnect.
    private _filterKey = '';
    private _refreshTimer: ReturnType<typeof setInterval> | null = null;
    private _ro: ResizeObserver | null = null;
    private _graphRef: GraphData = { nodes: [], edges: [], edgeMap: new Map(), nodeMap: new Map() };
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
        window.addEventListener('pointerdown', this._handleDocClick);
    }

    protected override onSessionReady() {
        // The namespace list is fetched first and the stream opens only afterwards, which is the
        // reverse of the order this page used when the namespace menu merely narrowed a filter:
        // a stream now has to name its scope, so no stream can open before the scope does. There
        // is no credential to wait for beyond the session cookie, which the browser attaches to
        // both fetches itself.
        void this._loadFlowNamespaces();
    }

    /** Loads the selector's options, then restores the scope from the URL and opens the stream if
     * that leaves one selected. A failure is an error - unlike an empty list, which is a real
     * answer this user is entitled to (see _renderScopeEmptyState). */
    private async _loadFlowNamespaces() {
        try {
            this._flowNamespaces = await flowNamespaces();
        } catch {
            this._flowNamespaces = null;
            this._error = FLOW_NAMESPACES_ERROR_MESSAGE;
        }
        this._flowNamespacesLoaded = true;
        this._restoreScopeFromURL();
        this._refreshStream();
    }

    /** Restores the scope a refresh or a shared link carries. A namespace is honoured even when
     * it is not in the list, since the list can be incomplete and the Flow Aggregator authorizes
     * the stream anyway; cluster scope is honoured only when the backend says this user has it,
     * because unlike a namespace there is nothing to show in the selector otherwise. */
    private _restoreScopeFromURL() {
        const params = new URLSearchParams(window.location.search);
        if (params.get(CLUSTER_WIDE_PARAM) === 'true') {
            if (this._flowNamespaces?.clusterWide) this._clusterWide = true;
            return;
        }
        const ns = params.get(OBSERVED_NS_PARAM)?.trim();
        if (ns) this._observedNs = ns;
    }

    /** Writes the current scope back to the URL, in place: this is page state a refresh should
     * keep, not a navigation worth a history entry. */
    private _writeScopeToURL() {
        const url = new URL(window.location.href);
        url.searchParams.delete(OBSERVED_NS_PARAM);
        url.searchParams.delete(CLUSTER_WIDE_PARAM);
        if (this._clusterWide) url.searchParams.set(CLUSTER_WIDE_PARAM, 'true');
        else if (this._observedNs) url.searchParams.set(OBSERVED_NS_PARAM, this._observedNs);
        if (url.toString() === window.location.href) return;
        window.history.replaceState(window.history.state, '', url.toString());
        // replaceState mutates the URL without telling anyone. A router that reads the location
        // through its own abstraction - react-router's useLocation(), in this repo's host - goes
        // on serving the value it last saw, so links it builds from the query string carry a
        // stale scope, or none at all on a fresh load. Both host routers already resync on
        // popstate, which is exactly "the URL changed underneath you"; dispatching it is
        // host-agnostic, where a custom event would need every host to opt in and would fail
        // silently in the one that had not.
        window.dispatchEvent(new PopStateEvent('popstate', { state: window.history.state }));
    }

    /** The selected scope, or null when none is: the state in which this page deliberately opens
     * no stream at all rather than falling back to observing everything. */
    private get _scope(): FlowStreamScope | null {
        if (this._clusterWide) return { clusterWide: true };
        if (this._observedNs) return { observedNamespace: this._observedNs };
        return null;
    }

    private _composeFilter(): FlowStreamFilter | null {
        const scope = this._scope;
        return scope ? { ...this._peerFilter, ...scope } : null;
    }

    /** The namespaces the selector offers. The URL may name one the backend did not list (see
     * _restoreScopeFromURL), and it has to appear here or the <select> would show no selection
     * for a scope that is nevertheless in force. */
    private get _observableNs(): string[] {
        const ns = new Set(observableNamespaces(this._flowNamespaces));
        if (this._observedNs) ns.add(this._observedNs);
        return Array.from(ns).sort();
    }

    /** Candidates the backend reported this user cannot observe. Offered as disabled options
     * rather than omitted, so a namespace the user expected to find is visibly refused rather
     * than silently missing. */
    private get _unobservableNs(): string[] {
        return (this._flowNamespaces?.namespaces ?? [])
            .filter(n => !n.canObserve && n.namespace !== this._observedNs)
            .map(n => n.namespace)
            .sort();
    }

    private _onScopeChange(value: string) {
        const clusterWide = value === SCOPE_CLUSTER_WIDE;
        const observedNs = clusterWide ? '' : value;
        if (clusterWide === this._clusterWide && observedNs === this._observedNs) return;
        this._clusterWide = clusterWide;
        this._observedNs = observedNs;
        // The scope is not its own peer, so a peer-namespace filter naming it is dropped here
        // rather than left selected against an option that no longer exists (see _availableNs).
        if (observedNs) this._pendingNs = this._pendingNs.filter(n => n !== observedNs);
        this._writeScopeToURL();
        this._refreshStream();
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
        super.updated(changed);
        // Setup ResizeObserver once the DOM is ready
        if (changed.has('viewMode') && this.viewMode === 'map') {
            // The <svg> is always freshly created when switching into map view (render()
            // ternaries between list/map templates, so Lit tears down and recreates the whole
            // subtree). Reset the memoized topology key so _buildServiceMap() does a full
            // rebuild instead of taking the "same topology, just update stroke widths" fast
            // path against a brand new, empty SVG.
            this._prevTopologyKey = '';
            this._setupResizeObserver();
            this._buildServiceMap();
        }
        // The <svg>/simulation only exist while in map view — leaving it tears down the DOM
        // Lit-side, but the simulation would otherwise keep ticking against detached nodes and
        // the ResizeObserver would keep observing until disconnectedCallback.
        if (changed.has('viewMode') && this.viewMode !== 'map') {
            this._simulation?.stop();
            this._ro?.disconnect();
            this._ro = null;
        }
        // Rebuild map when entries or SVG width change
        if ((changed.has('_entries') || changed.has('_svgWidth') || changed.has('_unidentifiedPeersOnly')) && this.viewMode === 'map') {
            this._buildServiceMap();
        }
        if (changed.has('_selectedEdgeKey')) {
            this._dispatchEdgeSelected();
        }
    }

    private _dispatchEdgeSelected() {
        const edge = this._selectedEdgeKey ? this._graphRef.edgeMap.get(this._selectedEdgeKey) : undefined;
        const detail = edge ? edgeToSelection(edge) : null;
        this.dispatchEvent(new CustomEvent<EdgeSelection | null>('antrea-edge-selected', { detail, bubbles: true, composed: true }));
    }

    // ── Stream management ─────────────────────────────────────────────────────

    private _startStream() {
        // Fail closed on no scope: an unscoped stream is not "everything", it is a request the
        // backend rejects, and defaulting to cluster-wide would silently ask for a grant this
        // user probably does not hold. The page shows an empty state instead - see
        // _renderScopeEmptyState.
        if (!this._filter) return;
        if (this._paused || this._integrationDisabled || this._filterKey === this._forbiddenFilterKey) return;
        this._client?.stop();
        this._client = new FlowStreamClient(this._filter, {
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
                // A 401 means the session is over; there is no refresh left to attempt. The
                // client has already stopped itself, and the host will log the user out.
                this.dispatchSessionExpired();
            },
            onDisabled: () => {
                // A 501 means Flow Aggregator integration is off for this deployment. The
                // client has already stopped itself; there is nothing to retry.
                this._integrationDisabled = true;
                this._client = null;
                this._connected = false;
                this._error = FLOW_VISIBILITY_DISABLED_MESSAGE;
            },
            onForbidden: () => {
                // A 403 means FA refused this user the scope _filter asked for. The client has
                // already stopped itself; record which request was refused so _startStream does
                // not retry it, without blocking a later request for a different scope (there is
                // no route guard upstream of this page - authorization is entirely FA's, so this
                // is the only place that can react to it).
                this._forbiddenFilterKey = this._filterKey;
                this._client = null;
                this._connected = false;
                this._error = FLOW_VISIBILITY_FORBIDDEN_MESSAGE;
            },
        });
        this._client.start();
    }

    private _stopStream() {
        this._client?.stop();
        this._client = null;
        this._connected = false;
    }

    private _applyFilter(filter: FlowPeerFilter) {
        this._peerFilter = filter;
        this._refreshStream();
    }

    /** Reopens the stream if the request the scope and the peer filters now describe differs from
     * the one that is open. The single chokepoint for both, since either can change it: callers
     * only ever build peer filters, which FlowStreamFilter's own type cannot express without a
     * scope, and the scope is state of the page rather than of any filter. */
    private _refreshStream() {
        const filter = this._composeFilter();
        const newKey = filter ? streamFilterKey(filter) : '';
        if (newKey === this._filterKey) return;
        this._filterKey = newKey;
        this._filter = filter;
        this._store.clear();
        this._entries = [];
        this._evictionWarning = false;
        this._droppedCount = 0;
        this._selectedEdgeKey = null;
        this._client?.stop();
        this._client = null;
        if (!this._paused) this._startStream();
    }

    // ── Filter actions ────────────────────────────────────────────────────────

    private _onApplyFilters() {
        this._nsOpen = false; this._podOpen = false; this._svcOpen = false;
        const filter: FlowPeerFilter = {};
        // Stripped out here rather than sent: it is not a namespace, and the backend would
        // reject it as one (or, worse, match nothing and look like an empty result).
        this._unidentifiedPeersOnly = this._pendingNs.includes(UNIDENTIFIED_PEERS_OPTION);
        const peerNs = this._pendingNs.filter(n => n !== UNIDENTIFIED_PEERS_OPTION);
        if (peerNs.length) filter.namespaces = peerNs;
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
        this._unidentifiedPeersOnly = false;
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

    /**
     * The peer-namespace menu's options: the namespaces seen at the far end of the flows this
     * stream has delivered so far, which is why the menu fills in as they arrive.
     *
     * Deliberately not intersected with the namespaces this user may read Kubernetes objects in,
     * the way it was while this control doubled as a scope. Those are a different grant from
     * flows/identity, and after per-user redaction the data is itself the authorization
     * boundary: a namespace can only appear on a record at all if the Flow Aggregator already
     * decided this user may identify it, so re-checking could only wrongly hide a legitimate
     * option.
     */
    private get _availableNs(): string[] {
        const ns = new Set<string>();
        for (const e of this._entries) {
            if (e.flow.k8s.sourcePodNamespace) ns.add(e.flow.k8s.sourcePodNamespace);
            if (e.flow.k8s.destinationPodNamespace) ns.add(e.flow.k8s.destinationPodNamespace);
        }
        // The scope is not its own peer.
        if (this._observedNs) ns.delete(this._observedNs);
        // A selection stays in the menu even once its traffic goes quiet, so that it does not
        // vanish from under the user while still being applied.
        for (const selected of this._pendingNs) if (selected !== UNIDENTIFIED_PEERS_OPTION) ns.add(selected);
        return [UNIDENTIFIED_PEERS_OPTION, ...Array.from(ns).sort()];
    }

    /** The entries the list and the map show. Everything but the "Unidentified peers"
     * pseudo-filter is applied by the Flow Aggregator; that one cannot be, because it selects
     * records by what was withheld from them. */
    private get _visibleEntries(): FlowEntry[] {
        if (!this._unidentifiedPeersOnly) return this._entries;
        return this._entries.filter(e =>
            endpointRedacted(e.flow.k8s.sourceDisclosure) || endpointRedacted(e.flow.k8s.destinationDisclosure));
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

        const graph = buildGraph(this._visibleEntries);
        this._graphRef = graph;

        // If a topology rebuild drops the currently-selected edge, clear the selection so
        // _dispatchEdgeSelected() emits null — otherwise a host listening on the
        // antrea-edge-selected extension point would keep believing an edge is selected even
        // though the details card has disappeared (render() returns nothing once the edge is
        // gone from edgeMap).
        if (this._selectedEdgeKey && !graph.edgeMap.has(this._selectedEdgeKey)) {
            this._selectedEdgeKey = null;
        }

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
        for (const role of EDGE_ROLES) {
            defs.append('marker')
                .attr('id', `arrowhead-${role}`)
                .attr('viewBox', '-10 -5 10 10').attr('refX', 0).attr('refY', 0)
                .attr('markerWidth', 7).attr('markerHeight', 7).attr('orient', 'auto')
                .append('path').attr('d', 'M-10,-4L0,0L-10,4').style('fill', EDGE_COLOR_VAR[role]);
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
                role: edgeRole(e),
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
            if (!inNamespaceHull(n)) continue;
            let arr = nsMap.get(n.namespace);
            if (!arr) { arr = []; nsMap.set(n.namespace, arr); }
            arr.push(n);
        }
        const nsHulls = container.append('g').attr('class', 'ns-hulls');
        const nsGroups: { nodes: D3Node[]; path: d3.Selection<SVGPathElement, unknown, null, undefined>; label: d3.Selection<SVGTextElement, unknown, null, undefined>; }[] = [];
        for (const [ns, nodes] of nsMap) {
            if (nodes.length < 1) continue;
            const path = nsHulls.append('path').style('fill', MAP_COLOR_VAR.nsHullFill).style('stroke', MAP_COLOR_VAR.nsHullStroke).attr('stroke-width', 1).attr('stroke-dasharray', '4,2');
            const label = nsHulls.append('text').text(ns).style('fill', MAP_COLOR_VAR.nsLabel).attr('font-size', '10px').attr('font-weight', '600');
            nsGroups.push({ nodes, path, label });
        }

        // Edge paths
        const linkPaths = container.append('g').selectAll<SVGPathElement, D3Link>('path')
            .data(d3Links).join('path')
            .attr('fill', 'none')
            .style('stroke', d => EDGE_COLOR_VAR[d.role])
            .attr('stroke-width', d => Math.min(1.5 + Math.log2(d.connectionCount + 1), 6))
            .attr('stroke-opacity', 0.6)
            .attr('marker-end', d => `url(#arrowhead-${d.role})`)
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
        edgeLabelGroups.append('rect').attr('rx', 3).attr('ry', 3).style('fill', MAP_COLOR_VAR.edgeLabelBg).style('stroke', d => EDGE_COLOR_VAR[d.role]).attr('stroke-width', 0.5).attr('stroke-opacity', 0.5);
        edgeLabelGroups.append('text').text(d => d.label).attr('text-anchor', 'middle').attr('dominant-baseline', 'central').style('fill', MAP_COLOR_VAR.edgeLabelText).attr('font-size', '9px').attr('pointer-events', 'none');

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
            })
            // Only the collapsed namespace has anything actionable to say. The anonymous peer
            // has no namespace to name a grant in, so it gets no tooltip at all - see
            // describeEndpoint - and a workload node is fully disclosed and needs none.
            .on('mouseenter', (event, d) => {
                if (d.kind !== 'undisclosedNamespace') return;
                this._showNodeTooltip(event, d.shortName, collapsedNamespaceTooltip(d.shortName));
            })
            .on('mousemove', (event, d) => {
                if (d.kind !== 'undisclosedNamespace' || !this._tooltipEl) return;
                this._tooltipEl.style.left = `${event.pageX + 12}px`;
                this._tooltipEl.style.top = `${event.pageY + 12}px`;
            })
            .on('mouseleave', (_event, d) => {
                if (d.kind !== 'undisclosedNamespace') return;
                this._hideTooltip();
            });

        nodeGroup.each(function (this: SVGGElement, d: D3Node) {
            const g = d3.select(this);
            if (d.kind === 'external' || d.kind === 'undisclosed') {
                const s = EXTERNAL_SIZE;
                const stroke = d.kind === 'external' ? MAP_COLOR_VAR.externalStroke : MAP_COLOR_VAR.undisclosedStroke;
                const fill = d.kind === 'external' ? MAP_COLOR_VAR.externalFill : MAP_COLOR_VAR.undisclosedFill;
                g.append('polygon').attr('points', `0,${-s} ${s},0 0,${s} ${-s},0`).style('fill', fill).style('stroke', stroke).attr('stroke-width', 2);
                const label = g.append('text').attr('dy', s + 14).attr('text-anchor', 'middle').style('fill', stroke).attr('font-size', '11px').attr('font-weight', '600');
                if (d.kind === 'undisclosed') {
                    // A static marker only: there is no namespace to name, so there is nothing
                    // actionable to say and no tooltip behind it. The label already carries the
                    // meaning.
                    appendLock(g, -6, -s - 20, stroke);
                    label.text(d.shortName);
                    g.append('text').text(d.detail).attr('dy', s + 27).attr('text-anchor', 'middle').style('fill', stroke).attr('font-size', '9px');
                } else {
                    label.text(d.shortName);
                }
                return;
            }
            const { hw, hh } = nodeHalfSize(d);
            if (d.kind === 'undisclosedNamespace') {
                // Drawn in the namespace hull's style rather than the workload's: what this
                // stands for is a namespace whose contents are hidden, and a rounded rect in
                // this map's grammar is a workload. It is a tidy rectangle where a real hull is
                // an irregular polygon, because there are no members to hull.
                g.append('rect').attr('x', -hw).attr('y', -hh).attr('width', hw * 2).attr('height', hh * 2)
                    .attr('rx', NODE_RX).attr('ry', NODE_RX)
                    .style('fill', MAP_COLOR_VAR.nsHullFill).style('stroke', MAP_COLOR_VAR.nsHullStroke)
                    .attr('stroke-width', 1).attr('stroke-dasharray', '4,2');
                g.append('text').text(d.shortName).attr('dy', -3).attr('x', LOCK_SIZE / 2 + 3).attr('text-anchor', 'middle')
                    .style('fill', MAP_COLOR_VAR.nsLabel).attr('font-size', '11px').attr('font-weight', '600');
                appendLock(g, -textWidth(d.shortName, 11) / 2 - LOCK_SIZE / 2 + 3, -13, MAP_COLOR_VAR.nsLabel);
                g.append('text').text(d.detail).attr('dy', 15).attr('text-anchor', 'middle')
                    .style('fill', MAP_COLOR_VAR.nodeNamespaceText).attr('font-size', '9px');
                // Worth a tooltip because it is actionable: it can name the grant that would
                // disclose this namespace. aria-label rather than an SVG <title> - the hover
                // handler below draws the visible one in the same panel the edge tooltip uses,
                // and a <title> would stack the browser's own on top of it.
                g.attr('role', 'img').attr('aria-label', collapsedNamespaceTooltip(d.shortName));
                return;
            }
            g.append('rect').attr('x', -hw).attr('y', -hh).attr('width', hw * 2).attr('height', hh * 2).attr('rx', NODE_RX).attr('ry', NODE_RX).style('fill', MAP_COLOR_VAR.nodeFill).style('stroke', MAP_COLOR_VAR.nodeStroke).attr('stroke-width', 1.5);
            g.append('text').text(d.shortName).attr('dy', -3).attr('text-anchor', 'middle').style('fill', MAP_COLOR_VAR.nodeText).attr('font-size', '12px').attr('font-weight', '600');
            g.append('text').text(d.detail).attr('dy', 13).attr('text-anchor', 'middle').style('fill', MAP_COLOR_VAR.nodeNamespaceText).attr('font-size', '9px');
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
                if (!inNamespaceHull(n)) continue;
                const c = centroids.get(n.namespace);
                if (c) { c.x += n.x!; c.y += n.y!; c.count++; }
                else centroids.set(n.namespace, { x: n.x!, y: n.y!, count: 1 });
            }
            const strength = 0.15 * alpha;
            for (const n of d3Nodes) {
                if (!inNamespaceHull(n)) continue;
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
        const ingressAllow = !edge.ingressActions.has(NetworkPolicyRuleAction.Drop) && !edge.ingressActions.has(NetworkPolicyRuleAction.Reject);
        const egressAllow = !edge.egressActions.has(NetworkPolicyRuleAction.Drop) && !edge.egressActions.has(NetworkPolicyRuleAction.Reject);
        const portParts: string[] = [];
        edge.protoPorts.forEach((pSet, proto) => {
            const name = getProtocolName(proto);
            portParts.push(pSet.size > 0 ? `${name}(${Array.from(pSet).sort((a, b) => a - b).join(', ')})` : name);
        });

        // Built from text nodes rather than innerHTML: edge.source/target (derived from pod
        // labels) and policy names are server-supplied data, not markup we control.
        const div = (text: string) => {
            const e = document.createElement('div');
            e.textContent = text;
            return e;
        };
        const policyLine = (allow: boolean, direction: string, name: string) => {
            const line = document.createElement('div');
            const mark = document.createElement('span');
            mark.style.color = allow ? EDGE_COLOR_VAR.allow : EDGE_COLOR_VAR.drop;
            mark.textContent = allow ? '✓' : '✗';
            line.append(mark, document.createTextNode(` ${direction}: ${name}`));
            return line;
        };

        tip.replaceChildren();
        const header = div(`${nodeLabel(this._graphRef, edge.source)} → ${nodeLabel(this._graphRef, edge.target)}`);
        header.style.fontWeight = '600';
        header.style.marginBottom = '4px';
        tip.append(header);
        tip.append(div(portParts.join(', ') || '-'));
        tip.append(div(`${edge.connectionCount} connection${edge.connectionCount !== 1 ? 's' : ''}`));
        tip.append(div(`↑ ${formatBytes(edge.totalBytesForward)}   ↓ ${formatBytes(edge.totalBytesReverse)}`));
        if (edge.bitRate > 0) tip.append(div(`Throughput: ${formatBitRate(edge.bitRate)}`));
        if (edge.ingressPolicies.size > 0 || edge.egressPolicies.size > 0) {
            const hr = document.createElement('hr');
            hr.style.borderColor = 'var(--antrea-color-map-tooltip-border, #555)';
            hr.style.margin = '4px 0';
            tip.append(hr);
            for (const p of edge.ingressPolicies) tip.append(policyLine(ingressAllow, 'Ingress', p));
            for (const p of edge.egressPolicies) tip.append(policyLine(egressAllow, 'Egress', p));
        }
        tip.style.left = `${event.pageX + 12}px`;
        tip.style.top = `${event.pageY + 12}px`;
        tip.style.opacity = '1';
    }

    /**
     * The collapsed-node tooltip, in the same panel the edge tooltip uses.
     *
     * Not an SVG <title>, which is what this was first built with: the browser's native tooltip
     * looks nothing like the edge panel a few pixels away, and it takes about a second to appear.
     * The accessible name it provided comes from aria-label on the node group instead - using
     * both would pop a native tooltip on top of this one.
     */
    private _showNodeTooltip(event: MouseEvent, heading: string, body: string) {
        const tip = this._tooltipEl;
        if (!tip) return;
        // textContent, not innerHTML: a namespace name is server-supplied data, not markup.
        tip.replaceChildren();
        const header = document.createElement('div');
        header.textContent = heading;
        header.style.fontWeight = '600';
        header.style.marginBottom = '4px';
        const text = document.createElement('div');
        text.textContent = body;
        text.style.maxWidth = '260px';
        tip.append(header, text);
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
        hint?: string,
    ) {
        const displayText = selected.length === 0 ? 'All' : selected.length <= 2 ? selected.join(', ') : `${selected.slice(0, 2).join(', ')} +${selected.length - 2}`;
        return html`
            <div class="multiselect" @pointerdown=${(e: Event) => e.stopPropagation()}>
                <span class="multiselect-label">${label}</span>
                <button type="button" class="multiselect-btn" title=${hint ?? nothing} @click=${onToggleOpen}>
                    ${displayText}
                    <span class="multiselect-chevron">&#9662;</span>
                </button>
                ${open ? html`
                    <div class="multiselect-dropdown">
                        ${hint ? html`<div class="multiselect-hint">${hint}</div>` : nothing}
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

    /** The observed-namespace selector: required, single-select, and the same control in both
     * views so the two cannot disagree about what is being observed. Cluster scope is offered
     * only when the backend says this user holds it, and is labelled as the one scope in which
     * nothing is redacted - a real property of it, and not one anybody would guess. */
    private _renderScopeSelect() {
        // Selection is marked on each option rather than bound to the <select>'s own .value: Lit
        // commits an element's bindings before rendering its children, so a .value naming an
        // option that does not exist yet is silently dropped on first render.
        const value = this._clusterWide ? SCOPE_CLUSTER_WIDE : this._observedNs;
        return html`
            <div class="field-group scope-select" style="min-width:220px">
                <label class="field-label" for="observed-ns">Observed namespace</label>
                <select id="observed-ns" class="field-select"
                    @change=${(e: Event) => this._onScopeChange((e.target as HTMLSelectElement).value)}>
                    <option value="" .selected=${value === ''}>Select a namespace…</option>
                    ${this._flowNamespaces?.clusterWide ? html`
                        <option value=${SCOPE_CLUSTER_WIDE} .selected=${value === SCOPE_CLUSTER_WIDE}>All namespaces</option>
                    ` : nothing}
                    ${this._observableNs.map(ns => html`<option value=${ns} .selected=${value === ns}>${ns}</option>`)}
                    ${this._unobservableNs.map(ns => html`<option value=${ns} disabled>${ns} (not authorized)</option>`)}
                </select>
            </div>
            ${this._flowNamespaces?.incomplete ? this._renderScopeByName() : nothing}
        `;
    }

    /**
     * A free-text way to name a namespace the list does not offer.
     *
     * Only rendered when the backend reports the list as incomplete, and that condition is the
     * point rather than tidiness: when the list is exhaustive, a namespace absent from it is one
     * this user cannot observe, so a text box could only invite a request the Flow Aggregator
     * will refuse. It replaces telling people to hand-edit ?observedNamespace= into the URL,
     * which worked but is not an instruction to put in front of a user.
     *
     * Nothing is validated here. Authorization is the Flow Aggregator's, and it already answers a
     * namespace this user may not observe with a terminal, non-retryable error the page renders.
     */
    private _renderScopeByName() {
        const submit = (e: Event) => {
            const input = e.target as HTMLInputElement;
            const ns = input.value.trim();
            if (!ns) return;
            input.value = '';
            this._onScopeChange(ns);
        };
        return html`
            <div class="field-group" style="min-width:200px">
                <label class="field-label" for="observed-ns-by-name">Namespace not listed</label>
                <input id="observed-ns-by-name" class="field-input" type="text" placeholder="Type a name, then Enter"
                    @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') submit(e); }}>
            </div>
        `;
    }

    /** What the page shows in place of the flow list or the map while no scope is selected: an
     * explanation, including for the user who may observe no namespace at all, whose empty list
     * is a real answer rather than a failure. */
    private _renderScopeEmptyState() {
        if (!this._flowNamespacesLoaded) {
            return html`<p class="text-muted">Loading the namespaces you may observe flows in…</p>`;
        }
        // Only when the backend actually answered. A rejected fetch also leaves _flowNamespaces
        // null, and asserting "you are not authorized" underneath the failure banner would tell
        // a user their grants are missing when the request merely failed.
        const none = this._flowNamespaces !== null
            && this._observableNs.length === 0 && !this._flowNamespaces.clusterWide;
        return html`
            <p class="text-muted">${none ? NO_OBSERVABLE_NAMESPACES_MESSAGE : SELECT_SCOPE_MESSAGE}</p>
            ${this._flowNamespaces?.incomplete ? html`<p class="text-muted">${INCOMPLETE_NAMESPACES_NOTE}</p>` : nothing}
        `;
    }

    private _renderFilters() {
        const statusColor = this._connected ? 'var(--antrea-color-success, #60b515)' : 'var(--antrea-color-danger, #f54f47)';
        const statusText = this._connected ? 'Connected' : (this._paused ? 'Paused' : 'Disconnected');
        return html`
            <div class="filter-bar">
                <div class="filter-row">
                    ${this._renderScopeSelect()}
                    ${this._renderMultiSelect('Peer namespace', this._availableNs, this._pendingNs, this._nsOpen, () => { this._nsOpen = !this._nsOpen; this._podOpen = false; this._svcOpen = false; }, v => { this._pendingNs = this._toggleSelection(this._pendingNs, v); }, PEER_NAMESPACE_HINT)}
                    ${this._renderMultiSelect('Pod Names', this._availablePods, this._pendingPods, this._podOpen, () => { this._podOpen = !this._podOpen; this._nsOpen = false; this._svcOpen = false; }, v => { this._pendingPods = this._toggleSelection(this._pendingPods, v); }, IDENTIFIED_ONLY_HINT)}
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
                        <label class="field-label" title=${IDENTIFIED_ONLY_HINT}>Pod Label Selector</label>
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
        let filtered = this._visibleEntries;
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
        // Plugin-inserted columns (via registerFlowTableColumnsProcessor) have no `field`, so
        // they render but aren't sortable — see BASE_COLUMNS' comment. Each processor is
        // isolated: a throwing plugin drops its own contribution instead of breaking the page.
        const columns: (FlowTableColumn & { field?: SortField })[] =
            this.flowTableColumnsProcessors.reduce((cols, fn) => {
                try {
                    return fn(cols);
                } catch (e) {
                    console.error('plugin flow table columns processor threw, skipping it', e);
                    return cols;
                }
            }, BASE_COLUMNS as FlowTableColumn[]);
        return html`
            <div style="display:flex;flex-direction:column;gap:1rem;max-width:100%">
                <div class="flow-list-header">
                    <input class="flow-filter-input" type="text" placeholder="Filter flows…" .value=${this._textFilter}
                        @input=${(e: Event) => { this._textFilter = (e.target as HTMLInputElement).value; }} />
                    <span class="text-muted">${sorted.length} connections</span>
                </div>
                <div class="flow-list-scroll">
                    <table class="data-table" part="table" style="min-width:1200px">
                        <thead><tr>${columns.map(col => html`
                            <th part="table-header-cell" class=${col.field ? 'sortable' : ''} @click=${() => { if (col.field) onSort(col.field); }}>
                                ${col.label}${col.field && this._sortField === col.field ? (this._sortDir === 'asc' ? ' ▲' : ' ▼') : ''}
                            </th>`)}</tr></thead>
                        <tbody>${sorted.map(entry => html`<tr>
                            ${columns.map(col => html`<td part="table-cell">${col.render(entry)}</td>`)}
                        </tr>`)}</tbody>
                    </table>
                </div>
            </div>
        `;
    }

    private _renderEdgeExtra(edge: WorkloadEdge) {
        if (this.edgeExtraRenderers.length === 0) return nothing;
        const selection = edgeToSelection(edge);
        const nodes = this.edgeExtraRenderers
            .map(fn => {
                try {
                    return fn(selection);
                } catch (e) {
                    console.error('plugin edge extra renderer threw, skipping it', e);
                    return null;
                }
            })
            .filter((n): n is Node => n !== null);
        return nodes.length ? html`<div class="edge-extra">${nodes}</div>` : nothing;
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
                    <div><strong>Source:</strong> ${nodeLabel(this._graphRef, d.source)}</div>
                    <div><strong>Target:</strong> ${nodeLabel(this._graphRef, d.target)}</div>
                    <div><strong>Connections:</strong> ${d.connectionCount}</div>
                    <div><strong>Bytes (Fwd):</strong> ${formatBytes(d.totalBytesForward)}</div>
                    <div><strong>Bytes (Rev):</strong> ${formatBytes(d.totalBytesReverse)}</div>
                    ${d.bitRate > 0 ? html`<div><strong>Bit Rate:</strong> ${formatBitRate(d.bitRate)}</div>` : nothing}
                    <div><strong>Dest Ports:</strong> ${d.destPortsStr || '-'}</div>
                    ${d.ingressPolicies.length ? html`<div><strong>Ingress Policies:</strong> ${d.ingressPolicies.join(', ')}</div>` : nothing}
                    ${d.egressPolicies.length ? html`<div><strong>Egress Policies:</strong> ${d.egressPolicies.join(', ')}</div>` : nothing}
                    <div><strong>Flow Types:</strong> ${d.flowTypes.join(', ') || '-'}</div>
                </div>
                ${this._renderEdgeExtra(edge)}
            </div>
        `;
    }

    private _renderServiceMap() {
        return html`
            <div class="map-container">
                <svg id="graph-svg" class="map-svg" part="map-svg" width=${this._svgWidth} height=${HEIGHT}></svg>
                <div id="graph-tooltip" class="map-tooltip" part="map-tooltip"></div>
                <div class="map-legend" part="map-legend">
                    <div class="map-legend-title">Legend</div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" style="stroke:${EDGE_COLOR_VAR.allow}" stroke-width="2"/></svg><span>Allow</span></div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" style="stroke:${EDGE_COLOR_VAR.drop}" stroke-width="2"/></svg><span>Drop / Reject</span></div>
                    <div class="legend-row"><svg width="24" height="6"><line x1="0" y1="3" x2="24" y2="3" style="stroke:${EDGE_COLOR_VAR.default}" stroke-width="2"/></svg><span>No policy</span></div>
                    <div style="margin-top:2px;color:var(--antrea-color-text-muted, #8899a4)">Line thickness = connection count</div>
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
                        <p class="page-title">${this.viewMode === 'list' ? 'Flow List' : 'Service Map'}</p>
                    </div>

                    ${this._renderFilters()}

                    ${this._error ? html`<antrea-alert status="danger">${this._error}</antrea-alert>` : nothing}

                    ${!this._scope
                        ? this._renderScopeEmptyState()
                        : this.viewMode === 'list' ? this._renderFlowList() : this._renderServiceMap()}
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-flow-visibility-page', AntreaFlowVisibilityPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-flow-visibility-page': AntreaFlowVisibilityPage; }
}
