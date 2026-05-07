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

package flowstream

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	flowpb "antrea.io/antrea-ui/pkg/apis/flow/v1alpha1"
)

// ---------------------------------------------------------------------------
// ipBytesToString
// ---------------------------------------------------------------------------

func TestIPBytesToString(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "nil slice returns empty string",
			input:    nil,
			expected: "",
		},
		{
			name:     "empty slice returns empty string",
			input:    []byte{},
			expected: "",
		},
		{
			name:     "IPv4 4-byte slice",
			input:    net.ParseIP("10.0.0.1").To4(),
			expected: "10.0.0.1",
		},
		{
			name:     "IPv4 mapped as 16-byte slice",
			input:    net.ParseIP("192.168.1.100").To16(),
			expected: "::ffff:192.168.1.100",
		},
		{
			name:     "IPv6 16-byte slice",
			input:    net.ParseIP("fd00::1"),
			expected: "fd00::1",
		},
		{
			name:     "invalid bytes returns error placeholder",
			input:    []byte{0xde, 0xad},
			expected: "<invalid-ip:dead>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipBytesToString(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// ---------------------------------------------------------------------------
// filterToGetFlowsRequest
// ---------------------------------------------------------------------------

func TestFilterToGetFlowsRequest(t *testing.T) {
	tests := []struct {
		name           string
		filter         *apisv1.FlowStreamFilter
		wantDirection  flowpb.FlowFilterDirection
		wantNamespaces []string
		wantPodNames   []string
		wantServices   []string
		wantIPs        []string
		wantLabelSel   string
		wantFlowTypes  []flowpb.FlowType
		wantFollow     bool
	}{
		{
			name:          "empty filter maps to BOTH direction; gRPC follow is always true",
			filter:        &apisv1.FlowStreamFilter{},
			wantDirection: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_BOTH,
			wantFollow:    true,
		},
		{
			name: "direction FROM",
			filter: &apisv1.FlowStreamFilter{
				Direction: apisv1.FlowFilterDirectionFrom,
				Follow:    true,
			},
			wantDirection: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
			wantFollow:    true,
		},
		{
			name: "direction TO",
			filter: &apisv1.FlowStreamFilter{
				Direction: apisv1.FlowFilterDirectionTo,
			},
			wantDirection: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO,
			wantFollow:    true,
		},
		{
			name: "direction BOTH explicit",
			filter: &apisv1.FlowStreamFilter{
				Direction: apisv1.FlowFilterDirectionBoth,
			},
			wantDirection: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_BOTH,
			wantFollow:    true,
		},
		{
			name: "all filter fields populated",
			filter: &apisv1.FlowStreamFilter{
				Namespaces:       []string{"default", "kube-system"},
				PodNames:         []string{"pod-a", "pod-b"},
				PodLabelSelector: "app=frontend",
				ServiceNames:     []string{"svc-a"},
				IPs:              []string{"10.0.0.1", "10.0.0.0/24"},
				FlowTypes:        []apisv1.FlowType{apisv1.FlowTypeIntraNode, apisv1.FlowTypeInterNode},
				Direction:        apisv1.FlowFilterDirectionFrom,
				Follow:           true,
			},
			wantDirection:  flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
			wantNamespaces: []string{"default", "kube-system"},
			wantPodNames:   []string{"pod-a", "pod-b"},
			wantServices:   []string{"svc-a"},
			wantIPs:        []string{"10.0.0.1", "10.0.0.0/24"},
			wantLabelSel:   "app=frontend",
			wantFlowTypes:  []flowpb.FlowType{flowpb.FlowType_FLOW_TYPE_INTRA_NODE, flowpb.FlowType_FLOW_TYPE_INTER_NODE},
			wantFollow:     true,
		},
		{
			name: "Follow false on filter is overridden — SSE always streams",
			filter: &apisv1.FlowStreamFilter{
				Follow: false,
			},
			wantDirection: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_BOTH,
			wantFollow:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := filterToGetFlowsRequest(tt.filter)
			require.NotNil(t, req)
			require.NotNil(t, req.Filter)

			assert.Equal(t, tt.wantDirection, req.Filter.Direction)
			assert.Equal(t, tt.wantNamespaces, req.Filter.Namespaces)
			assert.Equal(t, tt.wantPodNames, req.Filter.PodNames)
			assert.Equal(t, tt.wantServices, req.Filter.ServiceNames)
			assert.Equal(t, tt.wantIPs, req.Filter.Ips)
			assert.Equal(t, tt.wantLabelSel, req.Filter.PodLabelSelector)
			assert.Equal(t, tt.wantFlowTypes, req.Filter.FlowTypes)
			assert.Equal(t, tt.wantFollow, req.Follow)
		})
	}
}

