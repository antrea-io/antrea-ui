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

export enum FlowType {
    Unspecified = 0,
    IntraNode = 1,
    InterNode = 2,
    ToExternal = 3,
    FromExternal = 4,
}

export enum NetworkPolicyType {
    Unspecified = 0,
    K8s = 1,
    ANP = 2,
    ACNP = 3,
}

export enum NetworkPolicyRuleAction {
    NoAction = 0,
    Allow = 1,
    Drop = 2,
    Reject = 3,
}

export enum IPVersion {
    Unspecified = 0,
    IPv4 = 4,
    IPv6 = 6,
}

export enum FlowEndReason {
    Unspecified = 0,
    IdleTimeout = 1,
    ActiveTimeout = 2,
    EndOfFlow = 3,
    ForcedEnd = 4,
    LackOfResources = 5,
}

export interface Stats {
    packetTotalCount: number;
    packetDeltaCount: number;
    octetTotalCount: number;
    octetDeltaCount: number;
    throughput?: number;
}

export interface TCP {
    stateName: string;
}

export interface Transport {
    protocolNumber: number;
    sourcePort: number;
    destinationPort: number;
    tcp?: TCP;
}

export interface IP {
    version: IPVersion;
    source: string;
    destination: string;
}

// Pod labels are serialized as a flat map[string]string by the backend.
export type Labels = Record<string, string>;

// How much of one endpoint of a flow this user was authorized to see. Set by the Flow
// Aggregator's FlowStreamService only. Zero is Full, not "unspecified": a record nothing redacted
// reads as fully disclosed, which is every record on a cluster-wide stream. The backend never
// omits this field, but a record from a backend that predates it should still be read as Full.
export enum EndpointDisclosure {
    // Everything the record carries for the endpoint, including its Node placement and the
    // Egress applied to it.
    Full = 0,
    // The endpoint's Namespace, Pod and Service identity and the identity of the network policy
    // evaluated on its side, but not its Node placement or its Egress.
    Identity = 1,
    // Only what the flow itself shows - addresses, ports, protocol, statistics, and the type and
    // action of the policies evaluated on the endpoint's side - plus the endpoint's Namespace if
    // the connection was allowed.
    Flow = 2,
}

export interface Kubernetes {
    flowType: FlowType;

    // See EndpointDisclosure. Describes the endpoint, not a per-field guarantee: an endpoint can
    // lack a field below while still reporting Full, because the Flow Aggregator may simply
    // never have had it.
    sourceDisclosure: EndpointDisclosure;
    destinationDisclosure: EndpointDisclosure;

    sourcePodNamespace: string;
    sourcePodName: string;
    sourcePodUid: string;
    sourcePodLabels?: Labels;

    sourceNodeName: string;
    sourceNodeUid: string;

    destinationPodNamespace: string;
    destinationPodName: string;
    destinationPodUid: string;
    destinationPodLabels?: Labels;

    destinationNodeName: string;
    destinationNodeUid: string;

    destinationClusterIp: string;
    destinationServicePort: number;
    destinationServicePortName: string;
    destinationServiceUid: string;

    ingressNetworkPolicyType: NetworkPolicyType;
    ingressNetworkPolicyNamespace: string;
    ingressNetworkPolicyName: string;
    ingressNetworkPolicyUid: string;
    ingressNetworkPolicyRuleName: string;
    ingressNetworkPolicyRuleAction: NetworkPolicyRuleAction;

    egressNetworkPolicyType: NetworkPolicyType;
    egressNetworkPolicyNamespace: string;
    egressNetworkPolicyName: string;
    egressNetworkPolicyUid: string;
    egressNetworkPolicyRuleName: string;
    egressNetworkPolicyRuleAction: NetworkPolicyRuleAction;

    egressName: string;
    egressIp: string;
    egressNodeName: string;
    egressNodeUid: string;
    egressUid: string;
}

export interface Flow {
    id: string;
    startTs: string;
    endTs: string;
    endReason: FlowEndReason;
    ip: IP;
    transport: Transport;
    k8s: Kubernetes;
    stats: Stats;
    reverseStats: Stats;
}

export const flowTypeLabel: Record<FlowType, string> = {
    [FlowType.Unspecified]: "Unknown",
    [FlowType.IntraNode]: "IntraNode",
    [FlowType.InterNode]: "InterNode",
    [FlowType.ToExternal]: "ToExternal",
    [FlowType.FromExternal]: "FromExternal",
};

