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
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func discoveryService(namespace, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"observability.antrea.io/flow-stream-service": "true"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "flowstream-grpc", Port: 14740}}},
	}
}

type discoveryReport struct {
	endpoint DiscoveredEndpoint
	err      error
}

// discoveryRecorder collects what a ServiceDiscovery reports.
type discoveryRecorder struct {
	mu      sync.Mutex
	reports []discoveryReport
}

func (r *discoveryRecorder) record(endpoint DiscoveredEndpoint, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, discoveryReport{endpoint, err})
}

func (r *discoveryRecorder) all() []discoveryReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]discoveryReport(nil), r.reports...)
}

// runDiscovery starts a discovery of the Services in client in a synctest bubble, and waits until
// it has reported the first resolution.
func runDiscovery(t *testing.T, client *fake.Clientset, selector string) *discoveryRecorder {
	t.Helper()
	return runDiscoveryLogging(t, testr.New(t), client, selector)
}

// runDiscoveryLogging is runDiscovery with the discovery logging to logger.
func runDiscoveryLogging(t *testing.T, logger logr.Logger, client *fake.Clientset, selector string) *discoveryRecorder {
	t.Helper()
	recorder := &discoveryRecorder{}
	d, err := NewServiceDiscovery(logger, client, "flow-aggregator", selector, recorder.record)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	// Run waits for the Services to be listed by polling, which the bubble's clock lets pass.
	time.Sleep(time.Second)
	synctest.Wait()
	return recorder
}

func TestClusterWideServiceDiscovery(t *testing.T) {
	d, err := NewServiceDiscovery(testr.New(t), fake.NewClientset(), "flow-aggregator", "", nil)
	require.NoError(t, err)
	_, err = d.resolve()
	require.ErrorContains(t, err, "waiting")
	unrelated := discoveryService("other", "unlabeled")
	unrelated.Labels = nil
	require.NoError(t, d.informer.GetStore().Add(unrelated))
	_, err = d.resolve()
	require.ErrorContains(t, err, "waiting")

	first := discoveryService("networking-a", "fa")
	require.NoError(t, d.informer.GetStore().Add(first))
	endpoint, err := d.resolve()
	require.NoError(t, err)
	require.Equal(t, "fa.networking-a.svc:14740", endpoint.Address)
	require.Equal(t, "networking-a", endpoint.Namespace)
	second := discoveryService("networking-b", "second")
	require.NoError(t, d.informer.GetStore().Add(second))
	_, err = d.resolve()
	require.ErrorContains(t, err, "multiple Services match")
	require.NoError(t, d.informer.GetStore().Delete(first))
	endpoint, err = d.resolve()
	require.NoError(t, err)
	require.Equal(t, "networking-b", endpoint.Namespace)
}

// Every user who opens a flow stream sees the error of a source with several matching Services, and
// may not be allowed to read those Services, so the error must not name them. The backend log does.
func TestDiscoveryLogsTheMatchingServicesButDoesNotReportThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var logs []string
		logger := funcr.New(func(prefix, args string) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, prefix+" "+args)
		}, funcr.Options{})
		client := fake.NewClientset(discoveryService("networking-a", "fa"), discoveryService("networking-b", "second"))
		recorder := runDiscoveryLogging(t, logger, client, "")

		reports := recorder.all()
		require.Len(t, reports, 1)
		require.ErrorContains(t, reports[0].err, "multiple Services match")
		for _, name := range []string{"networking-a", "networking-b", "fa", "second"} {
			assert.NotContains(t, reports[0].err.Error(), name)
		}
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, logs, 1)
		assert.Contains(t, logs[0], "networking-a/fa")
		assert.Contains(t, logs[0], "networking-b/second")
	})
}