// ---------------------------------------------------------------------------
// protoFlowToAPI
// ---------------------------------------------------------------------------

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestProtoFlowToAPI_MinimalFlow(t *testing.T) {
	startTime := mustParseTime("2026-03-25T10:00:00Z")
	endTime := mustParseTime("2026-03-25T10:01:00Z")

	pb := &flowpb.Flow{
		Id:        "flow-abc-123",
		StartTs:   timestamppb.New(startTime),
		EndTs:     timestamppb.New(endTime),
		EndReason: flowpb.FlowEndReason_FLOW_END_REASON_IDLE_TIMEOUT,
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, "flow-abc-123", got.ID)
	assert.Equal(t, startTime.UTC().Format(time.RFC3339Nano), got.StartTs)
	assert.Equal(t, endTime.UTC().Format(time.RFC3339Nano), got.EndTs)
	assert.Equal(t, apisv1.FlowEndReasonIdleTimeout, got.EndReason)
	// nil sub-messages produce zero values, not panics
	assert.Empty(t, got.IP.Source)
	assert.Empty(t, got.IP.Destination)
	assert.Nil(t, got.Transport.TCP)
}

func TestProtoFlowToAPI_IPv4Addresses(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-ipv4",
		Ip: &flowpb.IP{
			Version:     flowpb.IPVersion_IP_VERSION_4,
			Source:      net.ParseIP("10.0.0.1").To4(),
			Destination: net.ParseIP("10.0.0.2").To4(),
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, apisv1.IPVersionIPv4, got.IP.Version)
	assert.Equal(t, "10.0.0.1", got.IP.Source)
	assert.Equal(t, "10.0.0.2", got.IP.Destination)
}

func TestProtoFlowToAPI_IPv6Addresses(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-ipv6",
		Ip: &flowpb.IP{
			Version:     flowpb.IPVersion_IP_VERSION_6,
			Source:      net.ParseIP("fd00::1"),
			Destination: net.ParseIP("fd00::2"),
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, apisv1.IPVersionIPv6, got.IP.Version)
	assert.Equal(t, "fd00::1", got.IP.Source)
	assert.Equal(t, "fd00::2", got.IP.Destination)
}

func TestProtoFlowToAPI_Transport(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-transport",
		Transport: &flowpb.Transport{
			ProtocolNumber:  6,
			SourcePort:      54321,
			DestinationPort: 80,
			Protocol: &flowpb.Transport_TCP{
				TCP: &flowpb.TCP{StateName: "ESTABLISHED"},
			},
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, uint32(6), got.Transport.ProtocolNumber)
	assert.Equal(t, uint32(54321), got.Transport.SourcePort)
	assert.Equal(t, uint32(80), got.Transport.DestinationPort)
	require.NotNil(t, got.Transport.TCP)
	assert.Equal(t, "ESTABLISHED", got.Transport.TCP.StateName)
}

func TestProtoFlowToAPI_TransportNoTCP(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-udp",
		Transport: &flowpb.Transport{
			ProtocolNumber:  17,
			SourcePort:      5000,
			DestinationPort: 53,
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, uint32(17), got.Transport.ProtocolNumber)
	assert.Nil(t, got.Transport.TCP)
}

