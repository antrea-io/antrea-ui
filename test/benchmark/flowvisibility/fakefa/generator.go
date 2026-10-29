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
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// tick is the generator's time step: records due within one tick are emitted together.
const tick = 100 * time.Millisecond

const (
	protoTCP = 6

	flowTypeIntraNode    = flowpb.FlowType_FLOW_TYPE_INTRA_NODE
	flowTypeInterNode    = flowpb.FlowType_FLOW_TYPE_INTER_NODE
	flowTypeToExternal   = flowpb.FlowType_FLOW_TYPE_TO_EXTERNAL
	flowTypeFromExternal = flowpb.FlowType_FLOW_TYPE_FROM_EXTERNAL
)

// conn is one connection of the synthetic cluster. Exactly one of src and srcIP, and of dst and
// dstIP, is set: the Pod for an in-cluster endpoint, the address for an external one.
type conn struct {
	id       string
	src, dst *pod
	// srcGen/dstGen are the generations of src/dst the connection was opened against: a Pod
	// replaced since then ends the connection.
	srcGen, dstGen int
	srcIP, dstIP   netip.Addr
	svc            *service
	srcPort        uint32
	dstPort        uint32
	policies       edgePolicies
	start          time.Time
	lastExport     time.Time
	// Bytes per second in each direction, and the cumulative counts as of lastExport.
	fwdRate, revRate   float64
	fwdBytes, revBytes uint64
	dead               bool
	// reconnected is set once the client of a connection whose server was replaced has opened
	// a new one, so that a server replaced twice does not get reconnected to twice.
	reconnected bool
}

func (c *conn) stale() bool {
	return (c.src != nil && c.src.generation != c.srcGen) || (c.dst != nil && c.dst.generation != c.dstGen)
}

// Generator produces the flow records of a synthetic cluster into a ring buffer, in real time.
type Generator struct {
	scale   Scale
	r       *rand.Rand
	cluster *cluster
	ring    *ring
	// wheel holds the long-lived connections, each in the slot of the tick (modulo
	// ActiveTimeout) at which it is reported.
	wheel    [][]*conn
	slot     int
	nextID   uint64
	shortAcc float64
	churnAcc float64

	recordsTotal atomic.Uint64
	liveConns    atomic.Int64
}

func NewGenerator(s Scale) (*Generator, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	// #nosec G404: synthetic data, which has to be reproducible from a seed
	r := rand.New(rand.NewPCG(s.Seed, s.Seed^0x9e3779b97f4a7c15))
	g := &Generator{
		scale:   s,
		r:       r,
		cluster: buildCluster(s, r),
		ring:    newRing(s.RingBufferSize),
		wheel:   make([][]*conn, int(s.ActiveTimeout/tick)),
	}
	return g, nil
}

// Summary describes the synthetic cluster, for logs.
func (g *Generator) Summary() string {
	return fmt.Sprintf("%s, %d long-lived connections (%.1f records/s steady state, plus %.1f short-lived connections/s)",
		g.cluster.summary(), g.liveConns.Load(), float64(g.liveConns.Load())/g.scale.ActiveTimeout.Seconds(), g.scale.ShortConnectionRate)
}

// RecordsTotal is the number of records generated so far, including the prefilled history.
func (g *Generator) RecordsTotal() uint64 { return g.recordsTotal.Load() }

// LiveConnections is the number of long-lived connections currently open.
func (g *Generator) LiveConnections() int64 { return g.liveConns.Load() }

// Run opens the initial connections, optionally prefills the history, then generates records in
// real time until ctx is done. ready is closed once the history is in the ring buffer.
func (g *Generator) Run(ctx context.Context, ready chan<- struct{}) {
	now := time.Now()
	historyStart := now
	if g.scale.PrefillHistory {
		historyStart = now.Add(-g.scale.ActiveTimeout)
	}
	g.openInitialConnections(historyStart)
	if g.scale.PrefillHistory {
		// One full turn of the wheel, with simulated timestamps, so every long-lived connection
		// has been reported once by the time the stream opens.
		for t := historyStart; t.Before(now); t = t.Add(tick) {
			g.step(t, false)
		}
	}
	close(ready)

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			g.step(t, true)
		}
	}
}

