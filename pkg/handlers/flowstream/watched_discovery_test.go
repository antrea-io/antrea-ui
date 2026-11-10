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
	"crypto/tls"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

const discoveredCAName = "flow-aggregator-ca"

// startDiscoveredWatched returns a WatchedSubscriber which discovers its Service and follows the
// CA ConfigMap discoveredCAName in the Service's namespace, running in the current synctest
// bubble, once the discovery has listed the Services.
func startDiscoveredWatched(t *testing.T, client *fake.Clientset) *WatchedSubscriber {
	t.Helper()
	return startDiscoveredWatchedLogging(t, testr.New(t), client)
}

// startDiscoveredWatchedLogging is startDiscoveredWatched with the subscriber logging to logger.
func startDiscoveredWatchedLogging(t *testing.T, logger logr.Logger, client *fake.Clientset) *WatchedSubscriber {
	t.Helper()
	w, err := NewWatchedSubscriber(logger, client, WatchedConfig{
		Name:        "flow-aggregator",
		CAConfigMap: discoveredCAName,
		Discovery:   serverconfig.FlowSourceDiscoveryConfig{Enabled: true},
	})
	require.NoError(t, err)
	stopCh := make(chan struct{})
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		w.Run(stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-runDone
		w.Close()
	})
	settle()
	return w
}

// settle lets the discovery and CA controllers of the current bubble process what they have been
// given: the informers poll for their caches to sync, which takes a little fake time.
func settle() {
	time.Sleep(time.Second)
	synctest.Wait()
}

