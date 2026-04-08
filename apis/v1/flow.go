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

package v1

// JSON-serializable flow types for the SSE API, mirroring the protobuf Flow message.

type FlowType int

const (
	FlowTypeUnspecified  FlowType = 0
	FlowTypeIntraNode    FlowType = 1
	FlowTypeInterNode    FlowType = 2
	FlowTypeToExternal   FlowType = 3
	FlowTypeFromExternal FlowType = 4
)

type NetworkPolicyType int

const (
	NetworkPolicyTypeUnspecified NetworkPolicyType = 0
	NetworkPolicyTypeK8s        NetworkPolicyType = 1
	NetworkPolicyTypeANP        NetworkPolicyType = 2
	NetworkPolicyTypeACNP       NetworkPolicyType = 3
)

type NetworkPolicyRuleAction int

const (
	NetworkPolicyRuleActionNoAction NetworkPolicyRuleAction = 0
	NetworkPolicyRuleActionAllow    NetworkPolicyRuleAction = 1
	NetworkPolicyRuleActionDrop     NetworkPolicyRuleAction = 2
	NetworkPolicyRuleActionReject   NetworkPolicyRuleAction = 3
)

type IPVersion int

const (
	IPVersionUnspecified IPVersion = 0
	IPVersionIPv4       IPVersion = 4
	IPVersionIPv6       IPVersion = 6
)

type FlowEndReason int

const (
	FlowEndReasonUnspecified     FlowEndReason = 0
	FlowEndReasonIdleTimeout     FlowEndReason = 1
	FlowEndReasonActiveTimeout   FlowEndReason = 2
	FlowEndReasonEndOfFlow       FlowEndReason = 3
	FlowEndReasonForcedEnd       FlowEndReason = 4
	FlowEndReasonLackOfResources FlowEndReason = 5
)

type FlowStats struct {
	PacketTotalCount uint64 `json:"packetTotalCount"`
	PacketDeltaCount uint64 `json:"packetDeltaCount"`
	OctetTotalCount  uint64 `json:"octetTotalCount"`
	OctetDeltaCount  uint64 `json:"octetDeltaCount"`
}

type FlowTCP struct {
	StateName string `json:"stateName"`
}

type FlowTransport struct {
	ProtocolNumber  uint32   `json:"protocolNumber"`
	SourcePort      uint32   `json:"sourcePort"`
	DestinationPort uint32   `json:"destinationPort"`
	TCP             *FlowTCP `json:"tcp,omitempty"`
}

type FlowIP struct {
	Version     IPVersion `json:"version"`
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
}

type FlowKubernetes struct {
	FlowType FlowType `json:"flowType"`

	SourcePodNamespace string            `json:"sourcePodNamespace"`
	SourcePodName      string            `json:"sourcePodName"`
	SourcePodUid       string            `json:"sourcePodUid"`
	SourcePodLabels    map[string]string `json:"sourcePodLabels,omitempty"`

	SourceNodeName string `json:"sourceNodeName"`
	SourceNodeUid  string `json:"sourceNodeUid"`

	DestinationPodNamespace string            `json:"destinationPodNamespace"`
	DestinationPodName      string            `json:"destinationPodName"`
	DestinationPodUid       string            `json:"destinationPodUid"`
	DestinationPodLabels    map[string]string `json:"destinationPodLabels,omitempty"`

	DestinationNodeName string `json:"destinationNodeName"`
	DestinationNodeUid  string `json:"destinationNodeUid"`

	DestinationClusterIp      string `json:"destinationClusterIp"`
	DestinationServicePort     uint32 `json:"destinationServicePort"`
	DestinationServicePortName string `json:"destinationServicePortName"`
	DestinationServiceUid      string `json:"destinationServiceUid"`

	IngressNetworkPolicyType       NetworkPolicyType       `json:"ingressNetworkPolicyType"`
	IngressNetworkPolicyNamespace  string                  `json:"ingressNetworkPolicyNamespace"`
	IngressNetworkPolicyName       string                  `json:"ingressNetworkPolicyName"`
	IngressNetworkPolicyUid        string                  `json:"ingressNetworkPolicyUid"`
	IngressNetworkPolicyRuleName   string                  `json:"ingressNetworkPolicyRuleName"`
	IngressNetworkPolicyRuleAction NetworkPolicyRuleAction `json:"ingressNetworkPolicyRuleAction"`

	EgressNetworkPolicyType       NetworkPolicyType       `json:"egressNetworkPolicyType"`
	EgressNetworkPolicyNamespace  string                  `json:"egressNetworkPolicyNamespace"`
	EgressNetworkPolicyName       string                  `json:"egressNetworkPolicyName"`
	EgressNetworkPolicyUid        string                  `json:"egressNetworkPolicyUid"`
	EgressNetworkPolicyRuleName   string                  `json:"egressNetworkPolicyRuleName"`
	EgressNetworkPolicyRuleAction NetworkPolicyRuleAction `json:"egressNetworkPolicyRuleAction"`

	EgressName     string `json:"egressName,omitempty"`
	EgressIp       string `json:"egressIp,omitempty"`
	EgressNodeName string `json:"egressNodeName,omitempty"`
	EgressNodeUid  string `json:"egressNodeUid,omitempty"`
	EgressUid      string `json:"egressUid,omitempty"`
}

type Flow struct {
	ID           string          `json:"id"`
	StartTs      string          `json:"startTs"`
	EndTs        string          `json:"endTs"`
	EndReason    FlowEndReason   `json:"endReason"`
	IP           FlowIP          `json:"ip"`
	Transport    FlowTransport   `json:"transport"`
	K8s          FlowKubernetes  `json:"k8s"`
	Stats        FlowStats       `json:"stats"`
	ReverseStats FlowStats       `json:"reverseStats"`
}

// FlowStreamEvent carries flow data and/or a dropped count from the stream.
// When Flows is non-empty, the SSE handler emits a "flow" event.
// When DroppedCount is non-zero, the SSE handler emits a "dropped" event.
type FlowStreamEvent struct {
	Flows        []Flow `json:"flows,omitempty"`
	DroppedCount uint64 `json:"droppedCount,omitempty"`
}

// FlowStreamDroppedEvent is the JSON payload for an SSE "dropped" event.
type FlowStreamDroppedEvent struct {
	DroppedCount uint64 `json:"droppedCount"`
}

// FlowStreamErrorEvent is the JSON payload for an SSE "error" event.
type FlowStreamErrorEvent struct {
	Message string `json:"message"`
}

type FlowDirection int

const (
	FlowDirectionBoth FlowDirection = 0
	FlowDirectionFrom FlowDirection = 1
	FlowDirectionTo   FlowDirection = 2
)

// FlowStreamFilter represents the query parameters for the flow stream endpoint.
// All specified filters are AND-ed. Within each filter, values are OR-ed.
type FlowStreamFilter struct {
	Namespaces       []string      `json:"namespaces,omitempty"`
	PodNames         []string      `json:"podNames,omitempty"`
	PodLabelSelector string        `json:"podLabelSelector,omitempty"`
	ServiceNames     []string      `json:"serviceNames,omitempty"`
	FlowTypes        []FlowType    `json:"flowTypes,omitempty"`
	IPs              []string      `json:"ips,omitempty"`
	Direction        FlowDirection `json:"direction,omitempty"`
	Follow           bool          `json:"follow"`
}