func (g *Generator) openInitialConnections(at time.Time) {
	for _, w := range g.cluster.workloads {
		for _, p := range w.pods {
			g.openClientConnections(p, at, true)
		}
		if w.externalIngress {
			for _, p := range w.pods {
				g.addLongLived(g.newExternalIngressConn(p, at), true)
			}
		}
	}
}

// openClientConnections opens p's long-lived connections as a client: to each peer, and outside
// the cluster if its workload does that.
func (g *Generator) openClientConnections(p *pod, at time.Time, backdate bool) {
	w := p.workload
	for _, peer := range w.peers {
		for range g.scale.ConnectionsPerPeer {
			g.addLongLived(g.newServiceConn(p, peer, at), backdate)
		}
	}
	if w.externalEgress {
		g.addLongLived(g.newExternalEgressConn(p, at), backdate)
	}
}

// addLongLived puts c on the wheel. backdate is for the connections open when the benchmark
// starts, which have been open for a while already.
func (g *Generator) addLongLived(c *conn, backdate bool) {
	if backdate {
		c.start = c.start.Add(-time.Duration(g.r.Int64N(int64(10 * time.Minute))))
	}
	c.lastExport = c.start
	slot := g.r.IntN(len(g.wheel))
	g.wheel[slot] = append(g.wheel[slot], c)
	g.liveConns.Add(1)
}

func (g *Generator) newConn(at time.Time) *conn {
	g.nextID++
	// Log-uniform between 1KB/s and 1MB/s, so edge widths and rates vary a lot.
	rate := math.Pow(10, 3+3*g.r.Float64())
	return &conn{
		id:      fmt.Sprintf("fakefa-%d", g.nextID),
		srcPort: 32768 + g.r.Uint32N(28000),
		start:   at,
		fwdRate: rate,
		revRate: rate * (0.5 + 4*g.r.Float64()),
	}
}

func (g *Generator) newServiceConn(src *pod, peer *workload, at time.Time) *conn {
	c := g.newConn(at)
	dst := peer.pods[g.r.IntN(len(peer.pods))]
	c.src, c.srcGen = src, src.generation
	c.dst, c.dstGen = dst, dst.generation
	c.svc = peer.svc
	c.dstPort = peer.svc.targetPort
	c.policies = g.cluster.policies[[2]*workload{src.workload, peer}]
	return c
}

func (g *Generator) newExternalEgressConn(src *pod, at time.Time) *conn {
	c := g.newConn(at)
	c.src, c.srcGen = src, src.generation
	c.dstIP = g.cluster.externalIPs[g.r.IntN(len(g.cluster.externalIPs))]
	c.dstPort = 443
	return c
}

func (g *Generator) newExternalIngressConn(dst *pod, at time.Time) *conn {
	c := g.newConn(at)
	c.srcIP = g.cluster.externalIPs[g.r.IntN(len(g.cluster.externalIPs))]
	c.dst, c.dstGen = dst, dst.generation
	c.svc = dst.workload.svc
	c.dstPort = dst.workload.svc.targetPort
	return c
}

// step advances the simulation to now: it reports the long-lived connections due in this tick,
// emits short-lived connections, and (when churn is set) replaces Pods.
func (g *Generator) step(now time.Time, churn bool) {
	var records []*flowpb.Flow

	slot := g.wheel[g.slot]
	kept := slot[:0]
	for _, c := range slot {
		if c.dead {
			continue
		}
		if c.stale() {
			// One end was replaced since the last report: the connection ended in between.
			records = append(records, g.record(c, now, flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW))
			c.dead = true
			g.liveConns.Add(-1)
			continue
		}
		records = append(records, g.record(c, now, flowpb.FlowEndReason_FLOW_END_REASON_ACTIVE_TIMEOUT))
		kept = append(kept, c)
	}
	clear(slot[len(kept):])
	g.wheel[g.slot] = kept
	g.slot = (g.slot + 1) % len(g.wheel)

	g.shortAcc += g.scale.ShortConnectionRate * tick.Seconds()
	for ; g.shortAcc >= 1; g.shortAcc-- {
		if c := g.newShortConn(now); c != nil {
			records = append(records, g.record(c, now, flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW))
		}
	}

	if churn {
		g.churnAcc += g.scale.PodChurnRate / 60 * tick.Seconds()
		for ; g.churnAcc >= 1; g.churnAcc-- {
			g.replaceRandomPod(now)
		}
	}

	if len(records) > 0 {
		g.recordsTotal.Add(uint64(len(records)))
		g.ring.append(records)
	}
}

