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
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

func historyFilter() *FlowStreamFilter { follow := false; return &FlowStreamFilter{Follow: &follow} }

func TestHistoricalRequestOptions(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?follow=false&since=2026-10-23T12:30:00Z&maxCount=123", nil)
	f, err := parseFlowStreamFilter(c)
	require.NoError(t, err)
	r := filterToGetFlowsRequest(&FlowStreamScope{ObservedNamespace: "team-a"}, f)
	require.False(t, r.Follow)
	require.EqualValues(t, 123, r.MaxCount)
	require.Equal(t, "2026-10-23T12:30:00Z", r.Since.AsTime().Format(time.RFC3339))
	require.Equal(t, []string{"team-a"}, r.Namespaces)
	require.Equal(t, MaxHistoricalSnapshots, filterToGetFlowsRequest(nil, historyFilter()).MaxCount)
	for _, query := range []string{"follow=bad", "since=yesterday", "maxCount=10001", "maxCount=0", "follow=", "since=", "maxCount=", "follow=true&follow=false", "since=0000-01-01T00:00:00Z"} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/?"+query, nil)
			_, err := parseFlowStreamFilter(c)
			require.Error(t, err)
		})
	}
}

type scriptedSubscriber func(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{})

func (s scriptedSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, f *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	return s(ctx, scope, f)
}

// An unbuffered send establishes delivery before the scripted terminal error.
func scriptedEvents(events []apisv1.FlowStreamEvent, terminal error) FlowStreamSubscriber {
	return scriptedSubscriber(func(ctx context.Context, _ *FlowStreamScope, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
		flows := make(chan apisv1.FlowStreamEvent)
		errs := make(chan error, 1)
		ready := make(chan struct{})
		close(ready)
		go func() {
			defer close(errs)
			defer close(flows)
			for _, event := range events {
				select {
				case flows <- event:
				case <-ctx.Done():
					return
				}
			}
			if terminal != nil {
				errs <- terminal
			}
		}()
		return flows, errs, ready
	})
}

func TestHistoricalRouting(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live        bool
		readerError error
		partial     bool
		noReader    bool
	}{
		{name: "live bypasses unavailable reader", live: true, readerError: dependencyUnavailable("offline")},
		{name: "historical completes"},
		{name: "history disabled", noReader: true},
		{name: "unavailable never falls back", readerError: dependencyUnavailable("offline")},
		{name: "authorization never falls back", readerError: &StreamError{Code: StreamErrorCodeForbidden}},
		{name: "partial result never falls back", readerError: dependencyUnavailable("offline"), partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				liveCalls, readerCalls := 0, 0
				wrap := func(counter *int, subscriber FlowStreamSubscriber) FlowStreamSubscriber {
					return scriptedSubscriber(func(ctx context.Context, scope *FlowStreamScope, f *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
						*counter++
						require.Equal(t, tc.live, f.following())
						return subscriber.Subscribe(ctx, scope, f)
					})
				}
				var records []apisv1.FlowStreamEvent
				if tc.partial {
					records = []apisv1.FlowStreamEvent{{Flows: []apisv1.Flow{{ID: "historical"}}}}
				}
				router := &RoutedSubscriber{Live: wrap(&liveCalls, scriptedEvents(nil, nil)), Reader: wrap(&readerCalls, scriptedEvents(records, tc.readerError))}
				if tc.noReader {
					router.Reader = nil
				}
				f := historyFilter()
				if tc.live {
					f.Follow = nil
				}
				var events []apisv1.FlowStreamEvent
				err := relay(t.Context(), router, &FlowStreamScope{ClusterWide: true}, f, func() error { return nil }, func(e apisv1.FlowStreamEvent) error { events = append(events, e); return nil })
				synctest.Wait()
				if tc.live {
					require.Equal(t, 1, liveCalls)
					require.Zero(t, readerCalls)
					require.NoError(t, err)
				} else {
					require.Zero(t, liveCalls)
					if tc.noReader {
						require.Zero(t, readerCalls)
						var se *StreamError
						require.ErrorAs(t, err, &se)
						require.Equal(t, historyDisabledCode, se.Code)
						require.False(t, se.Retryable)
					} else {
						require.Equal(t, 1, readerCalls)
						if tc.readerError != nil {
							require.ErrorIs(t, err, tc.readerError)
						} else {
							require.NoError(t, err)
						}
					}
				}
				for _, event := range events {
					if event.Source != "" {
						if tc.live {
							require.Equal(t, "live", event.Source)
						} else {
							require.Equal(t, "historical", event.Source)
						}
					}
				}
			})
		})
	}
}

