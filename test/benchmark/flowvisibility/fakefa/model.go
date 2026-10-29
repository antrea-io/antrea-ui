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

// Package fakefa is a stand-in for the Flow Aggregator's FlowStreamService: it models a synthetic
// cluster (Namespaces, Nodes, workloads, Services, and the connections between them) and streams
// the flow records that cluster would produce, so that antrea-ui can be benchmarked without Antrea.
package fakefa

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"time"
)

// Scale describes the synthetic cluster and its traffic. The topology knobs (Namespaces, Nodes,
// PodsPerNode, PodsPerWorkload) decide how many service map nodes there are; the traffic knobs
// decide how many edges there are and how many flow records per second reach antrea-ui.
type Scale struct {
	// Namespaces is the number of application Namespaces. Workloads are spread evenly across them.
	Namespaces int `json:"namespaces"`
	// Nodes is the number of K8s Nodes. Pods are spread evenly across them; a connection between
	// Pods on the same Node is intra-node, and inter-node otherwise.
	Nodes int `json:"nodes"`
	// PodsPerNode is the number of Pods on each Node (at most 250).
	PodsPerNode int `json:"podsPerNode"`
	// PodsPerWorkload is the number of replicas of each workload. A workload is one node of the
	// service map, so Nodes*PodsPerNode/PodsPerWorkload is roughly the number of map nodes.
	PodsPerWorkload int `json:"podsPerWorkload"`
	// ServiceFraction is the fraction of workloads exposed by a ClusterIP Service. Only those can
	// be a peer of another workload.
	ServiceFraction float64 `json:"serviceFraction"`
	// PeersPerWorkload is how many Services each workload is a client of: the out-degree of a
	// node of the service map.
	PeersPerWorkload int `json:"peersPerWorkload"`
	// CrossNamespaceFraction is the fraction of a workload's peers picked in another Namespace.
	CrossNamespaceFraction float64 `json:"crossNamespaceFraction"`
	// ConnectionsPerPeer is the number of long-lived connections each client Pod keeps open to
	// each of its peers, each to a random backend Pod. With the source port masked, as the
	// frontend's flow store does, this is the number of distinct connections per peer and Pod.
	ConnectionsPerPeer int `json:"connectionsPerPeer"`
	// ExternalEgressFraction is the fraction of workloads that also connect to destinations
	// outside the cluster (one long-lived connection per Pod).
	ExternalEgressFraction float64 `json:"externalEgressFraction"`
	// ExternalIngressFraction is the fraction of Services that also receive connections from
	// outside the cluster (one long-lived connection per backend Pod).
	ExternalIngressFraction float64 `json:"externalIngressFraction"`
	// ExternalIPs is the size of the pool external peers are drawn from.
	ExternalIPs int `json:"externalIPs"`
	// AllowPolicyFraction and DropPolicyFraction are the fractions of workload-to-Service edges
	// that an allow (K8s NetworkPolicy) or a drop (Antrea ClusterNetworkPolicy) rule applies to.
	AllowPolicyFraction float64 `json:"allowPolicyFraction"`
	DropPolicyFraction  float64 `json:"dropPolicyFraction"`
	// ShortConnectionRate is the cluster-wide rate of short-lived connections, in connections per
	// second. Each one is a single end-of-flow record between a client Pod and one of its peers.
	ShortConnectionRate float64 `json:"shortConnectionRate"`
	// ActiveTimeout is how often a long-lived connection is reported, like the Flow Aggregator's
	// active flow record timeout. Long-lived connections / ActiveTimeout is the steady record rate.
	ActiveTimeout time.Duration `json:"activeTimeout"`
	// PodChurnRate is how many Pods are replaced per minute, cluster-wide. A replaced Pod gets a
	// new name and IP, so its connections end and new ones (with new keys) start.
	PodChurnRate float64 `json:"podChurnRate"`
	// RingBufferSize is how many records the server keeps for a new stream to replay.
	RingBufferSize int `json:"ringBufferSize"`
	// PrefillHistory replays one ActiveTimeout of traffic into the ring buffer at startup, so a
	// stream opened right away starts with the backlog a long-running Flow Aggregator would have.
	PrefillHistory bool `json:"prefillHistory"`
	// MaxBatchSize caps the number of records in one GetFlowsResponse.
	MaxBatchSize int `json:"maxBatchSize"`
	// Seed makes the topology and traffic reproducible.
	Seed uint64 `json:"seed"`
}

// Presets are named starting points for Scale; any knob can then be overridden on its own.
var Presets = map[string]Scale{
	"tiny":   withTopology(3, 2, 10, 2),
	"small":  withTopology(10, 5, 20, 3),
	"medium": withTopology(30, 20, 30, 3),
	"large":  withTopology(100, 50, 40, 4),
	"xlarge": withTopology(300, 200, 50, 5),
}

