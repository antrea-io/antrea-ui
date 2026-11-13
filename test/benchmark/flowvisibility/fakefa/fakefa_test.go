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
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

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
		assert.Len(t, r.Ip.Source, 4, "every record has a source address")
		if r.K8S.FlowType != flowpb.FlowType_FLOW_TYPE_FROM_EXTERNAL {
			assert.NotEmpty(t, r.K8S.SourcePodName, "a record not from outside the cluster has a source Pod")
		}
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

func TestGeneratorEndedConnectionKeepsItsEndpoints(t *testing.T) {
	s := Presets["tiny"]
	s.ShortConnectionRate = 0
	s.PodChurnRate = 600 // one Pod per tick
	g, err := NewGenerator(s)
	require.NoError(t, err)
	at := time.Unix(1_800_000_000, 0)
	g.openInitialConnections(at)
	// Two turns of the wheel, so that a connection ended in the second was reported in the first.
	for end := at.Add(2 * s.ActiveTimeout); at.Before(end); at = at.Add(tick) {
		g.step(at, true)
	}

	type endpoints struct {
		srcIP, dstIP                   string
		srcPod, srcUID, dstPod, dstUID string
		srcNode, dstNode, srcNS, dstNS string
	}
	of := func(r *flowpb.Flow) endpoints {
		k := r.K8S
		return endpoints{
			string(r.Ip.Source), string(r.Ip.Destination),
			k.SourcePodName, k.SourcePodUid, k.DestinationPodName, k.DestinationPodUid,
			k.SourceNodeName, k.DestinationNodeName, k.SourcePodNamespace, k.DestinationPodNamespace,
		}
	}
	records, _, dropped, _ := g.ring.read(0, s.RingBufferSize)
	require.Zero(t, dropped)
	first := map[string]endpoints{}
	var checked int
	for _, r := range records {
		e, seen := first[r.Id]
		if !seen {
			first[r.Id] = of(r)
			continue
		}
		if r.EndReason == flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW {
			checked++
		}
		assert.Equal(t, e, of(r), "the records of connection %s report the same endpoints", r.Id)
	}
	assert.Positive(t, checked, "some connections reported before being ended by a replacement")
}

func TestReplacedPodGetsAnAddressNotInUse(t *testing.T) {
	s := Presets["tiny"]
	s.PodsPerNode = podHostsPerNode - 1 // a single free address per Node
	g, err := NewGenerator(s)
	require.NoError(t, err)
	c := g.cluster
	for range 4 * podHostsPerNode * s.Nodes {
		i := g.r.IntN(len(c.pods))
		old := c.pods[i]
		p := c.replacePod(i, g.r)
		require.NotEqual(t, old.ip, p.ip, "a replacement gets a new address")
		holders := 0
		for _, q := range c.pods {
			if q.ip == p.ip {
				holders++
			}
		}
		require.Equal(t, 1, holders, "%s is held by a single live Pod", p.ip)
	}
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
		{"service with its Namespace", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{ServiceNames: []string{"ns-b/db"}}}}, true},
		{"service in another Namespace", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{ServiceNames: []string{"ns-a/db"}}}}, false},
		{"Pod", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{PodNames: []string{"db-1"}}}}, true},
		{"Pod with its Namespace", &flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{PodNames: []string{"ns-b/db-1"}}}}, false},
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

func TestMatcherRejectsFromWithServiceNames(t *testing.T) {
	_, err := newMatcher(&flowpb.GetFlowsRequest{ClusterWide: true, Filters: []*flowpb.FlowFilter{{
		ServiceNames: []string{"db"}, Direction: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
	}}})
	assert.Error(t, err)
}

// testRecords is how many records newTestServer appends, to a ring buffer of testRingSize: the
// ring holds sequence numbers 5 to 14, and the next record appended would be 15.
const (
	testRecords  = 15
	testRingSize = 10
)

var testStart = time.Unix(1_800_000_000, 0)

// newTestServer returns a Server whose ring holds records "0" to "14", each ending a second after
// the previous one, of which "0" to "4" were evicted. Responses carry at most 4 records.
func newTestServer(t *testing.T) *Server {
	s := Presets["tiny"]
	s.RingBufferSize = testRingSize
	s.MaxBatchSize = 4
	g, err := NewGenerator(s)
	require.NoError(t, err)
	for i := range testRecords {
		g.ring.append([]*flowpb.Flow{{
			Id:    strconv.Itoa(i),
			EndTs: timestamppb.New(testStart.Add(time.Duration(i) * time.Second)),
			Ip:    &flowpb.IP{},
			K8S:   &flowpb.Kubernetes{},
		}})
	}
	return NewServer(g)
}

type recordingStream struct {
	grpc.ServerStream
	responses []*flowpb.GetFlowsResponse
}

func (s *recordingStream) Context() context.Context { return context.Background() }

func (s *recordingStream) Send(r *flowpb.GetFlowsResponse) error {
	s.responses = append(s.responses, r)
	return nil
}

type streamResult struct {
	ids              []string
	first, last      int64 // the sequence numbers of the first and last resume tokens
	dropped          uint64
	firstEpochIsOurs bool
}

