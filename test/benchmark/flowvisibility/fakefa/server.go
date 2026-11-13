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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/labels"

	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// Server implements FlowStreamService over a Generator's ring buffer.
//
// It follows the FlowStreamService contract for what a request asks: exactly one scope, filters,
// since, max_count, follow, and resume tokens, with dropped records reported. It does not
// authenticate or authorize the caller, and discloses every endpoint in full, which is what a
// caller allowed to observe flows in every Namespace would get.
type Server struct {
	flowpb.UnimplementedFlowStreamServiceServer
	gen   *Generator
	epoch string

	streamsOpen atomic.Int64
	recordsSent atomic.Uint64
	bytesSent   atomic.Uint64
}

func NewServer(gen *Generator) *Server {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &Server{gen: gen, epoch: hex.EncodeToString(b)}
}

// Stats is what the server reports about itself, for the benchmark's results.
type Stats struct {
	StreamsOpen      int64  `json:"streamsOpen"`
	RecordsGenerated uint64 `json:"recordsGenerated"`
	RecordsSent      uint64 `json:"recordsSent"`
	BytesSent        uint64 `json:"bytesSent"`
	LiveConnections  int64  `json:"liveConnections"`
	// Summary and Scale describe the run as a whole, rather than its progress.
	Summary string `json:"summary,omitempty"`
	Scale   *Scale `json:"scale,omitempty"`
}

func (s *Server) Stats() Stats {
	return Stats{
		StreamsOpen:      s.streamsOpen.Load(),
		RecordsGenerated: s.gen.RecordsTotal(),
		RecordsSent:      s.recordsSent.Load(),
		BytesSent:        s.bytesSent.Load(),
		LiveConnections:  s.gen.LiveConnections(),
		Summary:          s.gen.Summary(),
		Scale:            &s.gen.scale,
	}
}