func TestDiscoveryUsesConfiguredSelector(t *testing.T) {
	upstream := discoveryService("networking-a", "upstream")
	custom := discoveryService("networking-b", "custom")
	custom.Labels = map[string]string{"app": "flow-aggregator", "role": "visibility"}
	for _, tc := range []struct {
		name     string
		selector string
		// want is the namespace of the one Service the selector picks, or empty for none.
		want string
	}{
		{name: "empty selects the label Antrea's chart sets", selector: "", want: "networking-a"},
		{name: "default selector spelled out", selector: FlowServiceSelector, want: "networking-a"},
		{name: "a custom selector replaces the default", selector: "app=flow-aggregator", want: "networking-b"},
		{name: "set-based selector", selector: "role in (visibility, other),!legacy", want: "networking-b"},
		{name: "a selector nothing matches", selector: "app=missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Both Services are cached, so this checks the matching, not the server-side filter.
			d, err := NewServiceDiscovery(testr.New(t), fake.NewClientset(), "flow-aggregator", tc.selector, nil)
			require.NoError(t, err)
			require.NoError(t, d.informer.GetStore().Add(upstream.DeepCopy()))
			require.NoError(t, d.informer.GetStore().Add(custom.DeepCopy()))
			endpoint, err := d.resolve()
			if tc.want == "" {
				require.ErrorContains(t, err, "waiting for the Service")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, endpoint.Namespace)
		})
	}
}

func TestDiscoveryAsksTheServerForTheConfiguredSelector(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		custom := discoveryService("networking-b", "custom")
		custom.Labels = map[string]string{"app": "flow-aggregator"}
		client := fake.NewClientset(discoveryService("networking-a", "upstream"), custom)
		recorder := runDiscovery(t, client, "app=flow-aggregator")
		// Only the Service the selector names was cached, rather than every Service the
		// client could see.
		reports := recorder.all()
		require.Len(t, reports, 1)
		require.NoError(t, reports[0].err)
		require.Equal(t, "networking-b", reports[0].endpoint.Namespace)
		listed := false
		for _, action := range client.Actions() {
			if action.GetVerb() == "list" {
				listed = true
				require.Equal(t, "app=flow-aggregator", action.(k8stesting.ListAction).GetListRestrictions().Labels.String())
			}
		}
		require.True(t, listed)
	})
}

func TestNewServiceDiscoveryRejectsInvalidSelector(t *testing.T) {
	_, err := NewServiceDiscovery(testr.New(t), fake.NewClientset(), "flow-aggregator", "app in (a", nil)
	require.ErrorContains(t, err, `invalid label selector "app in (a"`)
}

func TestDiscoveryRejectsInvalidService(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*corev1.Service)
		message string
	}{
		{"external name", func(s *corev1.Service) {
			s.Spec.Type = corev1.ServiceTypeExternalName
			s.Spec.ExternalName = "external.example"
		}, "must not be ExternalName"},
		{"wrong port name", func(s *corev1.Service) { s.Spec.Ports[0].Name = "grpc" }, "no flowstream-grpc TCP port"},
		{"UDP port", func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolUDP }, "no flowstream-grpc TCP port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewServiceDiscovery(testr.New(t), fake.NewClientset(), "flow-aggregator", "", nil)
			require.NoError(t, err)
			svc := discoveryService("networking", "fa")
			tc.mutate(svc)
			require.NoError(t, d.informer.GetStore().Add(svc))
			_, err = d.resolve()
			require.ErrorContains(t, err, tc.message)
		})
	}
}

// With no Service at all, discovery still reports once the Services have been listed, so that
// the source can say what it is waiting for.
func TestDiscoveryReportsWhenNothingMatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		recorder := runDiscovery(t, fake.NewClientset(), "")
		reports := recorder.all()
		require.Len(t, reports, 1)
		require.ErrorContains(t, reports[0].err, "waiting for the Service")
	})
}

func TestDiscoveryReportsLateServiceAndDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset()
		recorder := runDiscovery(t, client, "")
		require.Len(t, recorder.all(), 1)
		ctx := t.Context()

		svc, err := client.CoreV1().Services("networking-a").Create(ctx, discoveryService("networking-a", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		reports := recorder.all()
		require.Len(t, reports, 2)
		require.NoError(t, reports[1].err)
		require.Equal(t, "fa.networking-a.svc:14740", reports[1].endpoint.Address)

		svc.Spec.Ports[0].Port = 24740
		_, err = client.CoreV1().Services(svc.Namespace).Update(ctx, svc, metav1.UpdateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		reports = recorder.all()
		require.Len(t, reports, 3)
		require.Equal(t, "fa.networking-a.svc:24740", reports[2].endpoint.Address)

		require.NoError(t, client.CoreV1().Services(svc.Namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{}))
		synctest.Wait()
		reports = recorder.all()
		require.Len(t, reports, 4)
		require.ErrorContains(t, reports[3].err, "waiting for the Service")

		_, err = client.CoreV1().Services("networking-b").Create(ctx, discoveryService("networking-b", "replacement"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		reports = recorder.all()
		require.Len(t, reports, 5)
		require.NoError(t, reports[4].err)
		require.Equal(t, "replacement.networking-b.svc:14740", reports[4].endpoint.Address)
		require.Equal(t, "networking-b", reports[4].endpoint.Namespace)

		for _, action := range client.Actions() {
			if action.GetVerb() == "list" || action.GetVerb() == "watch" {
				require.Empty(t, action.GetNamespace(), "discovery must watch every namespace")
			}
		}
	})
}

func TestDiscoveryReportsAmbiguousMatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := discoveryService("networking-a", "fa")
		client := fake.NewClientset(first)
		recorder := runDiscovery(t, client, "")
		require.Len(t, recorder.all(), 1)

		_, err := client.CoreV1().Services("networking-b").Create(t.Context(), discoveryService("networking-b", "second"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		reports := recorder.all()
		require.Len(t, reports, 2)
		require.ErrorContains(t, reports[1].err, "multiple Services match")

		require.NoError(t, client.CoreV1().Services("networking-a").Delete(t.Context(), "fa", metav1.DeleteOptions{}))
		synctest.Wait()
		reports = recorder.all()
		require.Len(t, reports, 3)
		require.NoError(t, reports[2].err)
		require.Equal(t, "networking-b", reports[2].endpoint.Namespace)
	})
}

func TestDiscoveryReportsRoutingChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*corev1.Service)
		// reported is whether the change reaches the source.
		reported bool
	}{
		{"selector", func(s *corev1.Service) { s.Spec.Selector["app"] = "replacement" }, true},
		{"numeric target port", func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.FromInt32(24740) }, true},
		{"named target port", func(s *corev1.Service) { s.Spec.Ports[0].TargetPort = intstr.FromString("new-grpc") }, true},
		{"recreated service", func(s *corev1.Service) { s.UID = "replacement-uid" }, true},
		{"annotation", func(s *corev1.Service) { s.Annotations = map[string]string{"note": "updated"} }, false},
		{"selector order", func(s *corev1.Service) { s.Spec.Selector = map[string]string{"role": "visibility", "app": "fa"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc := discoveryService("networking", "fa")
				svc.UID = "original-uid"
				svc.Spec.Selector = map[string]string{"app": "fa", "role": "visibility"}
				svc.Spec.Ports[0].TargetPort = intstr.FromInt32(14740)
				client := fake.NewClientset(svc)
				recorder := runDiscovery(t, client, "")
				require.Len(t, recorder.all(), 1)

				tc.change(svc)
				_, err := client.CoreV1().Services(svc.Namespace).Update(t.Context(), svc, metav1.UpdateOptions{})
				require.NoError(t, err)
				synctest.Wait()
				reports := recorder.all()
				if !tc.reported {
					require.Len(t, reports, 1, "an update which changes nothing the source connects to is not reported")
					return
				}
				require.Len(t, reports, 2)
				require.Equal(t, reports[0].endpoint.Address, reports[1].endpoint.Address, "routing changes keep the public address")
				require.NotEqual(t, reports[0].endpoint.Revision, reports[1].endpoint.Revision)
			})
		})
	}
}
