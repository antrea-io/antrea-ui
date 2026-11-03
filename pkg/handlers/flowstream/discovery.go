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
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

const (
	// FlowServiceLabel marks a Service as the one serving FlowStreamService. Antrea's Flow
	// Aggregator chart sets it when FlowStreamService is enabled.
	FlowServiceLabel = "observability.antrea.io/flow-stream-service"
	// FlowServiceSelector selects the Services which carry FlowServiceLabel. It is what a source
	// uses unless it configures its own selector.
	FlowServiceSelector = serverconfig.DefaultFlowServiceSelector
	// FlowServicePortName names the TCP port of a discovered Service serving FlowStreamService.
	FlowServicePortName = "flowstream-grpc"
)

// ServiceDiscovery watches labeled FlowStreamService Services across the cluster, for the flow
// source which enabled discovery. Deployments must restrict matching Services and their CA
// ConfigMaps to trusted administrators.
type ServiceDiscovery struct {
	// source is the name of the flow source, for messages.
	source string
	// selector selects the Services of the source. It is also applied to the cached Services,
	// since the watch alone is not a guarantee for every client.
	selector  labels.Selector
	informer  cache.SharedIndexInformer
	mu        sync.Mutex
	signature string
	changed   chan struct{}
}

// NewServiceDiscovery returns the discovery of the Service of flow source. selector is a label
// selector, and FlowServiceSelector when empty.
func NewServiceDiscovery(client kubernetes.Interface, source, selector string) (*ServiceDiscovery, error) {
	if selector == "" {
		selector = FlowServiceSelector
	}
	parsed, err := labels.Parse(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector %q: %w", selector, err)
	}
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) { options.LabelSelector = parsed.String() }))
	d := &ServiceDiscovery{source: source, selector: parsed, informer: factory.Core().V1().Services().Informer(), changed: make(chan struct{})}
	_, err = d.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { d.update() }, UpdateFunc: func(any, any) { d.update() }, DeleteFunc: func(any) { d.update() },
	})
	return d, err
}

func (d *ServiceDiscovery) Run(ctx context.Context) { d.informer.Run(ctx.Done()) }

// DiscoveredEndpoint keeps the Service address and its CA namespace together.
type DiscoveredEndpoint struct {
	Address   string
	Namespace string
	// Revision changes when the Service routes the same address to different backends.
	Revision string
}

func (d *ServiceDiscovery) resolve() (DiscoveredEndpoint, error) {
	var matches []*corev1.Service
	for _, obj := range d.informer.GetStore().List() {
		svc := obj.(*corev1.Service)
		if d.selector.Matches(labels.Set(svc.Labels)) {
			matches = append(matches, svc)
		}
	}
	if len(matches) == 0 {
		return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("waiting for the Service of flow source %q", d.source))
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, svc := range matches {
			names = append(names, svc.Namespace+"/"+svc.Name)
		}
		sort.Strings(names)
		return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("multiple Services match flow source %q: %s", d.source, strings.Join(names, ", ")))
	}
	svc := matches[0]
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("the Service of flow source %q must not be ExternalName", d.source))
	}
	for _, port := range svc.Spec.Ports {
		if port.Name == FlowServicePortName && port.Port > 0 && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
			// Ignore metadata-only updates, but reconnect when routing or Service identity changes.
			revision := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d:%s", svc.UID, svc.Spec.ClusterIP,
				labels.Set(svc.Spec.Selector).String(), port.TargetPort.Type, port.TargetPort.String())))
			return DiscoveredEndpoint{Address: fmt.Sprintf("%s.%s.svc:%d", svc.Name, svc.Namespace, port.Port),
				Namespace: svc.Namespace, Revision: fmt.Sprintf("%x", revision)}, nil
		}
	}
	return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("the Service of flow source %q has no %s TCP port", d.source, FlowServicePortName))
}

func (d *ServiceDiscovery) update() {
	d.mu.Lock()
	defer d.mu.Unlock()
	endpoint, err := d.resolve()
	signature := fmt.Sprintf("%s|%s|%s|%v", endpoint.Address, endpoint.Namespace, endpoint.Revision, err)
	if signature != d.signature {
		close(d.changed)
		d.changed = make(chan struct{})
		d.signature = signature
	}
}

func (d *ServiceDiscovery) Current() (DiscoveredEndpoint, <-chan struct{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.informer.HasSynced() {
		return DiscoveredEndpoint{}, d.changed, dependencyUnavailable(fmt.Sprintf("waiting for the discovery of flow source %q", d.source))
	}
	endpoint, err := d.resolve()
	return endpoint, d.changed, err
}
