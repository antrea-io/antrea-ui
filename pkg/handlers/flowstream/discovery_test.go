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
	"crypto/x509"
	"testing"
	"testing/synctest"

	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func discoveryService(namespace, name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"observability.antrea.io/flow-stream-service": "true"}},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "flowstream-grpc", Port: 14740}}},
	}
}

func TestClusterWideServiceDiscovery(t *testing.T) {
	d, err := NewServiceDiscovery(fake.NewClientset())
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
	d.update()
	changed := d.changed
	second := discoveryService("networking-b", "second")
	require.NoError(t, d.informer.GetStore().Add(second))
	d.update()
	select {
	case <-changed:
	default:
		t.Fatal("ambiguous selection did not invalidate the stream")
	}
	_, err = d.resolve()
	require.ErrorContains(t, err, "networking-a/fa, networking-b/second")
	require.NoError(t, d.informer.GetStore().Delete(first))
	endpoint, err = d.resolve()
	require.NoError(t, err)
	require.Equal(t, "networking-b", endpoint.Namespace)
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
			d, err := NewServiceDiscovery(fake.NewClientset())
			require.NoError(t, err)
			svc := discoveryService("networking", "fa")
			tc.mutate(svc)
			require.NoError(t, d.informer.GetStore().Add(svc))
			_, err = d.resolve()
			require.ErrorContains(t, err, tc.message)
		})
	}
}

func TestDiscoveryWatchesLateServiceAndDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset()
		d, err := NewServiceDiscovery(client)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go d.Run(ctx)
		synctest.Wait()
		_, _, err = d.Current()
		require.ErrorContains(t, err, "waiting")
		svc, err := client.CoreV1().Services("networking-a").Create(ctx, discoveryService("networking-a", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		endpoint, changed, err := d.Current()
		require.NoError(t, err)
		require.Equal(t, "fa.networking-a.svc:14740", endpoint.Address)

		svc.Spec.Ports[0].Port = 24740
		_, err = client.CoreV1().Services(svc.Namespace).Update(ctx, svc, metav1.UpdateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("Service port update did not invalidate the stream")
		}
		endpoint, changed, err = d.Current()
		require.NoError(t, err)
		require.Equal(t, "fa.networking-a.svc:24740", endpoint.Address)
		require.NoError(t, client.CoreV1().Services(svc.Namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{}))
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("Service deletion did not invalidate the stream")
		}
		_, _, err = d.Current()
		require.ErrorContains(t, err, "waiting")
		_, err = client.CoreV1().Services("networking-b").Create(ctx, discoveryService("networking-b", "replacement"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		endpoint, _, err = d.Current()
		require.NoError(t, err)
		require.Equal(t, "replacement.networking-b.svc:14740", endpoint.Address)
		require.Equal(t, "networking-b", endpoint.Namespace)

		for _, action := range client.Actions() {
			if action.GetVerb() == "list" || action.GetVerb() == "watch" {
				require.Empty(t, action.GetNamespace(), "discovery must watch every namespace")
			}
		}
	})
}

func TestDiscoveredEndpointUsesServiceNamespaceCA(t *testing.T) {
	// Generate certificates outside the bubble; only the watch needs fake time.
	pairA, err := testcerts.NewCA().NewKeyPair("fa.networking-a.svc")
	require.NoError(t, err)
	pairB, err := testcerts.NewCA().NewKeyPair("fa.networking-b.svc")
	require.NoError(t, err)
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset(discoveryService("networking-a", "fa"),
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "configured", Name: "custom-ca"}, Data: map[string]string{"ca.crt": "invalid; must never be read"}})
		d, err := NewServiceDiscovery(client)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go d.Run(ctx)
		synctest.Wait()
		subscriber := &EndpointSubscriber{Client: client, Discovery: d, Address: "ignored:14740", Namespace: "configured", CAConfigMap: "custom-ca"}
		_, _, err = subscriber.connectionConfig(ctx)
		require.ErrorContains(t, err, "networking-a/custom-ca")
		createCA := func(ns string, pem []byte) {
			_, err := client.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "custom-ca"}, Data: map[string]string{"ca.crt": string(pem)}}, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		createCA("networking-a", pairA.PublicKey())
		cfgA, changed, err := subscriber.connectionConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, "fa.networking-a.svc:14740", cfgA.Address)
		rootsA := x509.NewCertPool()
		require.True(t, rootsA.AppendCertsFromPEM(pairA.PublicKey()))
		require.True(t, cfgA.TLSConfig.RootCAs.Equal(rootsA))
		require.False(t, cfgA.TLSConfig.InsecureSkipVerify)

		// Moving the only matching Service also changes the source of its CA.
		createCA("networking-b", pairB.PublicKey())
		require.NoError(t, client.CoreV1().Services("networking-a").Delete(ctx, "fa", metav1.DeleteOptions{}))
		_, err = client.CoreV1().Services("networking-b").Create(ctx, discoveryService("networking-b", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("Service relocation did not invalidate the stream")
		}
		cfgB, _, err := subscriber.connectionConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, "fa.networking-b.svc:14740", cfgB.Address)
		rootsB := x509.NewCertPool()
		require.True(t, rootsB.AppendCertsFromPEM(pairB.PublicKey()))
		require.True(t, cfgB.TLSConfig.RootCAs.Equal(rootsB))
		require.False(t, cfgB.TLSConfig.RootCAs.Equal(rootsA))
		require.NotEqual(t, cfgA.ConnectionKey, cfgB.ConnectionKey)
		for _, action := range client.Actions() {
			if action.GetResource().Resource == "configmaps" && action.GetVerb() == "get" {
				require.Contains(t, []string{"networking-a", "networking-b"}, action.GetNamespace())
			}
		}
	})
}

func TestDiscoveryRoutingChangesReconnect(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*corev1.Service)
		reconnect bool
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
				discovery, err := NewServiceDiscovery(client)
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				go discovery.Run(ctx)
				synctest.Wait()
				endpoint := &EndpointSubscriber{Client: client, Discovery: discovery}
				before, changed, err := endpoint.connectionConfig(ctx)
				require.NoError(t, err)

				// Keep a live stream open and wait until relay has observed its acknowledgement.
				source := scriptedSubscriber(func(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
					ready := make(chan struct{})
					close(ready)
					return make(chan apisv1.FlowStreamEvent), make(chan error), ready
				})
				ready := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					done <- relay(ctx, source, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{}, changed,
						func() error { close(ready); return nil }, func(apisv1.FlowStreamEvent) error { return nil })
				}()
				<-ready
				synctest.Wait()
				tc.change(svc)
				_, err = client.CoreV1().Services(svc.Namespace).Update(ctx, svc, metav1.UpdateOptions{})
				require.NoError(t, err)
				synctest.Wait()
				after, _, err := endpoint.connectionConfig(ctx)
				require.NoError(t, err)
				require.Equal(t, before.Address, after.Address, "routing changes keep the public address")
				if tc.reconnect {
					select {
					case err := <-done:
						require.ErrorContains(t, err, "Service changed")
					default:
						t.Fatal("routing change left the old stream active")
					}
					require.NotEqual(t, before.ConnectionKey, after.ConnectionKey, "a retry must not reuse the cached old connection")
				} else {
					select {
					case <-changed:
						t.Fatal("irrelevant update invalidated the stream")
					default:
					}
					require.Equal(t, before.ConnectionKey, after.ConnectionKey)
					cancel()
					require.ErrorIs(t, <-done, context.Canceled)
				}
			})
		})
	}
}
