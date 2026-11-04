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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

type fakeCAProvider struct {
	mu sync.Mutex
	ca []byte
}

func (f *fakeCAProvider) CurrentCABundleContent() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ca
}

func (f *fakeCAProvider) set(ca []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ca = ca
}

func newTestCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newTestWatched(t *testing.T) (*WatchedSubscriber, *fakeCAProvider) {
	t.Helper()
	provider := &fakeCAProvider{}
	w, err := newWatchedSubscriber(testr.New(t), WatchedConfig{
		Address:     "127.0.0.1:0",
		Namespace:   "flow-aggregator",
		CAConfigMap: "flow-aggregator-ca",
	}, provider, nil)
	require.NoError(t, err)
	t.Cleanup(func() { w.Close() })
	return w, provider
}

func (w *WatchedSubscriber) currentForTest() (*GRPCFlowStreamSubscriber, int) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current, w.generation
}

func TestWatchedSubscriberUnavailableUntilCALoaded(t *testing.T) {
	w, _ := newTestWatched(t)

	flows, errs, ready := w.Subscribe(t.Context(), &FlowStreamScope{}, &FlowStreamFilter{})
	err := <-errs
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	assert.True(t, streamErr.Retryable)
	_, open := <-flows
	assert.False(t, open, "flowsCh should be closed")
	_, open = <-errs
	assert.False(t, open, "errCh should be closed after the one error")
	select {
	case <-ready:
		t.Fatal("ready must not be closed on a failure path")
	default:
	}
	assert.NotContains(t, err.Error(), "flow-aggregator-ca", "the cause is logged, not sent to the client")
}

func TestWatchedSubscriberSwapsSubscriberOnCAChange(t *testing.T) {
	w, provider := newTestWatched(t)

	provider.set(newTestCA(t))
	w.Enqueue()
	first, gen := w.currentForTest()
	require.NotNil(t, first)
	assert.Equal(t, 1, gen)

	provider.set(newTestCA(t))
	w.Enqueue()
	second, gen := w.currentForTest()
	require.NotNil(t, second)
	assert.NotSame(t, first, second)
	assert.Equal(t, 2, gen)
	assert.Equal(t, 2, second.generation, "the new subscriber must carry the new generation")
	assert.Equal(t, connectivity.Shutdown, first.conn.GetState(), "the replaced subscriber should be closed")
	assert.NotEqual(t, connectivity.Shutdown, second.conn.GetState())
}