func TestHistoricalSSECompletion(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "final error"}[failure], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var terminal error
				if failure {
					terminal = errors.New("query interrupted")
				}
				s := scriptedEvents([]apisv1.FlowStreamEvent{{Flows: []apisv1.Flow{{ID: "one"}}}}, terminal)
				rr := newCloseNotifyRecorder()
				newTestRouter(NewSSEHandler(testr.New(t), s)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true&follow=false", nil))
				if failure {
					require.NotContains(t, rr.Body.String(), "event:complete")
					require.Contains(t, rr.Body.String(), "event:error")
				} else {
					require.Contains(t, rr.Body.String(), "event:complete")
					require.Contains(t, rr.Body.String(), `"snapshotCount":1`)
				}
			})
		})
	}
}

func TestReaderAllowsEmptyHandshakeAndClassifiesUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return stream.Send(&flowpb.GetFlowsResponse{})
		}}
		subscriber := newTestSubscriber(t, server)
		subscriber.historical = true
		ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeToken, Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")}})
		ready := false
		err := relay(ctx, subscriber, &FlowStreamScope{ClusterWide: true}, historyFilter(), func() error { ready = true; return nil }, func(apisv1.FlowStreamEvent) error { t.Fatal("unexpected records"); return nil })
		require.NoError(t, err)
		require.True(t, ready)
		require.Equal(t, dependencyUnavailableCode, subscriber.classifyStreamErr(status.Error(codes.FailedPrecondition, "offline")).Code)
		require.Equal(t, StreamErrorCodeForbidden, subscriber.classifyStreamErr(status.Error(codes.PermissionDenied, "denied")).Code)
	})
}

func TestEndpointReloadsTrustAndSeparatesConnectionCaches(t *testing.T) {
	ctx := t.Context()
	pair, err := testcerts.NewCA().NewKeyPair("flow.test")
	require.NoError(t, err)
	client := fake.NewClientset()
	endpoint := &EndpointSubscriber{Client: client, Address: "reader.trusted.svc:14740", Namespace: "trusted", CAConfigMap: "reader-ca", Historical: true}
	_, err = endpoint.connectionConfig(ctx)
	var unavailable *StreamError
	require.ErrorAs(t, err, &unavailable)
	require.Equal(t, dependencyUnavailableCode, unavailable.Code)
	_, err = client.CoreV1().ConfigMaps("trusted").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "reader-ca"}, Data: map[string]string{"ca.crt": string(pair.PublicKey())}}, metav1.CreateOptions{})
	require.NoError(t, err)
	reader, err := endpoint.connectionConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, reader.TLSConfig.RootCAs)
	require.False(t, reader.TLSConfig.InsecureSkipVerify)
	endpoint.Historical = false
	live, err := endpoint.connectionConfig(ctx)
	require.NoError(t, err)
	require.NotEqual(t, reader.ConnectionKey, live.ConnectionKey)
	replacement, err := testcerts.NewCA().NewKeyPair("flow.test")
	require.NoError(t, err)
	_, err = client.CoreV1().ConfigMaps("trusted").Update(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "reader-ca"}, Data: map[string]string{"ca.crt": string(replacement.PublicKey())}}, metav1.UpdateOptions{})
	require.NoError(t, err)
	rotated, err := endpoint.connectionConfig(ctx)
	require.NoError(t, err)
	require.NotEqual(t, live.ConnectionKey, rotated.ConnectionKey)
}

