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
	"sync"

	"github.com/go-logr/logr"
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
// source which enabled discovery, and reports the endpoint of the Service it finds, or why there
// is none. Deployments must restrict matching Services and their CA ConfigMaps to trusted
// administrators.
type ServiceDiscovery struct {
	// logger already carries the name of the flow source.
	logger logr.Logger
	// source is the name of the flow source, for messages.
	source string
	// selector selects the Services of the source. It is also applied to the cached Services,
	// since the watch alone is not a guarantee for every client.
	selector labels.Selector
	informer cache.SharedIndexInformer
	// onChange receives the endpoint, or the error saying why there is none, each time either
	// changes. Calls are serialized and in order.
	onChange func(DiscoveredEndpoint, error)
	// mu serializes the calls of onChange. signature is what the last one reported.
	mu        sync.Mutex
	reported  bool
	signature string
}

// NewServiceDiscovery returns the discovery of the Service of flow source. selector is a label
// selector, and FlowServiceSelector when empty. Once Run has the Services of the cluster, onChange
// is called with what they resolve to, and again whenever that changes: another address,
// namespace, or revision (see DiscoveredEndpoint), or another reason for there being no endpoint.
// It is not called for updates that leave all of these as they were.
func NewServiceDiscovery(logger logr.Logger, client kubernetes.Interface, source, selector string, onChange func(DiscoveredEndpoint, error)) (*ServiceDiscovery, error) {
	if selector == "" {
		selector = FlowServiceSelector
	}
	parsed, err := labels.Parse(selector)
	if err != nil {
		return nil, fmt.Errorf("invalid label selector %q: %w", selector, err)
	}
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) { options.LabelSelector = parsed.String() }))
	d := &ServiceDiscovery{logger: logger, source: source, selector: parsed, informer: factory.Core().V1().Services().Informer(), onChange: onChange}
	_, err = d.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(any) { d.update() }, UpdateFunc: func(any, any) { d.update() }, DeleteFunc: func(any) { d.update() },
	})
	return d, err
}

// Run watches the Services until ctx is done. It reports the first resolution as soon as the
// Services have been listed, including when no Service matches.
func (d *ServiceDiscovery) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.informer.Run(ctx.Done())
	}()
	if cache.WaitForCacheSync(ctx.Done(), d.informer.HasSynced) {
		d.update()
	}
	<-done
}

// DiscoveredEndpoint keeps the Service address and its CA namespace together.
type DiscoveredEndpoint struct {
	Address   string
	Namespace string
	// Revision changes when the Service may route the same address to different backends: when its
	// identity, cluster IP, selector or target port change.
	Revision string
}

// matching returns the cached Services which the selector matches, sorted by namespace and name.
func (d *ServiceDiscovery) matching() []*corev1.Service {
	var matches []*corev1.Service
	for _, obj := range d.informer.GetStore().List() {
		svc := obj.(*corev1.Service)
		if d.selector.Matches(labels.Set(svc.Labels)) {
			matches = append(matches, svc)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Namespace != matches[j].Namespace {
			return matches[i].Namespace < matches[j].Namespace
		}
		return matches[i].Name < matches[j].Name
	})
	return matches
}

func (d *ServiceDiscovery) resolve() (DiscoveredEndpoint, error) {
	return d.resolveMatches(d.matching())
}

// resolveMatches resolves the endpoint of the Service among matches. The errors it returns reach
// every user who opens a flow stream, so they say what is wrong with the source but never name the
// Services found: update logs those.
func (d *ServiceDiscovery) resolveMatches(matches []*corev1.Service) (DiscoveredEndpoint, error) {
	if len(matches) == 0 {
		return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("waiting for the Service of flow source %q", d.source))
	}
	if len(matches) > 1 {
		return DiscoveredEndpoint{}, dependencyUnavailable(fmt.Sprintf("multiple Services match flow source %q", d.source))
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

// update reports the current resolution if it differs from the last one reported. It does
// nothing until the Services have been listed, so that no partial list is ever reported.
func (d *ServiceDiscovery) update() {
	if !d.informer.HasSynced() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	matches := d.matching()
	endpoint, err := d.resolveMatches(matches)
	signature := fmt.Sprintf("%s|%s|%s|%v", endpoint.Address, endpoint.Namespace, endpoint.Revision, err)
	if d.reported && signature == d.signature {
		return
	}
	d.reported, d.signature = true, signature
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, svc := range matches {
			names = append(names, svc.Namespace+"/"+svc.Name)
		}
		d.logger.Info("Several Services match the flow source's label selector, so it is unavailable "+
			"until exactly one does", "selector", d.selector.String(), "services", names)
	}
	d.onChange(endpoint, err)
}