// withTopology builds a preset from the topology knobs. Short-lived connections scale with the
// number of Pods: one every 10s per Pod.
func withTopology(namespaces, nodes, podsPerNode, podsPerWorkload int) Scale {
	s := DefaultScale()
	s.Namespaces, s.Nodes, s.PodsPerNode, s.PodsPerWorkload = namespaces, nodes, podsPerNode, podsPerWorkload
	s.ShortConnectionRate = float64(nodes*podsPerNode) / 10
	return s
}

// DefaultScale is the "small" preset.
func DefaultScale() Scale {
	return Scale{
		Namespaces:              10,
		Nodes:                   5,
		PodsPerNode:             20,
		PodsPerWorkload:         3,
		ServiceFraction:         0.7,
		PeersPerWorkload:        3,
		CrossNamespaceFraction:  0.2,
		ConnectionsPerPeer:      1,
		ExternalEgressFraction:  0.1,
		ExternalIngressFraction: 0.05,
		ExternalIPs:             50,
		AllowPolicyFraction:     0.3,
		DropPolicyFraction:      0.05,
		ShortConnectionRate:     10,
		ActiveTimeout:           60 * time.Second,
		PodChurnRate:            2,
		RingBufferSize:          100_000,
		PrefillHistory:          true,
		MaxBatchSize:            1000,
		Seed:                    1,
	}
}

func (s *Scale) Validate() error {
	switch {
	case s.Namespaces < 1:
		return fmt.Errorf("namespaces must be at least 1")
	case s.Nodes < 1 || s.Nodes > 60000:
		return fmt.Errorf("nodes must be between 1 and 60000")
	case s.PodsPerNode < 1 || s.PodsPerNode > 250:
		return fmt.Errorf("podsPerNode must be between 1 and 250")
	case s.PodsPerWorkload < 1:
		return fmt.Errorf("podsPerWorkload must be at least 1")
	case s.ExternalIPs < 1:
		return fmt.Errorf("externalIPs must be at least 1")
	case s.ActiveTimeout < time.Second:
		return fmt.Errorf("activeTimeout must be at least 1s")
	case s.RingBufferSize < 1:
		return fmt.Errorf("ringBufferSize must be at least 1")
	case s.MaxBatchSize < 1:
		return fmt.Errorf("maxBatchSize must be at least 1")
	}
	return nil
}

type node struct {
	name string
	uid  string
	// index decides the Node's Pod CIDR: 10.<index/256>.<index%256>.0/24.
	index int
}

type pod struct {
	name      string
	uid       string
	namespace string
	labels    map[string]string
	node      *node
	ip        netip.Addr
	workload  *workload
	// generation is bumped each time the Pod is replaced, so connections can tell a Pod they
	// were opened against from its replacement.
	generation int
}

type service struct {
	name       string
	uid        string
	clusterIP  netip.Addr
	port       uint32
	targetPort uint32
	portName   string
}

type workload struct {
	name      string
	namespace string
	pods      []*pod
	// svc is nil for a workload that no Service selects.
	svc *service
	// peers are the workloads (all with a Service) this one is a client of.
	peers []*workload
	// externalEgress is set for a workload whose Pods also connect outside the cluster.
	externalEgress bool
	// externalIngress is set for a workload whose Service is also reached from outside.
	externalIngress bool
}

type policy struct {
	typ       int32 // flowpb.NetworkPolicyType
	namespace string
	name      string
	uid       string
	rule      string
	action    int32 // flowpb.NetworkPolicyRuleAction
}

// edgePolicies is what applies to traffic from one workload to another.
type edgePolicies struct {
	ingress *policy
	egress  *policy
}

type cluster struct {
	namespaces  []string
	nodes       []*node
	workloads   []*workload
	pods        []*pod
	externalIPs []netip.Addr
	policies    map[[2]*workload]edgePolicies
	// nextPodIP tracks the next host address to hand out on each Node; a replaced Pod gets a new
	// one, like a real IPAM would, so that its connections get new keys.
	nextPodIP map[*node]int
}

var workloadWords = []string{
	"frontend", "api", "cart", "checkout", "catalog", "payments", "auth", "search", "inventory",
	"orders", "shipping", "reviews", "recommend", "ads", "email", "currency", "users", "billing",
	"gateway", "cache", "db", "queue", "worker", "metrics", "logs",
}

const randChars = "bcdfghjklmnpqrstvwxz2456789"

func randString(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randChars[r.IntN(len(randChars))]
	}
	return string(b)
}

func randUID(r *rand.Rand) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", r.Uint32(), r.Uint32()&0xffff, r.Uint32()&0xffff, r.Uint32()&0xffff, r.Uint64()&0xffffffffffff)
}

// NamespaceName is the name of the i-th synthetic Namespace.
func NamespaceName(i int) string { return fmt.Sprintf("ns-%03d", i) }

