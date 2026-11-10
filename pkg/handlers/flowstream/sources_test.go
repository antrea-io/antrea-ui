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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

func finiteFilter() *FlowStreamFilter { follow := false; return &FlowStreamFilter{Follow: &follow} }

func TestFiniteRequestOptions(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?source=flow-history&follow=false&since=2026-10-23T12:30:00Z&maxCount=123", nil)
	f, err := parseFlowStreamFilter(c)
	require.NoError(t, err)
	assert.Equal(t, "flow-history", f.Source)
	r := filterToGetFlowsRequest(&FlowStreamScope{ObservedNamespace: "team-a"}, f)
	require.False(t, r.Follow)
	// One record more than the limit, to tell a truncated result from one that fits.
	require.EqualValues(t, 124, r.MaxCount)
	require.Equal(t, "2026-10-23T12:30:00Z", r.Since.AsTime().Format(time.RFC3339))
	require.Equal(t, []string{"team-a"}, r.Namespaces)
	require.Equal(t, MaxFiniteSnapshots+1, filterToGetFlowsRequest(nil, finiteFilter()).MaxCount)
	for _, query := range []string{
		"follow=bad", "since=yesterday", "maxCount=1000001", "maxCount=0", "follow=", "since=", "maxCount=",
		"follow=true&follow=false", "maxCount=10", "follow=true&maxCount=10", "since=0000-01-01T00:00:00Z", "source=", "source=a&source=b",
	} {
		t.Run(query, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/?"+query, nil)
			_, err := parseFlowStreamFilter(c)
			require.Error(t, err)
		})
	}
}

func TestLiveRequestDefaults(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	f, err := parseFlowStreamFilter(c)
	require.NoError(t, err)
	assert.Empty(t, f.Source)
	r := filterToGetFlowsRequest(nil, f)
	assert.True(t, r.Follow)
	assert.Zero(t, r.MaxCount)
	assert.Nil(t, r.Since)
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

func TestSourceRouting(t *testing.T) {
	live := func() *FlowStreamFilter { return &FlowStreamFilter{} }
	finite := func() *FlowStreamFilter { return finiteFilter() }
	named := func(name string, f *FlowStreamFilter) *FlowStreamFilter { f.Source = name; return f }
	watchOnly := func(name string) FlowSource { return FlowSource{Name: name, Watch: true} }
	listOnly := func(name string) FlowSource { return FlowSource{Name: name, List: true} }
	both := func(name string) FlowSource { return FlowSource{Name: name, Watch: true, List: true} }

	for _, tc := range []struct {
		name    string
		sources []FlowSource
		filter  *FlowStreamFilter
		// want is the source that serves the request, or empty when it is rejected.
		want string
		// wantCode is the code of the error returned.
		wantCode string
		// sourceErr is returned by the serving source.
		sourceErr error
	}{
		{name: "live default is the first watch source", sources: []FlowSource{listOnly("hist"), watchOnly("fa"), both("other")}, filter: live(), want: "fa"},
		{name: "finite default is the first list source", sources: []FlowSource{watchOnly("fa"), listOnly("hist"), both("other")}, filter: finite(), want: "hist"},
		{name: "a source declaring both serves both", sources: []FlowSource{both("all")}, filter: finite(), want: "all"},
		{name: "named live source", sources: []FlowSource{both("a"), both("b")}, filter: named("b", live()), want: "b"},
		{name: "named finite source", sources: []FlowSource{both("a"), both("b")}, filter: named("b", finite()), want: "b"},
		{name: "named source without the capability", sources: []FlowSource{watchOnly("fa"), listOnly("hist")}, filter: named("fa", finite()), wantCode: StreamErrorCodeSourceUnsupported},
		{name: "named source, other capability", sources: []FlowSource{watchOnly("fa"), listOnly("hist")}, filter: named("hist", live()), wantCode: StreamErrorCodeSourceUnsupported},
		{name: "unknown source", sources: []FlowSource{both("a")}, filter: named("missing", live()), wantCode: StreamErrorCodeSourceUnsupported},
		{name: "no source can list", sources: []FlowSource{watchOnly("fa")}, filter: finite(), wantCode: StreamErrorCodeSourceNotServed},
		{name: "no source can watch", sources: []FlowSource{listOnly("hist")}, filter: live(), wantCode: StreamErrorCodeSourceNotServed},
		{name: "unavailable never falls back", sources: []FlowSource{listOnly("hist"), listOnly("spare")}, filter: finite(), want: "hist",
			sourceErr: dependencyUnavailable("offline"), wantCode: StreamErrorCodeSourceUnavailable},
		{name: "authorization never falls back", sources: []FlowSource{both("a"), both("b")}, filter: live(), want: "a",
			sourceErr: &StreamError{Code: StreamErrorCodeForbidden}, wantCode: StreamErrorCodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := map[string]int{}
				sources := make([]FlowSource, len(tc.sources))
				for i, source := range tc.sources {
					terminal := error(nil)
					if source.Name == tc.want {
						terminal = tc.sourceErr
					}
					sources[i] = source
					sources[i].Subscriber = scriptedSubscriber(func(ctx context.Context, scope *FlowStreamScope, f *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
						calls[source.Name]++
						return scriptedEvents(nil, terminal).Subscribe(ctx, scope, f)
					})
				}
				router := &RoutedSubscriber{Sources: sources}
				err := relay(t.Context(), router, &FlowStreamScope{ClusterWide: true}, tc.filter, func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
				synctest.Wait()
				if tc.wantCode == "" {
					require.NoError(t, err)
				} else {
					var se *StreamError
					require.ErrorAs(t, err, &se)
					require.Equal(t, tc.wantCode, se.Code)
					if tc.wantCode == StreamErrorCodeSourceUnsupported || tc.wantCode == StreamErrorCodeSourceNotServed {
						require.False(t, se.Retryable)
					}
				}
				wantCalls := map[string]int{}
				if tc.want != "" {
					wantCalls[tc.want] = 1
				}
				require.Equal(t, wantCalls, calls)
			})
		})
	}
}