func TestProtoFlowToAPI_Kubernetes(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-k8s",
		K8S: &flowpb.Kubernetes{
			FlowType:                       flowpb.FlowType_FLOW_TYPE_INTER_NODE,
			SourcePodNamespace:             "default",
			SourcePodName:                  "frontend-abc",
			SourcePodUid:                   "uid-src",
			SourcePodLabels:                &flowpb.Labels{Labels: map[string]string{"app": "frontend"}},
			SourceNodeName:                 "node-1",
			DestinationPodNamespace:        "default",
			DestinationPodName:             "backend-xyz",
			DestinationPodUid:              "uid-dst",
			DestinationPodLabels:           &flowpb.Labels{Labels: map[string]string{"app": "backend"}},
			DestinationNodeName:            "node-2",
			DestinationClusterIp:           net.ParseIP("10.96.0.10").To4(),
			DestinationServicePort:         8080,
			DestinationServicePortName:     "http",
			DestinationServiceUid:          "svc-uid",
			IngressNetworkPolicyType:       flowpb.NetworkPolicyType_NETWORK_POLICY_TYPE_K8S,
			IngressNetworkPolicyNamespace:  "default",
			IngressNetworkPolicyName:       "allow-ingress",
			IngressNetworkPolicyRuleAction: flowpb.NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_ALLOW,
			EgressNetworkPolicyType:        flowpb.NetworkPolicyType_NETWORK_POLICY_TYPE_ANP,
			EgressNetworkPolicyName:        "egress-policy",
			EgressNetworkPolicyRuleAction:  flowpb.NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_DROP,
		},
	}

	got := protoFlowToAPI(pb)
	k := got.K8s

	assert.Equal(t, apisv1.FlowTypeInterNode, k.FlowType)
	assert.Equal(t, "default", k.SourcePodNamespace)
	assert.Equal(t, "frontend-abc", k.SourcePodName)
	assert.Equal(t, map[string]string{"app": "frontend"}, k.SourcePodLabels)
	assert.Equal(t, "node-1", k.SourceNodeName)
	assert.Equal(t, "default", k.DestinationPodNamespace)
	assert.Equal(t, "backend-xyz", k.DestinationPodName)
	assert.Equal(t, map[string]string{"app": "backend"}, k.DestinationPodLabels)
	assert.Equal(t, "node-2", k.DestinationNodeName)
	assert.Equal(t, "10.96.0.10", k.DestinationClusterIp)
	assert.Equal(t, uint32(8080), k.DestinationServicePort)
	assert.Equal(t, "http", k.DestinationServicePortName)
	assert.Equal(t, apisv1.NetworkPolicyTypeK8s, k.IngressNetworkPolicyType)
	assert.Equal(t, "default", k.IngressNetworkPolicyNamespace)
	assert.Equal(t, "allow-ingress", k.IngressNetworkPolicyName)
	assert.Equal(t, apisv1.NetworkPolicyRuleActionAllow, k.IngressNetworkPolicyRuleAction)
	assert.Equal(t, apisv1.NetworkPolicyTypeANP, k.EgressNetworkPolicyType)
	assert.Equal(t, "egress-policy", k.EgressNetworkPolicyName)
	assert.Equal(t, apisv1.NetworkPolicyRuleActionDrop, k.EgressNetworkPolicyRuleAction)
}

func TestProtoFlowToAPI_KubernetesNilPodLabels(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-nil-labels",
		K8S: &flowpb.Kubernetes{
			SourcePodNamespace: "default",
			SourcePodName:      "pod-a",
			// SourcePodLabels intentionally nil
			DestinationPodNamespace: "default",
			DestinationPodName:      "pod-b",
			// DestinationPodLabels intentionally nil
		},
	}

	got := protoFlowToAPI(pb)

	// nil Labels message should produce a nil map, not a panic
	assert.Nil(t, got.K8s.SourcePodLabels)
	assert.Nil(t, got.K8s.DestinationPodLabels)
}

