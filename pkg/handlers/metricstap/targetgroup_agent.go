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

package metricstap

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	// agentServerName is the name the certificate of the agent API is issued for. The
	// certificate is self-signed by each agent and is valid for nothing else, so the name is
	// set explicitly instead of being derived from the address of the Node.
	agentServerName = "localhost"
	// agentNodeLabel is the label which carries the Node of an agent target.
	agentNodeLabel = "node"
)

var antreaAgentInfoGVR = schema.GroupVersionResource{
	Group:    "crd.antrea.io",
	Version:  "v1beta1",
	Resource: "antreaagentinfos",
}

// AgentTargetGroup is the set of Antrea Agents, one target per Node. The metrics of an agent are
// served by its API, on an address of its Node.
//
// Everything needed to reach an agent comes from its AntreaAgentInfo (API port, CA bundle) and
// from its Node (address), which antrea-ui watches with its own clients. Neither is a trust
// boundary: an agent can write the AntreaAgentInfo and the status of any Node, so a compromised
// agent decides where a scrape of any agent goes and which certificate it accepts. That is the
// reason scrapes carry the token of a ServiceAccount which can read /metrics and nothing else, and
// never the credential of a user. See docs/metrics.md.
//
// The two are watched, and not read when a target is listed or scraped: what a request costs the
// kube-apiserver would otherwise depend on the users, on their taps and on the names they select,
// through clients whose rate limit all users share.
type AgentTargetGroup struct {
	scraper *Scraper
	// agentInfos and nodes are keyed by name: an AntreaAgentInfo is named after the Node of
	// its agent.
	agentInfos cache.SharedIndexInformer
	nodes      cache.SharedIndexInformer
}

var _ runner = (*AgentTargetGroup)(nil)

// NewAgentTargetGroup creates the target group for the metrics of the Antrea Agents. Both clients
// must authenticate as antrea-ui itself: they need to list and watch AntreaAgentInfos and Nodes.
// Call Run in a goroutine to start the watches: until they have synced, the target group has no
// target and its scrapes fail as unavailable.
func NewAgentTargetGroup(dynamicClient dynamic.Interface, kubeClient kubernetes.Interface, scraper *Scraper) *AgentTargetGroup {
	// No resync: nothing handles the events, the stores are only read.
	agentInfos := dynamicinformer.NewFilteredDynamicInformer(
		dynamicClient, antreaAgentInfoGVR, metav1.NamespaceAll, 0, cache.Indexers{}, nil)
	g := &AgentTargetGroup{
		scraper:    scraper,
		agentInfos: agentInfos.Informer(),
		nodes:      coreinformers.NewNodeInformer(kubeClient, 0, cache.Indexers{}),
	}
	// Both stores hold one object per Node of the cluster, of which a scrape needs very little.
	// SetTransform only fails on an informer which has started.
	_ = g.agentInfos.SetTransform(trimAgentInfo)
	_ = g.nodes.SetTransform(trimNode)
	return g
}

// trimAgentInfo keeps what resolve reads of an AntreaAgentInfo: its name, API port and CA bundle.
func trimAgentInfo(obj any) (any, error) {
	agentInfo, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return obj, nil
	}
	trimmed := &unstructured.Unstructured{Object: map[string]any{}}
	trimmed.SetName(agentInfo.GetName())
	trimmed.SetResourceVersion(agentInfo.GetResourceVersion())
	for _, field := range []string{"apiPort", "apiCABundle"} {
		if value, ok := agentInfo.Object[field]; ok {
			trimmed.Object[field] = value
		}
	}
	return trimmed, nil
}

// trimNode keeps what resolve reads of a Node: its name and addresses.
func trimNode(obj any) (any, error) {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: node.Name, ResourceVersion: node.ResourceVersion},
		Status:     corev1.NodeStatus{Addresses: node.Status.Addresses},
	}, nil
}

// Run watches AntreaAgentInfos and Nodes until stopCh is closed. It blocks and should be called
// from a goroutine. The registry does it for the target groups it holds: see Registry.Run.
func (g *AgentTargetGroup) Run(stopCh <-chan struct{}) {
	var wg sync.WaitGroup
	wg.Go(func() { g.agentInfos.Run(stopCh) })
	wg.Go(func() { g.nodes.Run(stopCh) })
	wg.Wait()
}

func (g *AgentTargetGroup) Name() string {
	return TargetGroupAgent
}

func (g *AgentTargetGroup) Instanced() bool {
	return true
}