export const networkPolicyRuleActionLabel: Record<NetworkPolicyRuleAction, string> = {
    [NetworkPolicyRuleAction.NoAction]: "",
    [NetworkPolicyRuleAction.Allow]: "Allow",
    [NetworkPolicyRuleAction.Drop]: "Drop",
    [NetworkPolicyRuleAction.Reject]: "Reject",
};

export const protocolLabel: Record<number, string> = {
    1: "ICMP",
    6: "TCP",
    17: "UDP",
    58: "ICMPv6",
    132: "SCTP",
};

export function getProtocolName(protocolNumber: number): string {
    return protocolLabel[protocolNumber] ?? `Proto(${protocolNumber})`;
}

// How a rule action reads when the policy that carried it was withheld. The action itself is
// disclosed at every tier - only the policy's identity needs Identity - so this is the whole of
// what survives, and it is the most useful part: "my traffic to something I cannot see was
// dropped" stays answerable.
const hiddenPolicyActionLabel: Record<NetworkPolicyRuleAction, string> = {
    [NetworkPolicyRuleAction.NoAction]: "",
    [NetworkPolicyRuleAction.Allow]: "Allowed",
    [NetworkPolicyRuleAction.Drop]: "Dropped",
    [NetworkPolicyRuleAction.Reject]: "Rejected",
};

/**
 * Renders one of the two policy columns, keyed on the rule *action* rather than on the policy
 * name.
 *
 * | Name  | Action       | Tier | Means                             | Renders as                |
 * | ----- | ------------ | ---- | --------------------------------- | ------------------------- |
 * | set   | any          | any  | nothing withheld                  | `my-policy (Drop)`        |
 * | empty | not NoAction | Flow | withheld, but its effect is known | `Dropped (policy hidden)` |
 * | empty | not NoAction | else | never recorded, not withheld      | `Dropped`                 |
 * | empty | NoAction     | any  | no policy matched at all          | `` (the caller renders -) |
 *
 * `disclosure` is the tier of the endpoint this policy was evaluated on - the destination for an
 * ingress policy, the source for an egress one. Omitting it assumes the name was withheld, which
 * is the conservative reading for a caller that cannot say.
 *
 * The last row is not a redaction artefact and must not be marked as one: a policy that matched
 * always carries an action, so an empty name with NoAction was already empty before redaction
 * ran. A policy cell therefore never needs a bare lock - either the action is known and is
 * rendered, or there was no policy.
 */
export function formatPolicyInfo(
    name: string,
    action: NetworkPolicyRuleAction,
    disclosure?: EndpointDisclosure,
): string {
    const actionStr = networkPolicyRuleActionLabel[action];
    if (name) return actionStr ? `${name} (${actionStr})` : name;
    // An empty name is only evidence of redaction at the Flow tier. Above it the Flow Aggregator
    // may simply never have had the name - its own proto warns that an endpoint can lack a field
    // while still reporting full disclosure - and calling that "hidden" would blame redaction
    // for a gap it did not cause. The action is still worth showing either way.
    const withheld = disclosure === undefined || endpointRedacted(disclosure);
    const hidden = hiddenPolicyActionLabel[action];
    if (!hidden) return "";
    return withheld ? `${hidden} (policy hidden)` : hidden;
}

export function formatEndpoint(namespace: string, podName: string, ip: string): string {
    if (namespace && podName) {
        return `${namespace}/${podName}`;
    }
    return ip || "unknown";
}

/** Whether the Flow Aggregator withheld this endpoint's Kubernetes identity. Only the Flow tier
 * withholds anything this UI renders: the entire Full-over-Identity difference is Node placement
 * and the Antrea Egress, neither of which appears anywhere in the list, the map or the edge
 * details. So there are two visual states, not three - revisit only if a Node or Egress field is
 * ever surfaced. An absent marker reads as Full, which is the enum's zero value. */
export function endpointRedacted(disclosure: EndpointDisclosure | undefined): boolean {
    return disclosure === EndpointDisclosure.Flow;
}

/** Stands in for the workload name of an endpoint whose namespace survived redaction but whose
 * identity did not. */
export const HIDDEN_WORKLOAD_LABEL = "\u27e8hidden\u27e9";

/** One endpoint as a cell: the text to show, whether to mark it as withheld, and a tooltip if
 * there is an actionable one to give. */