func createCA(t *testing.T, client *fake.Clientset, namespace string, pem []byte) {
	t.Helper()
	_, err := client.CoreV1().ConfigMaps(namespace).Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: discoveredCAName, Namespace: namespace},
		Data:       map[string]string{flowSourceCAKey: string(pem)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (w *WatchedSubscriber) targetForTest() *watchTarget {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.target
}

// isRetired reports whether sub was retired rather than only closed.
func isRetired(sub *GRPCFlowStreamSubscriber) bool {
	select {
	case <-sub.retired:
		return true
	default:
		return false
	}
}

// subscribeError returns the error of a Subscribe which finds the source unavailable.
func subscribeError(t *testing.T, w *WatchedSubscriber) error {
	t.Helper()
	_, errs, _ := w.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	err, ok := <-errs
	require.True(t, ok, "the source should be unavailable")
	return err
}

func TestDiscoveredSubscriberFollowsTheService(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset()
		createCA(t, client, "networking-a", newTestCA(t))
		createCA(t, client, "networking-b", newTestCA(t))
		w := startDiscoveredWatched(t, client)
		assert.False(t, w.available())
		assert.ErrorContains(t, subscribeError(t, w), "waiting for the Service")

		_, err := client.CoreV1().Services("networking-a").Create(t.Context(), discoveryService("networking-a", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		settle()
		require.True(t, w.available())
		first, generation := w.currentForTest()
		assert.Equal(t, 1, generation)
		assert.Equal(t, "fa.networking-a.svc:14740", first.address)
		oldTarget := w.targetForTest()

		// Moving the Service moves the CA too: the source is rebuilt with the CA of the new
		// namespace, and the old connection is gone.
		require.NoError(t, client.CoreV1().Services("networking-a").Delete(t.Context(), "fa", metav1.DeleteOptions{}))
		_, err = client.CoreV1().Services("networking-b").Create(t.Context(), discoveryService("networking-b", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		settle()
		require.True(t, w.available())
		second, generation := w.currentForTest()
		assert.Equal(t, 2, generation, "generations carry on across targets")
		assert.Equal(t, "fa.networking-b.svc:14740", second.address)
		assert.Equal(t, first.connKeyPrefix, second.connKeyPrefix, "the connection key prefix is the source's, not the target's")
		assert.Equal(t, connectivity.Shutdown, first.conn.GetState())
		assert.True(t, isRetired(first), "a new endpoint ends the streams of client-certificate sessions too")
		assert.NotEqual(t, connectivity.Shutdown, second.conn.GetState())
		assert.False(t, isRetired(second))

		// The CA controller of the namespace it left must not replace what serves the new one.
		targetListener{w: w, t: oldTarget}.Enqueue()
		current, generation := w.currentForTest()
		assert.Same(t, second, current, "a notification of a replaced target must be ignored")
		assert.Equal(t, 2, generation)
	})
}

// This runs on the real clock, not in a synctest bubble: a CA ConfigMap which does not exist makes
// the CA controller back off for a duration derived from the wall clock, which a bubble's fake
// clock turns into one that never ends.
func TestDiscoveredSubscriberWaitsForTheCAOfTheService(t *testing.T) {
	client := fake.NewClientset()
	w, err := NewWatchedSubscriber(testr.New(t), client, WatchedConfig{
		Name:        "flow-aggregator",
		CAConfigMap: discoveredCAName,
		Discovery:   serverconfig.FlowSourceDiscoveryConfig{Enabled: true},
	})
	require.NoError(t, err)
	stopCh := make(chan struct{})
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		w.Run(stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-runDone
		w.Close()
	})
	unavailableWith := func(message string) func() bool {
		return func() bool {
			_, errs, _ := w.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
			err, ok := <-errs
			return ok && strings.Contains(err.Error(), message)
		}
	}
	const wait, tick = 5 * time.Second, 10 * time.Millisecond
	require.Eventually(t, unavailableWith("waiting for the Service"), wait, tick)

	// Nothing connects before the CA of the Service's namespace is loaded.
	_, err = client.CoreV1().Services("networking-a").Create(t.Context(), discoveryService("networking-a", "fa"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, unavailableWith("has not loaded the flow source CA"), wait, tick)
	createCA(t, client, "networking-a", newTestCA(t))
	require.Eventually(t, w.available, wait, tick)
	first, generation := w.currentForTest()
	assert.Equal(t, 1, generation)
	oldTarget := w.targetForTest()

	// Moving the Service makes the source unavailable until the CA of the new namespace is loaded.
	require.NoError(t, client.CoreV1().Services("networking-a").Delete(t.Context(), "fa", metav1.DeleteOptions{}))
	_, err = client.CoreV1().Services("networking-b").Create(t.Context(), discoveryService("networking-b", "fa"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, unavailableWith("has not loaded the flow source CA"), wait, tick)
	assert.Equal(t, connectivity.Shutdown, first.conn.GetState())
	targetListener{w: w, t: oldTarget}.Enqueue()
	assert.False(t, w.available(), "a notification of a replaced target must be ignored")

	createCA(t, client, "networking-b", newTestCA(t))
	require.Eventually(t, w.available, wait, tick)
	second, generation := w.currentForTest()
	assert.Equal(t, 2, generation)
	assert.Equal(t, "fa.networking-b.svc:14740", second.address)
}

func TestDiscoveredSubscriberReplacesTheSubscriberWhenTheServiceRoutesElsewhere(t *testing.T) {
	ca := newTestCA(t)
	synctest.Test(t, func(t *testing.T) {
		svc := discoveryService("networking", "fa")
		svc.Spec.Selector = map[string]string{"app": "fa"}
		client := fake.NewClientset(svc)
		createCA(t, client, "networking", ca)
		w := startDiscoveredWatched(t, client)
		require.True(t, w.available())
		first, generation := w.currentForTest()
		require.Equal(t, 1, generation)
		target := w.targetForTest()

		svc.Annotations = map[string]string{"note": "updated"}
		_, err := client.CoreV1().Services("networking").Update(t.Context(), svc, metav1.UpdateOptions{})
		require.NoError(t, err)
		settle()
		current, generation := w.currentForTest()
		assert.Same(t, first, current, "an update which routes nowhere else keeps the subscriber")
		assert.Equal(t, 1, generation)

		svc.Spec.Selector = map[string]string{"app": "replacement"}
		_, err = client.CoreV1().Services("networking").Update(t.Context(), svc, metav1.UpdateOptions{})
		require.NoError(t, err)
		settle()
		current, generation = w.currentForTest()
		assert.NotSame(t, first, current)
		assert.Equal(t, 2, generation)
		assert.Equal(t, connectivity.Shutdown, first.conn.GetState())
		assert.True(t, isRetired(first), "a Service which routes elsewhere ends the streams of client-certificate sessions too")
		assert.Same(t, target, w.targetForTest(), "the CA of the same namespace is still the one followed")
		assert.Equal(t, current.address, first.address)
	})
}

func TestDiscoveredSubscriberIsUnavailableWhileDiscoveryFails(t *testing.T) {
	ca := newTestCA(t)
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset(discoveryService("networking-a", "fa"))
		createCA(t, client, "networking-a", ca)
		w := startDiscoveredWatched(t, client)
		require.True(t, w.available())
		first, _ := w.currentForTest()

		_, err := client.CoreV1().Services("networking-b").Create(t.Context(), discoveryService("networking-b", "second"), metav1.CreateOptions{})
		require.NoError(t, err)
		settle()
		assert.False(t, w.available())
		assert.Equal(t, connectivity.Shutdown, first.conn.GetState())
		assert.True(t, isRetired(first))
		err = subscribeError(t, w)
		assert.ErrorContains(t, err, "multiple Services match")
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeSourceUnavailable, streamErr.Code)

		require.NoError(t, client.CoreV1().Services("networking-b").Delete(t.Context(), "second", metav1.DeleteOptions{}))
		settle()
		require.True(t, w.available(), "the source recovers once exactly one Service matches")
		second, generation := w.currentForTest()
		assert.NotSame(t, first, second)
		assert.Equal(t, 2, generation)
	})
}

// A source which was available and no longer is must say why in the backend log, once when it
// happens and again as a reminder for as long as it lasts: the error clients see is deliberately
// vague, and nothing else in the log tells the operator.
func TestDiscoveredSubscriberLogsWhyItBecameUnavailable(t *testing.T) {
	ca := newTestCA(t)
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var logs []string
		logger := funcr.New(func(prefix, args string) {
			mu.Lock()
			defer mu.Unlock()
			logs = append(logs, args)
		}, funcr.Options{})
		logged := func(substr string) int {
			mu.Lock()
			defer mu.Unlock()
			n := 0
			for _, l := range logs {
				if strings.Contains(l, substr) {
					n++
				}
			}
			return n
		}
		client := fake.NewClientset(discoveryService("networking-a", "fa"))
		createCA(t, client, "networking-a", ca)
		w := startDiscoveredWatchedLogging(t, logger, client)
		require.True(t, w.available())
		// Available for longer than a reminder interval: no reminder, and none is owed later.
		time.Sleep(2 * caWaitReminderInterval)
		require.Zero(t, logged("still waiting"))

		require.NoError(t, client.CoreV1().Services("networking-a").Delete(t.Context(), "fa", metav1.DeleteOptions{}))
		settle()
		require.False(t, w.available())
		assert.Equal(t, 1, logged(`"reason"="waiting for the Service of flow source`), "the log says why it became unavailable")

		time.Sleep(caWaitReminderInterval)
		synctest.Wait()
		assert.Equal(t, 1, logged("still waiting for the flow source to be discovered"),
			"a source unavailable again is reminded of like one which never was available")
	})
}