func TestSSECompletion(t *testing.T) {
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

func TestSSERejectsUnsupportedAndUnavailableSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		wantStatus int
		wantCode   string
	}{
		{name: "unknown source", query: "source=missing&follow=false", wantStatus: 400, wantCode: StreamErrorCodeSourceUnsupported},
		{name: "source lacks the capability", query: "source=fa&follow=false", wantStatus: 400, wantCode: StreamErrorCodeSourceUnsupported},
		{name: "source unavailable", query: "source=hist&follow=false", wantStatus: 503, wantCode: StreamErrorCodeSourceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				faCalls := 0
				fa := scriptedSubscriber(func(ctx context.Context, scope *FlowStreamScope, f *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
					faCalls++
					return scriptedEvents(nil, nil).Subscribe(ctx, scope, f)
				})
				hist := scriptedSubscriber(func(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
					errs := make(chan error, 1)
					errs <- dependencyUnavailable("flow source is unavailable")
					close(errs)
					flows := make(chan apisv1.FlowStreamEvent)
					close(flows)
					return flows, errs, make(chan struct{})
				})
				router := &RoutedSubscriber{Sources: []FlowSource{
					{Name: "fa", Watch: true, Subscriber: fa},
					{Name: "hist", List: true, Subscriber: hist},
				}}
				rr := newCloseNotifyRecorder()
				newTestRouter(NewSSEHandler(testr.New(t), router)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true&"+tc.query, nil))
				require.Zero(t, faCalls)
				require.Equal(t, tc.wantStatus, rr.Code)
				require.Contains(t, rr.Body.String(), tc.wantCode)
				require.NotContains(t, rr.Body.String(), "event:complete")
			})
		})
	}
}