func (s *Server) GetFlows(req *flowpb.GetFlowsRequest, stream flowpb.FlowStreamService_GetFlowsServer) error {
	if req.ClusterWide == (len(req.Namespaces) > 0) {
		return status.Error(codes.InvalidArgument, "exactly one of cluster_wide and namespaces must be set")
	}
	if len(req.Namespaces) > 1 || (len(req.Namespaces) == 1 && req.Namespaces[0] == "") {
		return status.Error(codes.InvalidArgument, "at most one non-empty Namespace may be named")
	}
	m, err := newMatcher(req)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// The stream starts after the resume point, or at the oldest record held. A token from another
	// epoch is treated as no token: the ring buffer it pointed into is gone. A resume point already
	// evicted is reported as dropped by the first read.
	ring := s.gen.ring
	from := ring.oldest()
	if t := req.Resume; t != nil && t.StreamEpoch == s.epoch {
		if t.SequenceNumber < -1 || t.SequenceNumber >= ring.nextSequence() {
			return status.Errorf(codes.InvalidArgument, "resume sequence_number %d was not issued by this server", t.SequenceNumber)
		}
		from = t.SequenceNumber + 1
	}

	s.streamsOpen.Add(1)
	defer s.streamsOpen.Add(-1)

	var droppedTotal uint64
	// lastToken is the sequence number of the last resume token sent.
	lastToken := from - 1
	send := func(flows []*flowpb.Flow) error {
		resp := &flowpb.GetFlowsResponse{
			Flows:        flows,
			DroppedCount: droppedTotal,
			ResumeToken:  &flowpb.ResumeToken{StreamEpoch: s.epoch, SequenceNumber: from - 1},
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
		lastToken = from - 1
		s.recordsSent.Add(uint64(len(flows)))
		s.bytesSent.Add(uint64(proto.Size(resp))) // #nosec G115: a size is never negative
		return nil
	}
	if err := send(nil); err != nil {
		return err
	}

	// When the filters remove every record of a batch, there is nothing to send, but the client's
	// resume point falls behind, and resuming from it would report the records filtered out as
	// dropped. Once it is behind by half the ring buffer, a response with no flows carries it
	// forward, and a stream that is not followed ends with one carrying the final position.
	tokenRefreshSpan := max(int64(s.gen.scale.RingBufferSize)/2, 1)
	maxBatch := s.gen.scale.MaxBatchSize
	maxCount := uint64(req.MaxCount)
	var sent uint64
	for {
		records, next, dropped, notify := ring.read(from, maxBatch)
		first := next - int64(len(records))
		from = next
		droppedTotal += dropped
		var out []*flowpb.Flow
		for i, r := range records {
			if !m.match(r) {
				continue
			}
			out = append(out, r)
			if maxCount > 0 && sent+uint64(len(out)) == maxCount {
				// The rest of the batch is not accounted for: the resume token stops at the last
				// record sent.
				from = first + int64(i) + 1
				break
			}
		}
		if len(out) > 0 || dropped > 0 || from-1-lastToken >= tokenRefreshSpan {
			if err := send(out); err != nil {
				return err
			}
			sent += uint64(len(out))
		}
		if maxCount > 0 && sent >= maxCount {
			return nil
		}
		if len(records) < maxBatch {
			if !req.Follow {
				if from-1 > lastToken {
					return send(nil)
				}
				return nil
			}
			select {
			case <-stream.Context().Done():
				return nil
			case <-notify:
			}
		}
	}
}

// matcher applies a request's scope and filters to a record.
type matcher struct {
	namespace string    // the scope, or "" for cluster-wide
	since     time.Time // zero when unset
	filters   []filter
}

type filter struct {
	namespaces []string
	pods       []string
	selector   labels.Selector
	services   []string
	flowTypes  []flowpb.FlowType
	prefixes   []netip.Prefix
	direction  flowpb.FlowFilterDirection
}

func newMatcher(req *flowpb.GetFlowsRequest) (*matcher, error) {
	m := &matcher{}
	if len(req.Namespaces) == 1 {
		m.namespace = req.Namespaces[0]
	}
	if req.Since != nil {
		m.since = req.Since.AsTime()
	}
	for _, f := range req.Filters {
		if f.Direction == flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM && len(f.ServiceNames) > 0 {
			return nil, errors.New("a filter cannot combine direction FROM with service_names")
		}
		pf := filter{
			namespaces: f.Namespaces,
			pods:       f.PodNames,
			services:   f.ServiceNames,
			flowTypes:  f.FlowTypes,
			direction:  f.Direction,
		}
		if f.PodLabelSelector != "" {
			sel, err := labels.Parse(f.PodLabelSelector)
			if err != nil {
				return nil, err
			}
			pf.selector = sel
		}
		for _, ip := range f.Ips {
			p, err := netip.ParsePrefix(ip)
			if err != nil {
				a, aerr := netip.ParseAddr(ip)
				if aerr != nil {
					return nil, err
				}
				p = netip.PrefixFrom(a, a.BitLen())
			}
			pf.prefixes = append(pf.prefixes, p)
		}
		m.filters = append(m.filters, pf)
	}
	return m, nil
}

func (m *matcher) match(r *flowpb.Flow) bool {
	k := r.K8S
	if m.namespace != "" && k.SourcePodNamespace != m.namespace && k.DestinationPodNamespace != m.namespace {
		return false
	}
	if !m.since.IsZero() && r.EndTs.AsTime().Before(m.since) {
		return false
	}
	for i := range m.filters {
		if !m.filters[i].match(r) {
			return false
		}
	}
	return true
}

type endpoint struct {
	namespace, pod string
	labels         map[string]string
	ip             []byte
}

// match reports whether r passes f: the endpoint criteria (Namespace, Pod, labels, IP) must all
// hold for the same endpoint, the one f's direction selects, or either one for "both".
func (f *filter) match(r *flowpb.Flow) bool {
	k := r.K8S
	if len(f.flowTypes) > 0 && !slices.Contains(f.flowTypes, k.FlowType) {
		return false
	}
	if len(f.services) > 0 {
		// The contract names a Service without its Namespace: "frontend", not "default/frontend".
		_, svc, _ := strings.Cut(k.DestinationServicePortName, "/")
		svc, _, _ = strings.Cut(svc, ":")
		if svc == "" || !slices.Contains(f.services, svc) {
			return false
		}
	}
	src := endpoint{k.SourcePodNamespace, k.SourcePodName, k.SourcePodLabels.GetLabels(), r.Ip.GetSource()}
	dst := endpoint{k.DestinationPodNamespace, k.DestinationPodName, k.DestinationPodLabels.GetLabels(), r.Ip.GetDestination()}
	switch f.direction {
	case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM:
		return f.matchEndpoint(src)
	case flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO:
		return f.matchEndpoint(dst)
	default:
		return f.matchEndpoint(src) || f.matchEndpoint(dst)
	}
}

func (f *filter) matchEndpoint(e endpoint) bool {
	if len(f.namespaces) > 0 && !slices.Contains(f.namespaces, e.namespace) {
		return false
	}
	if len(f.pods) > 0 && !slices.Contains(f.pods, e.pod) {
		return false
	}
	if f.selector != nil && (e.pod == "" || !f.selector.Matches(labels.Set(e.labels))) {
		return false
	}
	if len(f.prefixes) > 0 {
		a, ok := netip.AddrFromSlice(e.ip)
		if !ok || !slices.ContainsFunc(f.prefixes, func(p netip.Prefix) bool { return p.Contains(a) }) {
			return false
		}
	}
	return true
}