func TestDiscoveredSubscriberWithoutACAIsAvailableWithTheService(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset(discoveryService("networking", "fa"))
		w, err := NewWatchedSubscriber(testr.New(t), client, WatchedConfig{
			Name:      "flow-aggregator",
			Discovery: serverconfig.FlowSourceDiscoveryConfig{Enabled: true},
		})
		require.NoError(t, err)
		defer w.Close()
		assert.False(t, w.available(), "nothing is discovered before Run")
		stopCh := make(chan struct{})
		runDone := make(chan struct{})
		go func() {
			defer close(runDone)
			w.Run(stopCh)
		}()
		settle()
		require.True(t, w.available())
		current, _ := w.currentForTest()
		assert.Equal(t, "fa.networking.svc:14740", current.address)
		close(stopCh)
		<-runDone
	})
}

// Retiring a subscriber, unlike closing it, also ends the streams on the connections of
// client-certificate sessions, as the endpoint they were started against is gone. Those
// connections are real TLS ones, so this does not run in a synctest bubble.
func TestRetireEndsStreamsOfCertificateSessions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter *FlowStreamFilter
		// wantErr is whether the stream reports an error as it ends.
		wantErr bool
	}{
		{name: "live stream ends without an error", filter: &FlowStreamFilter{}},
		{name: "finite query fails", filter: finiteFilter(), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ca := testcerts.NewCA()
			kp, err := ca.NewKeyPair("flow.test")
			require.NoError(t, err)
			serverCert, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
			require.NoError(t, err)
			clientKP, err := ca.NewKeyPair("viewer")
			require.NoError(t, err)
			fakeSrv := &fakeFlowStreamServer{handle: func(int, string) error { return nil }}
			fakeSrv.send = func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
				if err := stream.Send(firstResponse); err != nil {
					return err
				}
				<-stream.Context().Done()
				return nil
			}
			sub := newCertTestSubscriber(t, fakeSrv, ca, serverCert, "flow.test")
			sub.retired = make(chan struct{})
			ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{
				Mode:       session.ModeKubeconfig,
				Credential: session.Credential{Kind: session.KindCert, CertPEM: clientKP.PublicKey(), KeyPEM: clientKP.PrivateKey()},
			})
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			flows, errs, ready := sub.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, tc.filter)
			select {
			case <-ready:
			case err := <-errs:
				require.FailNow(t, "stream failed before becoming ready", "%v", err)
			case <-ctx.Done():
				require.FailNow(t, "timed out waiting for the stream to become ready")
			}

			require.NoError(t, sub.Retire())
			for open := true; open; {
				select {
				case _, open = <-flows:
				case <-ctx.Done():
					require.FailNow(t, "timed out waiting for the stream to end")
				}
			}
			select {
			case err, ok := <-errs:
				if tc.wantErr {
					require.True(t, ok, "an interrupted finite query must fail")
					var streamErr *StreamError
					require.ErrorAs(t, err, &streamErr)
					assert.True(t, streamErr.Retryable)
					assert.Contains(t, streamErr.Error(), "canceled the query before completion")
				} else {
					assert.False(t, ok, "retiring must not surface as a live stream error: %v", err)
				}
			case <-ctx.Done():
				require.FailNow(t, "timed out waiting for the stream to end")
			}
		})
	}
}

