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

export interface Labels {
    labels: Record<string, string>;
}

export interface Kubernetes {
    flowType: FlowType;

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

export function formatPolicyInfo(name: string, action: NetworkPolicyRuleAction): string {
    if (!name) return "";
    const actionStr = networkPolicyRuleActionLabel[action];
    return actionStr ? `${name} (${actionStr})` : name;
}

export function formatEndpoint(namespace: string, podName: string, ip: string): string {
    if (namespace && podName) {
        return `${namespace}/${podName}`;
    }
    return ip || "unknown";
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