// This exercises real certificate handshakes, so it uses loopback TCP rather
// than synctest. Completion is synchronized by the stream channels, with a
// context deadline only as a test failure bound.
func TestCertificateSessionUsesSeparateLiveAndReaderConnections(t *testing.T) {
	ca := testcerts.NewCA()
	kp, err := ca.NewKeyPair("flow.test")
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
	require.NoError(t, err)
	clientKP, err := ca.NewKeyPair("viewer")
	require.NoError(t, err)
	faServer := &fakeFlowStreamServer{handle: func(int, string) error { return nil }}
	readerServer := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: func(s grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
		return s.Send(&flowpb.GetFlowsResponse{})
	}}
	fa := newCertTestSubscriber(t, faServer, ca, certificate, "flow.test")
	reader := newCertTestSubscriber(t, readerServer, ca, certificate, "flow.test")
	fa.connectionKey = "flow-aggregator-grpc/test"
	reader.connectionKey = "flow-reader-grpc/test"
	reader.historical = true
	ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeKubeconfig, Credential: session.Credential{Kind: session.KindCert, CertPEM: clientKP.PublicKey(), KeyPEM: clientKP.PrivateKey()}})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, upstream := range []FlowStreamSubscriber{fa, reader} {
		err := relay(ctx, upstream, &FlowStreamScope{ClusterWide: true}, historyFilter(), func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, faServer.calls.Load())
	require.EqualValues(t, 1, readerServer.calls.Load())
}

func TestFiniteCancellationDoesNotReportSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: func(s grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			if err := s.Send(&flowpb.GetFlowsResponse{}); err != nil {
				return err
			}
			return status.Error(codes.Canceled, "reader stopped")
		}}
		subscriber := newTestSubscriber(t, server)
		subscriber.historical = true
		ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeToken, Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")}})
		err := relay(ctx, subscriber, &FlowStreamScope{ClusterWide: true}, historyFilter(), func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
		require.ErrorContains(t, err, "canceled before completion")
	})
}

func TestHistoricalSSERejectsDisabledAndUnavailable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "disabled", false: "unavailable"}[disabled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				liveCalls := 0
				live := scriptedSubscriber(func(ctx context.Context, scope *FlowStreamScope, f *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
					liveCalls++
					return scriptedEvents(nil, nil).Subscribe(ctx, scope, f)
				})
				router := &RoutedSubscriber{Live: live}
				if !disabled {
					router.Reader = scriptedSubscriber(func(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
						errs := make(chan error, 1)
						errs <- dependencyUnavailable("Historical query service is unavailable")
						close(errs)
						flows := make(chan apisv1.FlowStreamEvent)
						close(flows)
						return flows, errs, make(chan struct{})
					})
				}
				rr := newCloseNotifyRecorder()
				newTestRouter(NewSSEHandler(testr.New(t), router)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true&follow=false", nil))
				require.Zero(t, liveCalls)
				if disabled {
					require.Equal(t, 400, rr.Code)
					require.Contains(t, rr.Body.String(), historyDisabledCode)
				} else {
					require.Equal(t, 503, rr.Code)
					require.Contains(t, rr.Body.String(), dependencyUnavailableCode)
				}
				require.NotContains(t, rr.Body.String(), "event:complete")
			})
		})
	}
}

func TestHistoricalResultLimitAcrossBatches(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live        bool
		secondBatch int
		wantCount   int
		wantError   bool
	}{
		{name: "exact limit", secondBatch: 1, wantCount: 3},
		{name: "over limit batch rejected", secondBatch: 2, wantCount: 2, wantError: true},
		{name: "live stream is not capped", live: true, secondBatch: 2, wantCount: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := scriptedEvents([]apisv1.FlowStreamEvent{
					{Flows: make([]apisv1.Flow, 2)},
					{Flows: make([]apisv1.Flow, tc.secondBatch)},
				}, nil)
				router := &RoutedSubscriber{Live: source, Reader: source}
				filter := historyFilter()
				filter.MaxCount = 3
				if tc.live {
					filter.Follow = nil
				}
				received := 0
				err := relay(t.Context(), router, &FlowStreamScope{ClusterWide: true}, filter,
					func() error { return nil },
					func(e apisv1.FlowStreamEvent) error { received += len(e.Flows); return nil })
				if tc.wantError {
					require.ErrorContains(t, err, "exceeded the requested result limit")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.wantCount, received)
			})
		})
	}
}