func TestProtoFlowToAPI_EgressIPBytes(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-egress",
		K8S: &flowpb.Kubernetes{
			EgressName:     "egress-rule-1",
			EgressIp:       net.ParseIP("172.16.0.1").To4(),
			EgressNodeName: "node-egress",
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, "egress-rule-1", got.K8s.EgressName)
	assert.Equal(t, "172.16.0.1", got.K8s.EgressIp)
	assert.Equal(t, "node-egress", got.K8s.EgressNodeName)
}

func TestProtoFlowToAPI_Stats(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-stats",
		Stats: &flowpb.Stats{
			PacketTotalCount: 100,
			PacketDeltaCount: 10,
			OctetTotalCount:  50000,
			OctetDeltaCount:  5000,
		},
		ReverseStats: &flowpb.Stats{
			PacketTotalCount: 80,
			PacketDeltaCount: 8,
			OctetTotalCount:  40000,
			OctetDeltaCount:  4000,
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, uint64(100), got.Stats.PacketTotalCount)
	assert.Equal(t, uint64(10), got.Stats.PacketDeltaCount)
	assert.Equal(t, uint64(50000), got.Stats.OctetTotalCount)
	assert.Equal(t, uint64(5000), got.Stats.OctetDeltaCount)
	assert.Equal(t, uint64(80), got.ReverseStats.PacketTotalCount)
	assert.Equal(t, uint64(8), got.ReverseStats.PacketDeltaCount)
	assert.Equal(t, uint64(40000), got.ReverseStats.OctetTotalCount)
	assert.Equal(t, uint64(4000), got.ReverseStats.OctetDeltaCount)
}

func TestProtoFlowToAPI_NilStatsAreZero(t *testing.T) {
	pb := &flowpb.Flow{
		Id: "flow-nil-stats",
		// Stats and ReverseStats intentionally nil
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, apisv1.FlowStats{}, got.Stats)
	assert.Equal(t, apisv1.FlowStats{}, got.ReverseStats)
}

func TestProtoFlowToAPI_FullFlow(t *testing.T) {
	startTime := mustParseTime("2026-03-25T10:00:00Z")
	endTime := mustParseTime("2026-03-25T10:01:00Z")

	pb := &flowpb.Flow{
		Id:        "flow-full",
		StartTs:   timestamppb.New(startTime),
		EndTs:     timestamppb.New(endTime),
		EndReason: flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW,
		Ip: &flowpb.IP{
			Version:     flowpb.IPVersion_IP_VERSION_4,
			Source:      net.ParseIP("10.1.0.1").To4(),
			Destination: net.ParseIP("10.2.0.1").To4(),
		},
		Transport: &flowpb.Transport{
			ProtocolNumber:  6,
			SourcePort:      12345,
			DestinationPort: 443,
			Protocol: &flowpb.Transport_TCP{
				TCP: &flowpb.TCP{StateName: "TIME_WAIT"},
			},
		},
		K8S: &flowpb.Kubernetes{
			FlowType:                flowpb.FlowType_FLOW_TYPE_INTRA_NODE,
			SourcePodNamespace:      "prod",
			SourcePodName:           "web-pod",
			DestinationPodNamespace: "prod",
			DestinationPodName:      "db-pod",
			DestinationClusterIp:    net.ParseIP("10.96.1.1").To4(),
		},
		Stats: &flowpb.Stats{
			PacketTotalCount: 200,
			OctetTotalCount:  100000,
		},
		ReverseStats: &flowpb.Stats{
			PacketTotalCount: 150,
			OctetTotalCount:  75000,
		},
	}

	got := protoFlowToAPI(pb)

	assert.Equal(t, "flow-full", got.ID)
	assert.Equal(t, apisv1.FlowEndReasonEndOfFlow, got.EndReason)
	assert.Equal(t, "10.1.0.1", got.IP.Source)
	assert.Equal(t, "10.2.0.1", got.IP.Destination)
	assert.Equal(t, "TIME_WAIT", got.Transport.TCP.StateName)
	assert.Equal(t, "10.96.1.1", got.K8s.DestinationClusterIp)
	assert.Equal(t, uint64(200), got.Stats.PacketTotalCount)
	assert.Equal(t, uint64(150), got.ReverseStats.PacketTotalCount)
}