func TestSSECompletionReportsTruncation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		flows         int
		wantTruncated string
		wantCount     string
	}{
		{name: "within the limit", flows: 2, wantTruncated: `"truncated":false`, wantCount: `"snapshotCount":2`},
		{name: "extra record received", flows: 3, wantTruncated: `"truncated":true`, wantCount: `"snapshotCount":2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := scriptedEvents([]apisv1.FlowStreamEvent{{Flows: make([]apisv1.Flow, tc.flows)}}, nil)
				router := &RoutedSubscriber{Sources: []FlowSource{{Name: "src", List: true, Subscriber: source}}}
				rr := newCloseNotifyRecorder()
				newTestRouter(NewSSEHandler(testr.New(t), router)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true&follow=false&maxCount=2", nil))
				require.Contains(t, rr.Body.String(), "event:complete")
				require.Contains(t, rr.Body.String(), tc.wantCount)
				require.Contains(t, rr.Body.String(), `"maxCount":2`)
				require.Contains(t, rr.Body.String(), tc.wantTruncated)
			})
		})
	}
}

func TestSSEAnswers501WhenNoSourceServesTheMode(t *testing.T) {
	router := &RoutedSubscriber{Sources: []FlowSource{{Name: "fa", Watch: true, Subscriber: scriptedEvents(nil, nil)}}}
	rr := newCloseNotifyRecorder()
	newTestRouter(NewSSEHandler(testr.New(t), router)).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true&follow=false", nil))
	require.Equal(t, http.StatusNotImplemented, rr.Code)
	var got apisv1.FlowStreamErrorEvent
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, StreamErrorCodeSourceNotServed, got.Code)
	require.False(t, got.Retryable)
	require.Contains(t, got.Message, "finite queries (follow=false)")
}

func TestFiniteResultLimitAcrossBatches(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live        bool
		secondBatch int
		thirdBatch  int
		// droppedOnly adds an event that only reports dropped_count after the others.
		droppedOnly   bool
		wantCount     int
		wantTruncated bool
		wantError     bool
	}{
		{name: "exact limit", secondBatch: 1, wantCount: 3},
		{name: "the extra record in the last batch truncates", secondBatch: 2, wantCount: 3, wantTruncated: true},
		{name: "the extra record alone truncates", secondBatch: 1, thirdBatch: 1, wantCount: 3, wantTruncated: true},
		{name: "a dropped-count-only event after the extra record", secondBatch: 2, droppedOnly: true, wantCount: 3, wantTruncated: true},
		{name: "a dropped-count-only event after records within the limit", secondBatch: 1, droppedOnly: true, wantCount: 3},
		{name: "more than one extra record is rejected", secondBatch: 3, wantCount: 2, wantError: true},
		{name: "a record after the extra one is rejected", secondBatch: 2, thirdBatch: 1, wantCount: 3, wantTruncated: true, wantError: true},
		{name: "live stream is not capped", live: true, secondBatch: 2, wantCount: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				batches := []apisv1.FlowStreamEvent{
					{Flows: make([]apisv1.Flow, 2)},
					{Flows: make([]apisv1.Flow, tc.secondBatch)},
				}
				if tc.thirdBatch > 0 {
					batches = append(batches, apisv1.FlowStreamEvent{Flows: make([]apisv1.Flow, tc.thirdBatch)})
				}
				if tc.droppedOnly {
					batches = append(batches, apisv1.FlowStreamEvent{DroppedCount: 5})
				}
				source := scriptedEvents(batches, nil)
				router := &RoutedSubscriber{Sources: []FlowSource{{Name: "src", Watch: true, List: true, Subscriber: source}}}
				filter := finiteFilter()
				filter.MaxCount = 3
				if tc.live {
					filter.Follow = nil
				}
				received := 0
				truncated := false
				err := relay(t.Context(), router, &FlowStreamScope{ClusterWide: true}, filter,
					func() error { return nil },
					func(e apisv1.FlowStreamEvent) error {
						received += len(e.Flows)
						truncated = truncated || e.Truncated
						return nil
					})
				if tc.wantError {
					require.ErrorContains(t, err, "exceeded the requested result limit")
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.wantCount, received)
				require.Equal(t, tc.wantTruncated, truncated)
			})
		})
	}
}

// A finite query is held to the same first-response contract as a live stream: a source which
// sends no stream_epoch may be ignoring the scope.
func TestFiniteQueryRequiresStreamEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return stream.Send(&flowpb.GetFlowsResponse{})
		}}
		subscriber := newTestSubscriber(t, server)
		subscriber.name = "hist"
		ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeToken, Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")}})
		err := relay(ctx, subscriber, &FlowStreamScope{ClusterWide: true}, finiteFilter(), func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
		var se *StreamError
		require.ErrorAs(t, err, &se)
		require.Equal(t, StreamErrorCodeFlowAggregatorTooOld, se.Code)
		require.Contains(t, se.Error(), `"hist"`)
	})
}

func TestClassifyStreamErrUnavailable(t *testing.T) {
	subscriber := &GRPCFlowStreamSubscriber{name: "hist"}
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			got := subscriber.classifyStreamErr(status.Error(code, "offline"))
			require.Equal(t, StreamErrorCodeSourceUnavailable, got.Code)
			require.True(t, got.Retryable)
			require.Contains(t, got.Error(), `"hist"`)
		})
	}
	require.Equal(t, StreamErrorCodeForbidden, subscriber.classifyStreamErr(status.Error(codes.PermissionDenied, "denied")).Code)
}

func TestFiniteCancellationDoesNotReportSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server := &fakeFlowStreamServer{handle: func(int, string) error { return nil }, send: func(s grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			if err := s.Send(firstResponse); err != nil {
				return err
			}
			return status.Error(codes.Canceled, "source stopped")
		}}
		subscriber := newTestSubscriber(t, server)
		ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeToken, Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")}})
		err := relay(ctx, subscriber, &FlowStreamScope{ClusterWide: true}, finiteFilter(), func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
		require.ErrorContains(t, err, "canceled the query before completion")
	})
}

// This exercises real certificate handshakes, so it uses loopback TCP rather than synctest.
// Completion is synchronized by the stream channels, with a context deadline only as a test
// failure bound. Each source must get its own connection in the session: with a shared key, the
// second source would reuse the first one's connection and its server would never be called.
func TestCertificateSessionUsesOneConnectionPerSource(t *testing.T) {
	ca := testcerts.NewCA()
	kp, err := ca.NewKeyPair("flow.test")
	require.NoError(t, err)
	certificate, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
	require.NoError(t, err)
	clientKP, err := ca.NewKeyPair("viewer")
	require.NoError(t, err)
	servers := []*fakeFlowStreamServer{
		{handle: func(int, string) error { return nil }},
		{handle: func(int, string) error { return nil }},
	}
	var subscribers []FlowStreamSubscriber
	for i, name := range []string{"flow-aggregator", "flow-history"} {
		subscriber := newCertTestSubscriber(t, servers[i], ca, certificate, "flow.test")
		subscriber.name = name
		subscribers = append(subscribers, subscriber)
	}
	ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeKubeconfig, Credential: session.Credential{Kind: session.KindCert, CertPEM: clientKP.PublicKey(), KeyPEM: clientKP.PrivateKey()}})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, upstream := range subscribers {
		err := relay(ctx, upstream, &FlowStreamScope{ClusterWide: true}, finiteFilter(), func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, servers[0].calls.Load())
	require.EqualValues(t, 1, servers[1].calls.Load())
}

func TestNewRoutedSubscriber(t *testing.T) {
	caConfigMap := func(name string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "trusted"}, Data: data}
	}
	watch := []string{serverconfig.FlowSourceCapabilityWatch}
	list := []string{serverconfig.FlowSourceCapabilityList}

	t.Run("builds the sources in order", func(t *testing.T) {
		client := fake.NewClientset(caConfigMap("hist-ca", map[string]string{"ca.crt": string(newTestCA(t))}))
		router, err := NewRoutedSubscriber(testr.New(t), client, []serverconfig.FlowSourceConfig{
			{Name: "flow-aggregator", Capabilities: watch, Address: "fa.example.test:14740", InsecureSkipVerify: true},
			{Name: "flow-history", Capabilities: list, Address: "hist.example.test:14740", CAConfigMap: "hist-ca", Namespace: "trusted", ServerName: "flow.test"},
			{Name: "both", Capabilities: append(append([]string{}, watch...), list...), Address: "both.example.test:14740"},
		}, nil)
		require.NoError(t, err)
		runRoutedSubscriber(t, router)
		require.Len(t, router.Sources, 3)
		assert.Equal(t, "flow-aggregator", router.Sources[0].Name)
		assert.True(t, router.Sources[0].Watch)
		assert.False(t, router.Sources[0].List)
		assert.Equal(t, "flow-history", router.Sources[1].Name)
		assert.False(t, router.Sources[1].Watch)
		assert.True(t, router.Sources[1].List)
		assert.True(t, router.Sources[2].Watch)
		assert.True(t, router.Sources[2].List)
		for _, source := range router.Sources {
			watched, ok := source.Subscriber.(*WatchedSubscriber)
			require.True(t, ok)
			require.Eventually(t, watched.available, 5*time.Second, 10*time.Millisecond)
			grpcSource, _ := watched.currentForTest()
			assert.Equal(t, source.Name, grpcSource.name)
			if source.Name == "flow-history" {
				assert.Equal(t, "flow.test", grpcSource.tlsConfig.ServerName)
				assert.NotNil(t, grpcSource.tlsConfig.RootCAs)
			}
		}
	})

	t.Run("a source which discovers its Service waits for it, alone", func(t *testing.T) {
		client := fake.NewClientset()
		router, err := NewRoutedSubscriber(testr.New(t), client, []serverconfig.FlowSourceConfig{
			{Name: "flow-aggregator", Capabilities: watch, InsecureSkipVerify: true,
				Discovery: serverconfig.FlowSourceDiscoveryConfig{Enabled: true}},
			{Name: "flow-history", Capabilities: list, Address: "hist.example.test:14740", InsecureSkipVerify: true},
		}, nil)
		require.NoError(t, err)
		runRoutedSubscriber(t, router)
		discovering := router.Sources[0].Subscriber.(*WatchedSubscriber)
		static := router.Sources[1].Subscriber.(*WatchedSubscriber)
		assert.True(t, static.available(), "a source with an address does not wait for discovery")
		assert.False(t, discovering.available())

		_, err = client.CoreV1().Services("networking").Create(t.Context(), discoveryService("networking", "fa"), metav1.CreateOptions{})
		require.NoError(t, err)
		require.Eventually(t, discovering.available, 5*time.Second, 10*time.Millisecond)
		current, _ := discovering.currentForTest()
		assert.Equal(t, "fa.networking.svc:14740", current.address)
		assert.Equal(t, "flow-aggregator", current.name)
	})

	for _, tc := range []struct {
		name string
		data map[string]string
	}{
		{name: "missing CA ConfigMap"},
		{name: "CA ConfigMap without ca.crt", data: map[string]string{"other": "x"}},
		{name: "unparsable CA", data: map[string]string{"ca.crt": "not a certificate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goodCA := caConfigMap("good-ca", map[string]string{"ca.crt": string(newTestCA(t))})
			goodCA.Namespace = "live"
			client := fake.NewClientset(goodCA)
			cms := client.CoreV1().ConfigMaps("trusted")
			if tc.data != nil {
				_, err := cms.Create(t.Context(), caConfigMap("bad-ca", tc.data), metav1.CreateOptions{})
				require.NoError(t, err)
			}
			router, err := NewRoutedSubscriber(testr.New(t), client, []serverconfig.FlowSourceConfig{
				{Name: "flow-aggregator", Capabilities: watch, Address: "fa.example.test:14740", CAConfigMap: "good-ca", Namespace: "live"},
				{Name: "flow-history", Capabilities: list, Address: "hist.example.test:14740", CAConfigMap: "bad-ca", Namespace: "trusted"},
			}, nil)
			require.NoError(t, err, "a missing or invalid CA must not prevent backend startup")
			runRoutedSubscriber(t, router)
			good := router.Sources[0].Subscriber.(*WatchedSubscriber)
			bad := router.Sources[1].Subscriber.(*WatchedSubscriber)
			require.Eventually(t, good.available, 5*time.Second, 10*time.Millisecond)
			unaffected, _ := good.currentForTest()
			assert.False(t, bad.available())
			filter := finiteFilter()
			filter.Source = "flow-history"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = relay(ctx, router, &FlowStreamScope{}, filter, func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
			var streamErr *StreamError
			require.ErrorAs(t, err, &streamErr)
			assert.True(t, streamErr.Retryable)

			// A valid CA arriving later recovers only that source. Subsequent rotations also
			// leave the other source's subscriber and generation alone.
			cm := caConfigMap("bad-ca", map[string]string{"ca.crt": string(newTestCA(t))})
			if tc.data == nil {
				_, err = cms.Create(t.Context(), cm, metav1.CreateOptions{})
			} else {
				_, err = cms.Update(t.Context(), cm, metav1.UpdateOptions{})
			}
			require.NoError(t, err)
			require.Eventually(t, bad.available, 5*time.Second, 10*time.Millisecond)
			first, _ := bad.currentForTest()
			assert.Equal(t, "flow-history", first.name)
			cm.Data["ca.crt"] = string(newTestCA(t))
			_, err = cms.Update(t.Context(), cm, metav1.UpdateOptions{})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				_, generation := bad.currentForTest()
				return generation == 2 && first.conn.GetState() == connectivity.Shutdown
			}, 5*time.Second, 10*time.Millisecond)
			current, generation := good.currentForTest()
			assert.Same(t, unaffected, current)
			assert.Equal(t, 1, generation)
			assert.NotEqual(t, connectivity.Shutdown, current.conn.GetState())

			require.NoError(t, router.Close())
			assert.False(t, good.available())
			assert.False(t, bad.available())
			assert.Equal(t, connectivity.Shutdown, unaffected.conn.GetState())
		})
	}
}

// Every discovering source follows the Service its own selector matches, and when that Service
// moves the other sources keep their connections.
func TestNewRoutedSubscriberWithSeveralDiscoveringSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := fake.NewClientset()
		serviceIn := func(namespace, name, label string) *corev1.Service {
			svc := discoveryService(namespace, name)
			if label != "" {
				svc.Labels = map[string]string{"app": label}
			}
			return svc
		}
		router, err := NewRoutedSubscriber(testr.New(t), client, []serverconfig.FlowSourceConfig{
			{Name: "flow-aggregator", Capabilities: []string{serverconfig.FlowSourceCapabilityWatch},
				Discovery: serverconfig.FlowSourceDiscoveryConfig{Enabled: true}},
			{Name: "flow-history", Capabilities: []string{serverconfig.FlowSourceCapabilityList},
				Discovery: serverconfig.FlowSourceDiscoveryConfig{Enabled: true, LabelSelector: "app=flow-history"}},
		}, nil)
		require.NoError(t, err)
		runRoutedSubscriber(t, router)
		aggregator := router.Sources[0].Subscriber.(*WatchedSubscriber)
		history := router.Sources[1].Subscriber.(*WatchedSubscriber)
		settle()
		assert.False(t, aggregator.available())
		assert.False(t, history.available())

		// A Service which matches only the second selector serves only that source.
		for _, svc := range []*corev1.Service{
			serviceIn("history-ns", "history", "flow-history"),
			serviceIn("networking-a", "fa", ""),
		} {
			_, err = client.CoreV1().Services(svc.Namespace).Create(t.Context(), svc, metav1.CreateOptions{})
			require.NoError(t, err)
		}
		settle()
		require.True(t, aggregator.available())
		require.True(t, history.available())
		aggregatorSub, _ := aggregator.currentForTest()
		historySub, historyGeneration := history.currentForTest()
		assert.Equal(t, "fa.networking-a.svc:14740", aggregatorSub.address)
		assert.Equal(t, "history.history-ns.svc:14740", historySub.address)

		// The aggregator's Service moves: only that source reconnects.
		require.NoError(t, client.CoreV1().Services("networking-a").Delete(t.Context(), "fa", metav1.DeleteOptions{}))
		_, err = client.CoreV1().Services("networking-b").Create(t.Context(), serviceIn("networking-b", "fa", ""), metav1.CreateOptions{})
		require.NoError(t, err)
		settle()
		moved, generation := aggregator.currentForTest()
		assert.Equal(t, "fa.networking-b.svc:14740", moved.address)
		assert.Equal(t, 2, generation)
		assert.Equal(t, connectivity.Shutdown, aggregatorSub.conn.GetState())
		current, generation := history.currentForTest()
		assert.Same(t, historySub, current, "another discovering source keeps its subscriber")
		assert.Equal(t, historyGeneration, generation)
		assert.NotEqual(t, connectivity.Shutdown, current.conn.GetState())
		assert.False(t, isRetired(current))

		// Losing its Service makes one source unavailable and leaves the other serving.
		require.NoError(t, client.CoreV1().Services("history-ns").Delete(t.Context(), "history", metav1.DeleteOptions{}))
		settle()
		assert.False(t, history.available())
		assert.True(t, aggregator.available())
	})
}

func runRoutedSubscriber(t *testing.T, router *RoutedSubscriber) {
	t.Helper()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.Run(stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-done
		require.NoError(t, router.Close())
	})
}