func (g *Generator) newShortConn(now time.Time) *conn {
	p := g.cluster.pods[g.r.IntN(len(g.cluster.pods))]
	if len(p.workload.peers) == 0 {
		return nil
	}
	peer := p.workload.peers[g.r.IntN(len(p.workload.peers))]
	c := g.newServiceConn(p, peer, now.Add(-time.Duration(g.r.Int64N(int64(2*time.Second)))))
	c.lastExport = c.start
	return c
}

// replaceRandomPod replaces a Pod the way a rollout would: same workload and Node, new name and
// IP. Its connections are reported as ended the next time they are due (see step); the
// replacement opens fresh ones, and the Pods that were its clients reconnect to it.
func (g *Generator) replaceRandomPod(now time.Time) {
	p := g.cluster.pods[g.r.IntN(len(g.cluster.pods))]
	g.cluster.respawnPod(p, g.r)
	g.openClientConnections(p, now, false)
	if p.workload.externalIngress {
		g.addLongLived(g.newExternalIngressConn(p, now), false)
	}
	// Clients of p's workload whose connection is now stale reconnect to a random backend. They
	// are found by scanning the wheel, which is cheap next to the rate at which Pods churn. A
	// client replaced since it opened the connection is skipped: its replacement opened its own.
	var reconnect []*conn
	for _, slot := range g.wheel {
		for _, c := range slot {
			if !c.dead && !c.reconnected && c.dst == p && c.src != nil && c.dstGen != p.generation &&
				c.src.generation == c.srcGen {
				c.reconnected = true
				reconnect = append(reconnect, c)
			}
		}
	}
	for _, c := range reconnect {
		g.addLongLived(g.newServiceConn(c.src, p.workload, now), false)
	}
}

func ipBytes(a netip.Addr) []byte {
	if !a.IsValid() {
		return nil
	}
	b := a.As4()
	return b[:]
}