func buildCluster(s Scale, r *rand.Rand) *cluster {
	c := &cluster{policies: map[[2]*workload]edgePolicies{}, nextPodIP: map[*node]int{}}
	for i := range s.Namespaces {
		c.namespaces = append(c.namespaces, NamespaceName(i))
	}
	for i := range s.Nodes {
		c.nodes = append(c.nodes, &node{name: fmt.Sprintf("node-%03d", i), uid: randUID(r), index: i})
	}
	for i := range s.ExternalIPs {
		c.externalIPs = append(c.externalIPs, netip.AddrFrom4([4]byte{203, 0, byte(113 + i/250), byte(1 + i%250)}))
	}

	totalPods := s.Nodes * s.PodsPerNode
	numWorkloads := max(1, totalPods/s.PodsPerWorkload)
	svcIndex := 0
	for i := range numWorkloads {
		ns := c.namespaces[i%len(c.namespaces)]
		// Workload names repeat across Namespaces, as they do in real clusters.
		nameIdx := i / len(c.namespaces)
		name := workloadWords[nameIdx%len(workloadWords)]
		if nameIdx >= len(workloadWords) {
			name = fmt.Sprintf("%s-%d", name, nameIdx/len(workloadWords))
		}
		w := &workload{name: name, namespace: ns}
		if r.Float64() < s.ServiceFraction {
			w.svc = &service{
				name:       name,
				uid:        randUID(r),
				clusterIP:  netip.AddrFrom4([4]byte{10, 96, byte(svcIndex / 250), byte(1 + svcIndex%250)}),
				port:       80,
				targetPort: 8080,
				portName:   "http",
			}
			svcIndex++
			w.externalIngress = r.Float64() < s.ExternalIngressFraction
		}
		w.externalEgress = r.Float64() < s.ExternalEgressFraction
		c.workloads = append(c.workloads, w)
	}

	// Pods are dealt to Nodes round-robin, so a workload's replicas land on different Nodes.
	podIdx := 0
	for _, w := range c.workloads {
		replicas := s.PodsPerWorkload
		for range replicas {
			n := c.nodes[podIdx%len(c.nodes)]
			podIdx++
			p := &pod{namespace: w.namespace, workload: w, node: n, labels: map[string]string{"app": w.name}}
			c.respawnPod(p, r)
			w.pods = append(w.pods, p)
			c.pods = append(c.pods, p)
		}
	}

	var withSvc []*workload
	byNS := map[string][]*workload{}
	for _, w := range c.workloads {
		if w.svc != nil {
			withSvc = append(withSvc, w)
			byNS[w.namespace] = append(byNS[w.namespace], w)
		}
	}
	if len(withSvc) == 0 {
		return c
	}
	for _, w := range c.workloads {
		seen := map[*workload]bool{w: true}
		for range s.PeersPerWorkload {
			candidates := byNS[w.namespace]
			if r.Float64() < s.CrossNamespaceFraction || len(candidates) < 2 {
				candidates = withSvc
			}
			// A few attempts at a fresh peer; a small cluster may just not have enough.
			for range 8 {
				peer := candidates[r.IntN(len(candidates))]
				if seen[peer] {
					continue
				}
				seen[peer] = true
				w.peers = append(w.peers, peer)
				c.policies[[2]*workload{w, peer}] = randomPolicies(s, r, w, peer)
				break
			}
		}
	}
	return c
}

func randomPolicies(s Scale, r *rand.Rand, src, dst *workload) edgePolicies {
	x := r.Float64()
	switch {
	case x < s.DropPolicyFraction:
		return edgePolicies{ingress: &policy{
			typ: 3, name: "deny-" + src.namespace + "-to-" + dst.name, uid: randUID(r), rule: "drop-rule", action: 2,
		}}
	case x < s.DropPolicyFraction+s.AllowPolicyFraction:
		return edgePolicies{ingress: &policy{
			typ: 1, namespace: dst.namespace, name: "allow-" + dst.name, uid: randUID(r), action: 1,
		}}
	}
	return edgePolicies{}
}

// respawnPod gives p a fresh identity: name, UID and IP, as a Deployment replacing it would.
func (c *cluster) respawnPod(p *pod, r *rand.Rand) {
	p.name = fmt.Sprintf("%s-%s-%s", p.workload.name, randString(r, 10), randString(r, 5))
	p.uid = randUID(r)
	host := c.nextPodIP[p.node]
	c.nextPodIP[p.node] = (host + 1) % 250
	// #nosec G115: Validate bounds the Node index to 60000, and host is under 250
	p.ip = netip.AddrFrom4([4]byte{10, byte(p.node.index / 256), byte(p.node.index % 256), byte(2 + host)})
	p.generation++
}

func (c *cluster) summary() string {
	edges := 0
	for _, w := range c.workloads {
		edges += len(w.peers)
	}
	return fmt.Sprintf("%d namespaces, %d nodes, %d workloads, %d pods, %d workload->service edges",
		len(c.namespaces), len(c.nodes), len(c.workloads), len(c.pods), edges)
}