export interface EndpointView {
    text: string;
    /** The peer's address, shown under `text`. formatEndpoint renders a name or an address and
     * never both, which left a withheld workload as "ns/<hidden>" and nothing else - a cell that
     * identifies nothing, since two peers in the same namespace then look identical. The address
     * is not redacted at any tier, so it is always available to fall back on. Carried on every
     * endpoint rather than only the withheld ones, so that a row does not change shape depending
     * on how much of it was disclosed. Unset where the address is already the text. */
    detail?: string;
    redacted: boolean;
    /** Set only where it is actionable, i.e. where there is a namespace to name. */
    tooltip?: string;
}

/**
 * How to render one endpoint of a flow.
 *
 * At the Flow tier the namespace survives only for an allowed connection: redactFlow clears it
 * as well when the connection was denied, to close a Pod-CIDR enumeration oracle. Those are the
 * two withheld forms, and both are marked, so a cell does not read as missing data and get filed
 * as a bug. The address is never redacted at any tier - redactFlow rewrites only the Kubernetes
 * sub-message - so there is always a label to fall back to.
 */
export function endpointView(
    disclosure: EndpointDisclosure | undefined,
    namespace: string,
    podName: string,
    ip: string,
    external = false,
): EndpointView {
    // An endpoint outside the cluster reaches the Flow tier too, because tierFor resolves an
    // endpoint with no namespace that way, but nothing was withheld from it: it never had a
    // Kubernetes identity to withhold. Upstream's fix for telling the two apart is the flow
    // type, which is never redacted. Marking it would claim something is hidden that is not.
    if (external || !endpointRedacted(disclosure)) {
        const text = formatEndpoint(namespace, podName, ip);
        // Only when the name is what is shown: formatEndpoint falls back to the address, and
        // repeating it under itself would be noise.
        return { text, detail: text === ip ? undefined : ip || undefined, redacted: false };
    }
    if (namespace) {
        return {
            text: `${namespace}/${HIDDEN_WORKLOAD_LABEL}`,
            detail: ip,
            redacted: true,
            tooltip: `Workload not shown. Seeing it requires get flows/identity on ${namespace}.`,
        };
    }
    // Nothing to name, so nothing actionable to say: no tooltip, just the address and the mark.
    return { text: ip || "unknown", redacted: true };
}

/** How to render the Dest Service cell. The destination Service fields follow the destination
 * endpoint's tier, so they are gone at the Flow tier - and an unexplained empty cell is exactly
 * the "looks like missing data" failure the mark exists to prevent. Its tooltip is its own rather
 * than the peer's: with a lock already in the Destination cell, a second unexplained one reads as
 * a duplicate rather than as a separately withheld field. */
export function destinationServiceView(
    disclosure: EndpointDisclosure | undefined,
    destinationServicePortName: string,
    external = false,
): EndpointView {
    const name = destinationK8sServiceFilterKey(destinationServicePortName) || destinationServicePortName;
    if (name) return { text: name, redacted: false };
    // See endpointView: an out-of-cluster destination is at the Flow tier but had no Service to
    // withhold, so its empty cell is genuinely empty rather than redacted.
    if (!external && endpointRedacted(disclosure)) {
        return {
            text: "",
            redacted: true,
            tooltip: "Service not shown. It belongs to the destination's namespace.",
        };
    }
    return { text: "-", redacted: false };
}


/**
 * Returns namespace/service with port suffix removed
 * (e.g. flow-demo-a/agnhost-server:http → flow-demo-a/agnhost-server).
 * Only returns values with namespace/service format, empty string otherwise.
 */
export function destinationK8sServiceFilterKey(destinationServicePortName: string): string {
    const s = destinationServicePortName.trim();
    if (!s) {
        return '';
    }
    // Only handle valid namespace/service format
    const slash = s.indexOf('/');
    if (slash <= 0) {
        return '';
    }
    const colon = s.indexOf(':', slash);
    if (colon >= 0) {
        return s.slice(0, colon);
    }
    return s;
}

export function formatBytes(bytes: number): string {
    if (bytes === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const value = bytes / Math.pow(1024, i);
    return `${value.toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

/**
 * Connection key for deduplication: 5-tuple with source port masked off.
 * Two flow records with the same key represent the same logical connection.
 */
export function connectionKey(flow: Flow): string {
    const srcIP = flow.ip.source;
    const dstIP = flow.ip.destination;
    const proto = flow.transport.protocolNumber;
    const dstPort = flow.transport.destinationPort;
    return `${srcIP}|${dstIP}|${proto}|${dstPort}`;
}
