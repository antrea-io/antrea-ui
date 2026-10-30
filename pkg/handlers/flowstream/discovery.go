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
)

const FlowServiceSelector = "observability.antrea.io/flow-stream-service=true"

// ServiceDiscovery watches labeled Flow Aggregator Services across the cluster.
// Deployments must restrict matching Services and their CA ConfigMaps to trusted administrators.
type ServiceDiscovery struct {
	informer  cache.SharedIndexInformer
	mu        sync.Mutex
	signature string
	changed   chan struct{}
}

func NewServiceDiscovery(client kubernetes.Interface) (*ServiceDiscovery, error) {
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) { options.LabelSelector = FlowServiceSelector }))
	d := &ServiceDiscovery{informer: factory.Core().V1().Services().Informer(), changed: make(chan struct{})}
	_, err := d.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { d.update() }, UpdateFunc: func(any, any) { d.update() }, DeleteFunc: func(any) { d.update() },
	})
	return d, err
}

func (d *ServiceDiscovery) Run(ctx context.Context) { d.informer.Run(ctx.Done()) }

// DiscoveredEndpoint keeps the service address and its CA namespace together.
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
		if svc.Labels["observability.antrea.io/flow-stream-service"] == "true" {
			matches = append(matches, svc)
		}
	}
	if len(matches) == 0 {
		return DiscoveredEndpoint{}, dependencyUnavailable("waiting for the Flow Aggregator Service")
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, svc := range matches {
			names = append(names, svc.Namespace+"/"+svc.Name)
		}
		sort.Strings(names)
		return DiscoveredEndpoint{}, dependencyUnavailable("multiple Flow Aggregator Services match: " + strings.Join(names, ", "))
	}
	svc := matches[0]
	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return DiscoveredEndpoint{}, dependencyUnavailable("Flow Aggregator Service must not be ExternalName")
	}
	for _, port := range svc.Spec.Ports {
		if port.Name == "flowstream-grpc" && port.Port > 0 && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
			// Ignore metadata-only updates, but reconnect when routing or Service identity changes.
			revision := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d:%s", svc.UID, svc.Spec.ClusterIP,
				labels.Set(svc.Spec.Selector).String(), port.TargetPort.Type, port.TargetPort.String())))
			return DiscoveredEndpoint{Address: fmt.Sprintf("%s.%s.svc:%d", svc.Name, svc.Namespace, port.Port),
				Namespace: svc.Namespace, Revision: fmt.Sprintf("%x", revision)}, nil
		}
	}
	return DiscoveredEndpoint{}, dependencyUnavailable("Flow Aggregator Service has no flowstream-grpc TCP port")
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
		return DiscoveredEndpoint{}, d.changed, dependencyUnavailable("waiting for Flow Aggregator discovery")
	}
	endpoint, err := d.resolve()
	return endpoint, d.changed, err
}
