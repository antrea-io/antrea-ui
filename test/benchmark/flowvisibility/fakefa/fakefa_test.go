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

package fakefa

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// prefill runs the generator for one ActiveTimeout of simulated time, without the real-time loop.
func prefill(t *testing.T, s Scale) *Generator {
	g, err := NewGenerator(s)
	require.NoError(t, err)
	start := time.Unix(1_800_000_000, 0)
	g.openInitialConnections(start)
	for at := start; at.Before(start.Add(s.ActiveTimeout)); at = at.Add(tick) {
		g.step(at, true)
	}
	return g
}

func TestGeneratorReportsEveryLongLivedConnection(t *testing.T) {
	s := Presets["tiny"]
	s.ShortConnectionRate = 0
	s.PodChurnRate = 0
	g := prefill(t, s)

	records, _, dropped, _ := g.ring.read(0, s.RingBufferSize)
	assert.Zero(t, dropped)
	assert.EqualValues(t, g.LiveConnections(), len(records), "one turn of the wheel reports each connection once")
	for _, r := range records {
		assert.NotEmpty(t, r.K8S.SourcePodName+r.Ip.String(), "every record has a source")
		if r.K8S.FlowType == flowpb.FlowType_FLOW_TYPE_INTRA_NODE || r.K8S.FlowType == flowpb.FlowType_FLOW_TYPE_INTER_NODE {
			assert.Regexp(t, `^ns-\d{3}/[a-z-0-9]+:http$`, r.K8S.DestinationServicePortName)
		}
	}
}

func TestGeneratorChurnReplacesConnections(t *testing.T) {
	s := Presets["tiny"]
	s.ShortConnectionRate = 0
	s.PodChurnRate = 600 // one Pod per tick
	g := prefill(t, s)

	records, _, _, _ := g.ring.read(0, s.RingBufferSize)
	var ended int
	for _, r := range records {
		if r.EndReason == flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW {
			ended++
		}
	}
	assert.Positive(t, ended, "connections of replaced Pods are reported as ended")
}

func TestGeneratorChurnKeepsConnectionCount(t *testing.T) {
	s := Presets["tiny"]
	s.ShortConnectionRate = 0
	s.PodChurnRate = 600 // one Pod per tick
	g, err := NewGenerator(s)
	require.NoError(t, err)
	at := time.Unix(1_800_000_000, 0)
	g.openInitialConnections(at)
	want := g.LiveConnections()
	// One turn of the wheel replacing Pods, then one without, by the end of which every
	// connection ended by a replacement has been reported.
	for _, churn := range []bool{true, false} {
		for end := at.Add(s.ActiveTimeout); at.Before(end); at = at.Add(tick) {
			g.step(at, churn)
		}
	}
	assert.Equal(t, want, g.LiveConnections(), "every connection a replacement ends is opened again once")
}

func TestRingDropsWhatWasOverwritten(t *testing.T) {
	r := newRing(4)
	for range 6 {
		r.append([]*flowpb.Flow{{}})
	}
	records, next, dropped, _ := r.read(0, 10)
	assert.Len(t, records, 4)
	assert.EqualValues(t, 6, next)
	assert.EqualValues(t, 2, dropped)
}

func TestMatcher(t *testing.T) {
	flow := &flowpb.Flow{
		Ip: &flowpb.IP{Source: []byte{10, 0, 0, 2}, Destination: []byte{10, 0, 1, 2}},
		K8S: &flowpb.Kubernetes{
			FlowType:                   flowpb.FlowType_FLOW_TYPE_INTER_NODE,
			SourcePodNamespace:         "ns-a",
			SourcePodName:              "web-1",
			SourcePodLabels:            &flowpb.Labels{Labels: map[string]string{"app": "web"}},
			DestinationPodNamespace:    "ns-b",
			DestinationPodName:         "db-1",
			DestinationServicePortName: "ns-b/db:http",
		},
	}
	for _, tc := range []struct {
		name string
		req  *flowpb.GetFlowsRequest
		want bool
	}{
		{"cluster-wide", &flowpb.GetFlowsRequest{ClusterWide: true}, true},
		{"source Namespace in scope", &flowpb.GetFlowsRequest{Namespaces: []string{"ns-a"}}, true},
		{"destination Namespace in scope", &flowpb.GetFlowsRequest{Namespaces: []string{"ns-b"}}, true},
		{"Namespace out of scope", &flowpb.GetFlowsRequest{Namespaces: []string{"ns-c"}}, false},
		{"service", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{ServiceNames: []string{"db"}}}}, true},
		{"other service", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{ServiceNames: []string{"web"}}}}, false},
		{"service with its Namespace", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{ServiceNames: []string{"ns-b/db"}}}}, false},
		{"label on source", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{PodLabelSelector: "app=web"}}}, true},
		{"label to destination", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{
			PodLabelSelector: "app=web", Direction: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO,
		}}}, false},
		{"peer Namespace from", &flowpb.GetFlowsRequest{Namespaces: []string{"ns-b"}, Filters: []*flowpb.FlowFilter{{
			Namespaces: []string{"ns-a"}, Direction: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
		}}}, true},
		{"IP prefix", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{Ips: []string{"10.0.1.0/24"}}}}, true},
		{"flow type", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{
			FlowTypes: []flowpb.FlowType{flowpb.FlowType_FLOW_TYPE_INTRA_NODE},
		}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := newMatcher(tc.req)
			require.NoError(t, err)
			assert.Equal(t, tc.want, m.match(flow))
		})
	}
}
