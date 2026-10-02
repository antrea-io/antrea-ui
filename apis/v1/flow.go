// Copyright 2026 Antrea Authors
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

type FlowType int32

const (
	FlowTypeUnspecified  FlowType = 0
	FlowTypeIntraNode    FlowType = 1
	FlowTypeInterNode    FlowType = 2
	FlowTypeToExternal   FlowType = 3
	FlowTypeFromExternal FlowType = 4
)

type NetworkPolicyType int32

const (
	NetworkPolicyTypeUnspecified NetworkPolicyType = 0
	NetworkPolicyTypeK8s         NetworkPolicyType = 1
	NetworkPolicyTypeANP         NetworkPolicyType = 2
	NetworkPolicyTypeACNP        NetworkPolicyType = 3
)

// EndpointDisclosure records how much of one endpoint of a flow this user was authorized to see.
// Set by the Flow Aggregator's FlowStreamService only. Zero is Full, not "unspecified": a record
// nothing redacted reads as fully disclosed, which is every record on a cluster-wide stream.
type EndpointDisclosure int32

const (
	// EndpointDisclosureFull means everything the record carries for the endpoint, including
	// its Node placement and the Egress applied to it.
	EndpointDisclosureFull EndpointDisclosure = 0
	// EndpointDisclosureIdentity means the endpoint's Namespace, Pod and Service identity and
	// the identity of the network policy evaluated on its side, but not its Node placement or
	// its Egress.
	EndpointDisclosureIdentity EndpointDisclosure = 1
	// EndpointDisclosureFlow means only what the flow itself shows - addresses, ports,
	// protocol, statistics, and the type and action of the policies evaluated on the
	// endpoint's side - plus the endpoint's Namespace if the connection was allowed.
	EndpointDisclosureFlow EndpointDisclosure = 2
)

type NetworkPolicyRuleAction int32

const (
	NetworkPolicyRuleActionNoAction NetworkPolicyRuleAction = 0
	NetworkPolicyRuleActionAllow    NetworkPolicyRuleAction = 1
	NetworkPolicyRuleActionDrop     NetworkPolicyRuleAction = 2
	NetworkPolicyRuleActionReject   NetworkPolicyRuleAction = 3
)

type IPVersion int32

const (
	IPVersionUnspecified IPVersion = 0
	IPVersionIPv4        IPVersion = 4
	IPVersionIPv6        IPVersion = 6
)

type FlowEndReason int32

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
	Throughput       uint64 `json:"throughput,omitempty"`
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

	// SourceDisclosure and DestinationDisclosure report the tier each endpoint was disclosed
	// at, so a withheld field is distinguishable from a field the Flow Aggregator never had.
	//
	// Deliberately no omitempty on either: Full is the zero value, so omitempty would drop
	// exactly the case a client most needs to read as Full. Keeping them always present makes
	// the wire format say what it means. A client should still default a missing value to Full,
	// for records from a backend that predates these fields.
	//
	// Note that the marker describes an endpoint, not a per-field guarantee: an endpoint can
	// lack a field while still reporting Full, because the Flow Aggregator may simply never
	// have had it.
	SourceDisclosure      EndpointDisclosure `json:"sourceDisclosure"`
	DestinationDisclosure EndpointDisclosure `json:"destinationDisclosure"`

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

	DestinationClusterIp       string `json:"destinationClusterIp"`
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
	ID           string         `json:"id"`
	StartTs      string         `json:"startTs"`
	EndTs        string         `json:"endTs"`
	EndReason    FlowEndReason  `json:"endReason"`
	IP           FlowIP         `json:"ip"`
	Transport    FlowTransport  `json:"transport"`
	K8s          FlowKubernetes `json:"k8s"`
	Stats        FlowStats      `json:"stats"`
	ReverseStats FlowStats      `json:"reverseStats"`
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
	// Code is a stable, machine-readable identifier for the failure kind (see
	// pkg/handlers/flowstream.StreamError), so a client can decide how to react without parsing
	// Message. Empty for an error this backend could not classify.
	Code string `json:"code,omitempty"`
	// Retryable reports whether the same request is expected to succeed if retried. The
	// frontend uses this to decide whether to keep reconnecting or to stop and show Message.
	Retryable bool `json:"retryable"`
}

// FlowNamespaceAccess is one candidate Namespace and whether the user may observe flows in it.
//
// Both observable and non-observable candidates are reported, so the selector can show a
// Namespace it knows about as unavailable rather than silently omitting it — the difference
// between "you cannot observe flows there" and "that Namespace does not exist" is the one a user
// asks about.
type FlowNamespaceAccess struct {
	Namespace string `json:"namespace"`
	// CanObserve is the verdict of a SelfSubjectAccessReview for watch on
	// flows.observability.antrea.io in this Namespace. watch, not list: the flow stream always
	// follows, and the Flow Aggregator grants list alone as history-only access, so a
	// list-only Namespace would be offered here and then fail to stream.
	CanObserve bool `json:"canObserve"`
}

// FlowNamespacesResponse answers "which Namespaces may I observe flows in", which Kubernetes has
// no reverse lookup for: the Flow Aggregator requires every stream to name its scope, so the
// options have to be enumerated. Like AccessSummary this is a rendering hint — the Flow
// Aggregator authorizes every stream itself.
type FlowNamespacesResponse struct {
	// Namespaces is the candidate list with a verdict for each, sorted by name. Never null. An
	// empty list is a real answer: this user is a subject of no RoleBinding that would put a
	// Namespace within reach.
	Namespaces []FlowNamespaceAccess `json:"namespaces"`
	// ClusterWide is the verdict of one cluster-scoped review. It drives whether the selector
	// offers the cluster-wide option, which is the only scope in which nothing is redacted.
	ClusterWide bool `json:"clusterWide"`
	// Incomplete mirrors SubjectRulesReviewStatus.Incomplete: the candidate list is not
	// exhaustive, so a Namespace's absence from it does not mean the user cannot observe
	// flows there. It is set whenever the candidates were derived from RoleBinding subjects
	// rather than enumerated, and when the candidate list had to be truncated. Discovering
	// Namespaces a user may observe but may not list is not answerable; this is how they are
	// told.
	Incomplete bool `json:"incomplete"`
}