func getFlows(t *testing.T, srv *Server, req *flowpb.GetFlowsRequest) (streamResult, error) {
	stream := &recordingStream{}
	if err := srv.GetFlows(req, stream); err != nil {
		return streamResult{}, err
	}
	require.NotEmpty(t, stream.responses)
	first, last := stream.responses[0], stream.responses[len(stream.responses)-1]
	assert.Empty(t, first.Flows, "the first response carries no flows")
	res := streamResult{
		first:            first.ResumeToken.SequenceNumber,
		last:             last.ResumeToken.SequenceNumber,
		dropped:          last.DroppedCount,
		firstEpochIsOurs: first.ResumeToken.StreamEpoch == srv.epoch,
	}
	for _, r := range stream.responses {
		for _, f := range r.Flows {
			res.ids = append(res.ids, f.Id)
		}
	}
	return res, nil
}

// ids returns the record IDs from to to, both included.
func ids(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, strconv.Itoa(i))
	}
	return out
}

func TestGetFlowsResume(t *testing.T) {
	srv := newTestServer(t)
	resume := func(epoch string, seq int64) *flowpb.ResumeToken {
		return &flowpb.ResumeToken{StreamEpoch: epoch, SequenceNumber: seq}
	}
	for _, tc := range []struct {
		name    string
		resume  *flowpb.ResumeToken
		want    streamResult
		wantErr bool
	}{
		{
			name: "no resume starts at the oldest record",
			want: streamResult{ids: ids(5, 14), first: 4, last: 14},
		},
		{
			name:   "matching epoch continues after the token",
			resume: resume(srv.epoch, 9),
			want:   streamResult{ids: ids(10, 14), first: 9, last: 14},
		},
		{
			name:   "matching epoch at the last record issued sends nothing",
			resume: resume(srv.epoch, 14),
			want:   streamResult{first: 14, last: 14},
		},
		{
			name:   "evicted resume point starts at the oldest record and reports the gap",
			resume: resume(srv.epoch, 2),
			want:   streamResult{ids: ids(5, 14), first: 2, last: 14, dropped: 2},
		},
		{
			name:   "-1 is the lowest token issued",
			resume: resume(srv.epoch, -1),
			want:   streamResult{ids: ids(5, 14), first: -1, last: 14, dropped: 5},
		},
		{
			name:   "other epoch is treated as no resume",
			resume: resume("other", 9),
			want:   streamResult{ids: ids(5, 14), first: 4, last: 14},
		},
		{
			name:    "matching epoch at the next sequence number is rejected",
			resume:  resume(srv.epoch, testRecords),
			wantErr: true,
		},
		{
			name:    "matching epoch below -1 is rejected",
			resume:  resume(srv.epoch, -2),
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getFlows(t, srv, &flowpb.GetFlowsRequest{ClusterWide: true, Resume: tc.resume})
			if tc.wantErr {
				assert.Equal(t, codes.InvalidArgument, status.Code(err))
				return
			}
			require.NoError(t, err)
			tc.want.firstEpochIsOurs = true
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGetFlowsMaxCount(t *testing.T) {
	srv := newTestServer(t)
	got, err := getFlows(t, srv, &flowpb.GetFlowsRequest{ClusterWide: true, MaxCount: 3})
	require.NoError(t, err)
	assert.Equal(t, ids(5, 7), got.ids, "max_count is a limit within a response too")
	assert.EqualValues(t, 7, got.last, "the resume token stops at the last record sent")

	got, err = getFlows(t, srv, &flowpb.GetFlowsRequest{
		ClusterWide: true, Resume: &flowpb.ResumeToken{StreamEpoch: srv.epoch, SequenceNumber: got.last},
	})
	require.NoError(t, err)
	assert.Equal(t, ids(8, 14), got.ids, "resuming from it misses nothing")
}

func TestGetFlowsAdvancesResumeTokenPastFilteredRecords(t *testing.T) {
	srv := newTestServer(t)
	stream := &recordingStream{}
	require.NoError(t, srv.GetFlows(&flowpb.GetFlowsRequest{
		ClusterWide: true, Filters: []*flowpb.FlowFilter{{Namespaces: []string{"none"}}},
	}, stream))
	var tokens []int64
	for _, r := range stream.responses {
		assert.Empty(t, r.Flows)
		tokens = append(tokens, r.ResumeToken.SequenceNumber)
	}
	// Batches end at 8, 12 and 14. The token is carried forward once 5 records (half the ring)
	// behind, at 12, and the stream ends at the last record.
	assert.Equal(t, []int64{4, 12, 14}, tokens)

	// A ring's worth of new records evicts everything the stream examined.
	for i := range testRingSize {
		srv.gen.ring.append([]*flowpb.Flow{{Id: strconv.Itoa(testRecords + i), Ip: &flowpb.IP{}, K8S: &flowpb.Kubernetes{}}})
	}
	got, err := getFlows(t, srv, &flowpb.GetFlowsRequest{
		ClusterWide: true, Resume: &flowpb.ResumeToken{StreamEpoch: srv.epoch, SequenceNumber: tokens[len(tokens)-1]},
	})
	require.NoError(t, err)
	assert.Zero(t, got.dropped, "resuming from the last token reports nothing dropped")
}

func TestGetFlowsSince(t *testing.T) {
	srv := newTestServer(t)
	got, err := getFlows(t, srv, &flowpb.GetFlowsRequest{ClusterWide: true, Since: timestamppb.New(testStart.Add(12 * time.Second))})
	require.NoError(t, err)
	assert.Equal(t, ids(12, 14), got.ids, "records ending at or after since")
}