// A finite query which Retire stops before it completed must fail, wherever it was waiting: a
// query that ends without an error looks like a finished one with fewer results. A live stream
// ends without an error. These use bufconn and the shared connection, so they run in a synctest
// bubble.
func TestRetireFailsFiniteQueriesWhereverTheyWait(t *testing.T) {
	flow := &flowpb.Flow{}
	for _, tc := range []struct {
		name string
		// send is what the flow source does on the stream.
		send func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error
		// retireAfterReady is whether the test waits for the first response before retiring.
		retireAfterReady bool
	}{
		{
			name: "before the first response",
			send: func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
				<-stream.Context().Done()
				return nil
			},
		},
		{
			name: "while handing a batch to a client which does not read",
			send: func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
				if err := stream.Send(firstResponse); err != nil {
					return err
				}
				// More batches than the stream buffers (16), so that forwarding one blocks.
				for range 40 {
					if err := stream.Send(&flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{flow}}); err != nil {
						return err
					}
				}
				<-stream.Context().Done()
				return nil
			},
			retireAfterReady: true,
		},
	} {
		for _, live := range []bool{false, true} {
			name := tc.name + "/finite"
			filter := finiteFilter()
			if live {
				name, filter = tc.name+"/live", &FlowStreamFilter{}
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					fakeSrv := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: tc.send}
					sub := newTestSubscriber(t, fakeSrv)
					sub.retired = make(chan struct{})
					ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{
						Mode:       session.ModeToken,
						Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")},
					})

					_, errs, ready := sub.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, filter)
					synctest.Wait()
					if tc.retireAfterReady {
						select {
						case <-ready:
						default:
							require.FailNow(t, "the stream should be ready before it is retired")
						}
					}
					require.NoError(t, sub.Retire())
					synctest.Wait()
					select {
					case err, ok := <-errs:
						if live {
							assert.False(t, ok, "retiring must not surface as a live stream error: %v", err)
							return
						}
						require.True(t, ok, "an interrupted finite query must fail")
						var streamErr *StreamError
						require.ErrorAs(t, err, &streamErr)
						assert.True(t, streamErr.Retryable)
						assert.Contains(t, streamErr.Error(), "canceled the query before completion")
					default:
						require.FailNow(t, "the stream should have ended")
					}
				})
			})
		}
	}
}