// The CA controller keeps the last good bundle, and does not notify the WatchedSubscriber, when the
// ConfigMap is deleted or holds an invalid bundle. Run against a real controller on a fake
// clientset, so that this is checked rather than assumed.
func TestWatchedSubscriberKeepsCurrentWhenTheConfigMapGoesBad(t *testing.T) {
	const namespace, name = "flow-aggregator", "flow-aggregator-ca"
	tests := []struct {
		name   string
		damage func(t *testing.T, client kubernetes.Interface)
	}{
		{name: "deleted", damage: func(t *testing.T, client kubernetes.Interface) {
			require.NoError(t, client.CoreV1().ConfigMaps(namespace).Delete(t.Context(), name, metav1.DeleteOptions{}))
		}},
		{name: "empty ca.crt", damage: func(t *testing.T, client kubernetes.Interface) {
			setCAConfigMap(t, client, "")
		}},
		{name: "invalid PEM", damage: func(t *testing.T, client kubernetes.Interface) {
			setCAConfigMap(t, client, "not a certificate")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			setCAConfigMap(t, client, string(newTestCA(t)))
			w, err := NewWatchedSubscriber(testr.New(t), client, WatchedConfig{
				Address:     "127.0.0.1:0",
				Namespace:   namespace,
				CAConfigMap: name,
			})
			require.NoError(t, err)
			t.Cleanup(func() { w.Close() })
			stopCh := make(chan struct{})
			runDone := make(chan struct{})
			go func() {
				defer close(runDone)
				w.Run(stopCh)
			}()
			t.Cleanup(func() {
				close(stopCh)
				<-runDone
			})

			// This test runs on the real clock, not in a synctest bubble: the CA controller's
			// error backoff sleeps for a duration derived from the wall clock, which a bubble's
			// fake clock turns into one that never ends.
			require.Eventually(t, w.available, 5*time.Second, 10*time.Millisecond,
				"the bundle in the ConfigMap should have been loaded")
			good, gen := w.currentForTest()
			require.Equal(t, 1, gen)

			tt.damage(t, client)
			assert.Never(t, func() bool {
				current, gen := w.currentForTest()
				return current != good || gen != 1 || good.conn.GetState() == connectivity.Shutdown
			}, 500*time.Millisecond, 10*time.Millisecond,
				"a bad ConfigMap must leave the current subscriber open, without consuming a generation")

			// The watcher is still following the ConfigMap.
			setCAConfigMap(t, client, string(newTestCA(t)))
			require.Eventually(t, func() bool {
				_, gen := w.currentForTest()
				return gen == 2
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func setCAConfigMap(t *testing.T, client kubernetes.Interface, ca string) {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "flow-aggregator", Name: "flow-aggregator-ca"},
		Data:       map[string]string{flowAggregatorCAKey: ca},
	}
	cms := client.CoreV1().ConfigMaps(cm.Namespace)
	if _, err := cms.Update(t.Context(), cm, metav1.UpdateOptions{}); apierrors.IsNotFound(err) {
		_, err = cms.Create(t.Context(), cm, metav1.CreateOptions{})
		require.NoError(t, err)
	} else {
		require.NoError(t, err)
	}
}

func TestWatchedSubscriberInvalidFirstBundleStaysUnavailable(t *testing.T) {
	w, provider := newTestWatched(t)
	provider.set([]byte("not a certificate"))
	w.Enqueue()
	assert.False(t, w.available())

	provider.set(newTestCA(t))
	w.Enqueue()
	assert.True(t, w.available(), "a valid bundle arriving later makes flow visibility available")
}

func TestWatchedSubscriberBuildsOnceWithoutAConfigMap(t *testing.T) {
	tests := []struct {
		name string
		cfg  WatchedConfig
	}{
		{name: "no ConfigMap", cfg: WatchedConfig{Address: "127.0.0.1:0"}},
		{name: "insecure skip verify", cfg: WatchedConfig{
			Address: "127.0.0.1:0", CAConfigMap: "flow-aggregator-ca", InsecureSkipVerify: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, err := NewWatchedSubscriber(testr.New(t), nil, tt.cfg)
			require.NoError(t, err)
			t.Cleanup(func() { w.Close() })
			current, gen := w.currentForTest()
			assert.NotNil(t, current)
			assert.Equal(t, 1, gen)
			w.Run(nil) // Must return right away: there is nothing to watch.
		})
	}
}

func TestWatchedSubscriberClose(t *testing.T) {
	w, provider := newTestWatched(t)
	provider.set(newTestCA(t))
	w.Enqueue()
	current, _ := w.currentForTest()
	require.NotNil(t, current)

	require.NoError(t, w.Close())
	assert.Equal(t, connectivity.Shutdown, current.conn.GetState())
	assert.False(t, w.available())

	// A bundle arriving after Close must not resurrect a subscriber nobody will close.
	provider.set(newTestCA(t))
	w.Enqueue()
	assert.False(t, w.available())
}

// Once a CA is loaded, Subscribe goes to the subscriber built for it.
func TestWatchedSubscriberSubscribeDelegatesToTheCurrentSubscriber(t *testing.T) {
	w, provider := newTestWatched(t)
	provider.set(newTestCA(t))
	w.Enqueue()

	// Point the current subscriber at a fake server: the one built from the CA dials a real
	// address with TLS.
	var calls atomic.Int32
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		calls.Add(1)
		return nil
	}}
	fakeSub := newTestSubscriber(t, fake)
	w.mu.Lock()
	w.current.Close()
	w.current = fakeSub
	w.mu.Unlock()

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	_, errCh, readyCh := w.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	// The fake server ends the stream immediately after its first response. Wait for that normal
	// close before checking readiness, so two closed channels cannot race in the same select.
	select {
	case err, ok := <-errCh:
		require.False(t, ok, "unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for stream to finish")
	}
	select {
	case <-readyCh:
	default:
		t.Fatal("stream finished without becoming ready")
	}
	assert.Equal(t, int32(1), calls.Load())
}

// Two sources can have the same address and CA generation but still own independent connections.
// Rotating either source must leave the other source's cached connection usable.
// A CA rotation closes the connection that bearer-authenticated streams run on. A stream that is
// already established must end like an ordinary disconnect, with no error, so that the client
// reconnects instead of showing an error.
func TestWatchedSubscriberRotationEndsLiveStreamWithoutError(t *testing.T) {
	w, provider := newTestWatched(t)
	provider.set(newTestCA(t))
	w.Enqueue()

	fakeSrv := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	fakeSrv.send = func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
		if err := stream.Send(firstResponse); err != nil {
			return err
		}
		<-stream.Context().Done()
		return nil
	}
	fakeSub := newTestSubscriber(t, fakeSrv)
	w.mu.Lock()
	w.current.Close()
	w.current = fakeSub
	w.mu.Unlock()

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	flowsCh, errCh, readyCh := w.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case <-readyCh:
	case err := <-errCh:
		require.FailNow(t, "stream failed before becoming ready", "%v", err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for the stream to become ready")
	}

	provider.set(newTestCA(t))
	w.Enqueue()

	for range flowsCh {
	}
	select {
	case err, ok := <-errCh:
		assert.False(t, ok, "a CA rotation must not surface as a stream error: %v", err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out waiting for the stream to end")
	}
}

func TestWatchedSubscriberIsolatesSessionConnectionsAcrossSources(t *testing.T) {
	firstSource, firstProvider := newTestWatched(t)
	secondSource, secondProvider := newTestWatched(t)
	ca := newTestCA(t)
	firstProvider.set(ca)
	firstSource.Enqueue()
	secondProvider.set(ca)
	secondSource.Enqueue()

	clientCA := testcerts.NewCA()
	clientKP, err := clientCA.NewKeyPair("flow-viewer-client")
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
	connection := func(w *WatchedSubscriber) *grpc.ClientConn {
		t.Helper()
		sub, _ := w.currentForTest()
		require.NotNil(t, sub)
		_, _, err := sub.resolveCall(ctx)
		require.NoError(t, err)
		conn, err := ra.ConnFor(flowStreamConnKey(sub.connKeyPrefix, sub.generation),
			func(*session.Credential) (*grpc.ClientConn, func(), error) {
				return nil, nil, errors.New("resolveCall did not cache a connection")
			})
		require.NoError(t, err)
		return conn
	}

	first := connection(firstSource)
	second := connection(secondSource)
	require.NotSame(t, first, second, "different sources must not share a session connection")
	assert.Same(t, first, connection(firstSource), "requests to one source should reuse its connection")

	firstProvider.set(newTestCA(t))
	firstSource.Enqueue()
	firstRotated := connection(firstSource)
	assert.NotSame(t, first, firstRotated)
	assert.Equal(t, connectivity.Shutdown, first.GetState())
	assert.NotEqual(t, connectivity.Shutdown, second.GetState(), "rotating the first source must leave the second open")
	assert.Same(t, second, connection(secondSource))

	secondProvider.set(newTestCA(t))
	secondSource.Enqueue()
	secondRotated := connection(secondSource)
	assert.NotSame(t, second, secondRotated)
	assert.Equal(t, connectivity.Shutdown, second.GetState())
	assert.NotEqual(t, connectivity.Shutdown, firstRotated.GetState(), "rotating the second source must leave the first open")
	assert.Same(t, firstRotated, connection(firstSource))
}

func TestWatchedSubscriberLogsUntilACABundleIsLoaded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var waiting int
		logger := funcr.New(func(_, args string) {
			if strings.Contains(args, "still waiting") {
				mu.Lock()
				waiting++
				mu.Unlock()
			}
		}, funcr.Options{})
		reminders := func() int {
			mu.Lock()
			defer mu.Unlock()
			return waiting
		}

		provider := &fakeCAProvider{}
		w, err := newWatchedSubscriber(logger, WatchedConfig{
			Address:     "127.0.0.1:0",
			CAConfigMap: "flow-aggregator-ca",
		}, provider, func(ctx context.Context) { <-ctx.Done() })
		require.NoError(t, err)
		defer w.Close()

		stopCh := make(chan struct{})
		runDone := make(chan struct{})
		go func() {
			defer close(runDone)
			w.Run(stopCh)
		}()

		time.Sleep(caWaitReminderInterval - time.Second)
		synctest.Wait()
		assert.Zero(t, reminders(), "the first reminder is due after a full interval")

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, 1, reminders())
		time.Sleep(caWaitReminderInterval)
		synctest.Wait()
		assert.Equal(t, 2, reminders(), "the reminder repeats while there is no bundle")

		provider.set(newTestCA(t))
		w.Enqueue()
		time.Sleep(3 * caWaitReminderInterval)
		synctest.Wait()
		assert.Equal(t, 2, reminders(), "no reminder once a bundle is loaded")

		close(stopCh)
		<-runDone
	})
}