// synced reports whether the target group knows the agents and the Nodes of the cluster.
func (g *AgentTargetGroup) synced() bool {
	return g.agentInfos.HasSynced() && g.nodes.HasSynced()
}

func (g *AgentTargetGroup) Targets(context.Context) ([]apisv1.MetricsTarget, error) {
	if !g.synced() {
		return nil, newError(ErrorCodeUnavailable, "the Antrea agents are not known yet", nil)
	}
	nodes := g.agentInfos.GetStore().ListKeys()
	targets := make([]apisv1.MetricsTarget, 0, len(nodes))
	for _, node := range nodes {
		targets = append(targets, apisv1.MetricsTarget{
			ID:     TargetGroupAgent + "/" + node,
			Labels: map[string]string{agentNodeLabel: node},
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets, nil
}

func (g *AgentTargetGroup) Scrape(ctx context.Context, node string) ([]*dto.MetricFamily, error) {
	info, err := g.resolve(node)
	if err != nil {
		return nil, err
	}
	return g.scraper.Scrape(ctx, info)
}

// resolve returns how to reach the agent of node. It contacts nothing: a name which is not the
// one of an agent costs as little as one which is.
func (g *AgentTargetGroup) resolve(node string) (ConnInfo, error) {
	if !g.synced() {
		return ConnInfo{}, newError(ErrorCodeUnavailable, "the Antrea agents are not known yet", nil)
	}
	// The stores are in memory: their lookups do not fail.
	obj, found, _ := g.agentInfos.GetStore().GetByKey(node)
	if !found {
		return ConnInfo{}, newError(ErrorCodeNotFound, fmt.Sprintf("no Antrea agent on Node %q", node), nil)
	}
	agentInfo := obj.(*unstructured.Unstructured)
	port, found, err := unstructured.NestedInt64(agentInfo.Object, "apiPort")
	if err != nil || !found || port < 1 || port > 65535 {
		return ConnInfo{}, newError(ErrorCodeInvalidTarget, "the Antrea agent does not report a valid API port", err)
	}
	// apiCABundle is a []byte in the Go type, hence base64 in an unstructured object.
	encodedBundle, _, err := unstructured.NestedString(agentInfo.Object, "apiCABundle")
	if err != nil || encodedBundle == "" {
		// No fallback to an unverified connection: the token would go to whoever answers.
		return ConnInfo{}, newError(ErrorCodeInvalidTarget, "the Antrea agent does not report a CA bundle", err)
	}
	caBundle, err := base64.StdEncoding.DecodeString(encodedBundle)
	if err != nil {
		return ConnInfo{}, newError(ErrorCodeInvalidTarget, "the Antrea agent does not report a valid CA bundle", err)
	}

	obj, found, _ = g.nodes.GetStore().GetByKey(node)
	if !found {
		return ConnInfo{}, newError(ErrorCodeNotFound, fmt.Sprintf("Node %q does not exist", node), nil)
	}
	addr, err := nodeAddress(obj.(*corev1.Node))
	if err != nil {
		return ConnInfo{}, newError(ErrorCodeInvalidTarget, "the Node of the Antrea agent has no usable address", err)
	}
	return ConnInfo{
		Host:       net.JoinHostPort(addr.String(), strconv.FormatInt(port, 10)),
		ServerName: agentServerName,
		CABundle:   caBundle,
	}, nil
}

// nodeAddress returns the address to reach node at: its first InternalIP, or its first ExternalIP
// when it has no InternalIP.
//
// Loopback, link-local and unspecified addresses are refused. This is not what protects the token,
// since the status of a Node can be rewritten to any other address: it only keeps antrea-ui from
// being pointed at its own Pod and at link-local services, such as the metadata endpoint of a
// cloud provider.
func nodeAddress(node *corev1.Node) (netip.Addr, error) {
	for _, addressType := range []corev1.NodeAddressType{corev1.NodeInternalIP, corev1.NodeExternalIP} {
		for _, address := range node.Status.Addresses {
			if address.Type != addressType {
				continue
			}
			addr, err := netip.ParseAddr(address.Address)
			if err != nil {
				return netip.Addr{}, fmt.Errorf("invalid %s %q: %w", addressType, address.Address, err)
			}
			addr = addr.Unmap()
			if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
				return netip.Addr{}, fmt.Errorf("refusing %s %s: loopback, link-local and unspecified addresses are not allowed", addressType, addr)
			}
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("the Node has no InternalIP or ExternalIP")
}