// record builds the flow record reporting c as of now, and advances c's counters.
func (g *Generator) record(c *conn, now time.Time, reason flowpb.FlowEndReason) *flowpb.Flow {
	elapsed := now.Sub(c.lastExport).Seconds()
	denied := c.policies.ingress != nil && c.policies.ingress.action != int32(flowpb.NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_ALLOW)
	fwdRate, revRate := c.fwdRate, c.revRate
	if denied {
		// Retransmitted SYNs, and nothing back.
		fwdRate, revRate = 60, 0
	}
	fwdDelta := uint64(fwdRate * elapsed)
	revDelta := uint64(revRate * elapsed)
	c.fwdBytes += fwdDelta
	c.revBytes += revDelta
	c.lastExport = now

	srcIP, dstIP := c.srcIP, c.dstIP
	if c.src != nil {
		srcIP = c.src.ip
	}
	if c.dst != nil {
		dstIP = c.dst.ip
	}
	tcpState := "ESTABLISHED"
	if reason == flowpb.FlowEndReason_FLOW_END_REASON_END_OF_FLOW {
		tcpState = "TIME_WAIT"
	}
	if denied {
		tcpState = "SYN_SENT"
	}

	k := &flowpb.Kubernetes{}
	switch {
	case c.src == nil:
		k.FlowType = flowTypeFromExternal
	case c.dst == nil:
		k.FlowType = flowTypeToExternal
	case c.src.node == c.dst.node:
		k.FlowType = flowTypeIntraNode
	default:
		k.FlowType = flowTypeInterNode
	}
	if p := c.src; p != nil {
		k.SourcePodNamespace, k.SourcePodName, k.SourcePodUid = p.namespace, p.name, p.uid
		k.SourcePodLabels = &flowpb.Labels{Labels: p.labels}
		k.SourceNodeName, k.SourceNodeUid = p.node.name, p.node.uid
	}
	if p := c.dst; p != nil {
		k.DestinationPodNamespace, k.DestinationPodName, k.DestinationPodUid = p.namespace, p.name, p.uid
		k.DestinationPodLabels = &flowpb.Labels{Labels: p.labels}
		k.DestinationNodeName, k.DestinationNodeUid = p.node.name, p.node.uid
	}
	if s := c.svc; s != nil {
		k.DestinationClusterIp = ipBytes(s.clusterIP)
		k.DestinationServiceIp = ipBytes(s.clusterIP)
		k.DestinationServicePort = s.port
		k.DestinationServicePortName = fmt.Sprintf("%s/%s:%s", c.dst.namespace, s.name, s.portName)
		k.DestinationServiceUid = s.uid
	}
	if p := c.policies.ingress; p != nil {
		k.IngressNetworkPolicyType = flowpb.NetworkPolicyType(p.typ)
		k.IngressNetworkPolicyNamespace, k.IngressNetworkPolicyName, k.IngressNetworkPolicyUid = p.namespace, p.name, p.uid
		k.IngressNetworkPolicyRuleName = p.rule
		k.IngressNetworkPolicyRuleAction = flowpb.NetworkPolicyRuleAction(p.action)
	}
	if p := c.policies.egress; p != nil {
		k.EgressNetworkPolicyType = flowpb.NetworkPolicyType(p.typ)
		k.EgressNetworkPolicyNamespace, k.EgressNetworkPolicyName, k.EgressNetworkPolicyUid = p.namespace, p.name, p.uid
		k.EgressNetworkPolicyRuleName = p.rule
		k.EgressNetworkPolicyRuleAction = flowpb.NetworkPolicyRuleAction(p.action)
	}

	return &flowpb.Flow{
		Id:        c.id,
		StartTs:   timestamppb.New(c.start),
		EndTs:     timestamppb.New(now),
		EndReason: reason,
		Ip: &flowpb.IP{
			Version:     flowpb.IPVersion_IP_VERSION_4,
			Source:      ipBytes(srcIP),
			Destination: ipBytes(dstIP),
		},
		Transport: &flowpb.Transport{
			ProtocolNumber:  protoTCP,
			SourcePort:      c.srcPort,
			DestinationPort: c.dstPort,
			Protocol:        &flowpb.Transport_TCP{TCP: &flowpb.TCP{StateName: tcpState}},
		},
		K8S: k,
		Stats: &flowpb.Stats{
			PacketTotalCount: c.fwdBytes/1000 + 1,
			PacketDeltaCount: fwdDelta/1000 + 1,
			OctetTotalCount:  c.fwdBytes,
			OctetDeltaCount:  fwdDelta,
		},
		ReverseStats: &flowpb.Stats{
			PacketTotalCount: c.revBytes / 1000,
			PacketDeltaCount: revDelta / 1000,
			OctetTotalCount:  c.revBytes,
			OctetDeltaCount:  revDelta,
		},
		Aggregation: &flowpb.Aggregation{
			Throughput:        uint64(fwdRate * 8),
			ReverseThroughput: uint64(revRate * 8),
		},
	}
}

// ring is the Flow Aggregator's ring buffer of recent records, which every stream reads from at
// its own pace. Records are never modified once appended, so streams share them.
type ring struct {
	mu   sync.Mutex
	buf  []*flowpb.Flow
	next int64 // sequence number of the next record appended
	// notify is closed, and replaced, whenever records are appended.
	notify chan struct{}
}

func newRing(size int) *ring {
	return &ring{buf: make([]*flowpb.Flow, size), notify: make(chan struct{})}
}

func (r *ring) append(records []*flowpb.Flow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		r.buf[r.next%int64(len(r.buf))] = rec
		r.next++
	}
	close(r.notify)
	r.notify = make(chan struct{})
}

// oldest is the sequence number of the oldest record still held.
func (r *ring) oldest() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return max(0, r.next-int64(len(r.buf)))
}

// read returns up to maxCount records starting at sequence number from, the sequence number to
// read from next, how many records were lost because from had already been overwritten, and a
// channel closed when more records are appended.
func (r *ring) read(from int64, maxCount int) ([]*flowpb.Flow, int64, uint64, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var dropped uint64
	if oldest := r.next - int64(len(r.buf)); from < oldest {
		dropped = uint64(oldest - from) // #nosec G115: positive, since from < oldest
		from = oldest
	}
	n := min(int64(maxCount), r.next-from)
	out := make([]*flowpb.Flow, 0, n)
	for i := range n {
		out = append(out, r.buf[(from+i)%int64(len(r.buf))])
	}
	return out, from + n, dropped, r.notify
}