// A client-certificate session keeps its own connection per source, keyed by the source and the
// generation. A new target must supersede the connections of the earlier ones, without touching
// another source's, and a request which still reads the replaced subscriber must not close the
// connection of the new one.
func TestRetargetSupersedesSessionConnectionsOfEarlierTargets(t *testing.T) {
	newWatched := func(discovery bool) *WatchedSubscriber {
		w, err := NewWatchedSubscriber(testr.New(t), fake.NewClientset(), WatchedConfig{
			Name:      "source",
			Address:   "static.example.test:14740",
			Discovery: serverconfig.FlowSourceDiscoveryConfig{Enabled: discovery},
		})
		require.NoError(t, err)
		t.Cleanup(func() { w.Close() })
		return w
	}
	discovered, other, static := newWatched(true), newWatched(true), newWatched(false)
	discovered.retarget(DiscoveredEndpoint{Address: "fa.networking-a.svc:14740", Namespace: "networking-a", Revision: "1"}, nil)
	other.retarget(DiscoveredEndpoint{Address: "history.history-ns.svc:14740", Namespace: "history-ns", Revision: "1"}, nil)

	clientKP, err := testcerts.NewCA().NewKeyPair("viewer")
	require.NoError(t, err)
	store := newTestStore(t)
	ctx, ra := ctxWithSessionAuth(t, store, &session.Spec{
		Mode: session.ModeKubeconfig,
		Credential: session.Credential{
			Kind:    session.KindCert,
			CertPEM: clientKP.PublicKey(),
			KeyPEM:  clientKP.PrivateKey(),
		},
	})
	t.Cleanup(func() { store.Delete(ra.SessionID()) })
	connection := func(sub *GRPCFlowStreamSubscriber) *grpc.ClientConn {
		t.Helper()
		_, _, err := sub.resolveCall(ctx)
		require.NoError(t, err)
		conn, err := ra.ConnFor(flowStreamConnKey(sub.connKeyPrefix, sub.generation),
			func(*session.Credential) (*grpc.ClientConn, func(), error) {
				return nil, nil, errors.New("resolveCall did not cache a connection")
			})
		require.NoError(t, err)
		return conn
	}

	first, _ := discovered.currentForTest()
	staticSub, _ := static.currentForTest()
	otherSub, _ := other.currentForTest()
	firstConn, staticConn, otherConn := connection(first), connection(staticSub), connection(otherSub)

	discovered.retarget(DiscoveredEndpoint{Address: "fa.networking-b.svc:14740", Namespace: "networking-b", Revision: "1"}, nil)
	second, generation := discovered.currentForTest()
	require.Equal(t, 2, generation)
	secondConn := connection(second)
	assert.NotSame(t, firstConn, secondConn)
	assert.Equal(t, connectivity.Shutdown, firstConn.GetState(), "the session replaces the connection of the earlier target")
	assert.NotEqual(t, connectivity.Shutdown, staticConn.GetState(), "another source's connection is left alone")
	assert.NotEqual(t, connectivity.Shutdown, otherConn.GetState(), "another discovering source's connection is left alone")

	_, _, err = first.resolveCall(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, connectivity.Shutdown, secondConn.GetState(), "a late request on the replaced subscriber must not close the new connection")
	assert.Same(t, secondConn, connection(second))
}
