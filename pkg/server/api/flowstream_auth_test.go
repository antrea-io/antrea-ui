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

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/handlers/flowstream"
)

// flowingSubscriber delivers one flow record immediately, so a test can tell whether the caller
// actually received flow data rather than just a status code. It also counts Subscribe calls and
// records the scope it was called with, so a test can assert the stronger property: not merely
// what the caller got back, but whether the backend asked the Flow Aggregator for flows on their
// behalf at all, and in what scope.
//
// Setting err makes it behave like the real subscriber reporting a failure the Flow Aggregator
// returned: buffer the error and close both channels, which is how a stream that never opened
// reaches StreamFlows before it commits to a 200.
type flowingSubscriber struct {
	subscribes atomic.Int32
	scope      atomic.Pointer[flowstream.FlowStreamScope]
	err        error
}

func (s *flowingSubscriber) Subscribe(ctx context.Context, scope *flowstream.FlowStreamScope, _ *flowstream.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	s.subscribes.Add(1)
	s.scope.Store(scope)
	flowsCh := make(chan apisv1.FlowStreamEvent, 1)
	if s.err != nil {
		errCh := make(chan error, 1)
		errCh <- s.err
		close(flowsCh)
		close(errCh)
		// Never closed, matching the real GRPCFlowStreamSubscriber: a failure that never
		// reached a live stream must never signal readiness.
		return flowsCh, errCh, make(chan struct{})
	}
	errCh := make(chan error)
	ready := make(chan struct{})
	close(ready)
	flowsCh <- apisv1.FlowStreamEvent{Flows: []apisv1.Flow{{}}}
	return flowsCh, errCh, ready
}

// newStreamingServer builds a Server whose flow stream route is the real one (a nil subscriber
// registers flowStreamDisabled instead), on top of the fake K8s API server so that a test can
// observe whether any SelfSubjectAccessReview is issued for the route - the answer, since
// authorization moved to the Flow Aggregator, is that none is.
func newStreamingServer(t *testing.T, subscriber *flowingSubscriber) (*testServer, *fakeAccessK8sAPIServer, <-chan struct{}) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	ts.s.flowStreamSSEHandler = flowstream.NewSSEHandler(testr.New(t), subscriber)
	router := gin.New()
	// Closed once the first request's whole handler chain has returned, which is the
	// happens-before edge a "did not subscribe" assertion needs: the client's Do() returns as
	// soon as the response headers arrive, so reading the counter at that point could race a
	// handler still on its way to Subscribe. Registered first, so its c.Next() wraps every later
	// handler. The channel belongs to the server, not to a request, so a second request through
	// the same router must not close it again: sync.OnceFunc makes the edge mean "the first
	// request's chain has returned" instead of panicking on the second.
	handlerReturned := make(chan struct{})
	closeOnce := sync.OnceFunc(func() { close(handlerReturned) })
	router.Use(func(c *gin.Context) {
		defer closeOnce()
		c.Next()
	})
	ts.s.AddRoutes(&router.RouterGroup)
	ts.router = router
	return ts, fakeAPIServer, handlerReturned
}

// httptest.ResponseRecorder does not implement http.CloseNotifier, which gin's Stream needs, so
// the stream cases need a real server.
//
// query is appended to the request URL. Every caller has to name a scope now: it is what the Flow
// Aggregator authorizes the stream against, and StreamFlows rejects a request without one with a
// 400 before any connection is made.
func openStream(t *testing.T, ts *testServer, token, query string) (*http.Response, func()) {
	srv := httptest.NewServer(ts.router)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/flows/stream?"+query, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp, func() {
		cancel()
		resp.Body.Close()
		srv.Close()
	}
}

// assertRejectedWithoutSubscribing asserts the backend did not ask the Flow Aggregator for flow
// data on this caller's behalf. Waits for the handler chain to return first, so the counter is read
// after every code path that could have subscribed has run — no polling, and no window in which a
// slow handler makes the assertion pass vacuously.
//
// The wait cannot use testing/synctest: the stream cases need a real server (see openStream), and
// goroutines blocked on real network I/O are never durably blocked, so a bubble would simply hang.
func assertRejectedWithoutSubscribing(t *testing.T, handlerReturned <-chan struct{}, subscriber *flowingSubscriber, msg string) {
	t.Helper()
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the handler never returned, so it is still streaming rather than rejecting", msg)
	}
	assert.Zero(t, subscriber.subscribes.Load(), msg)
}

// receivesFlow reports whether the open stream delivered a flow record, rather than just a status
// code.
func receivesFlow(t *testing.T, resp *http.Response) bool {
	t.Helper()
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			return true
		}
	}
	return false
}

// The flow stream handler never presents the caller's credential to Kubernetes: it presents it to
// the Flow Aggregator instead. So "the API server will reject a bad token" is not true of this
// route, and a token that nothing validated would simply be believed. The check in authn.bearer is
// what this test pins - and it is now the only thing standing between an unvalidated token and the
// Flow Aggregator, since there is no longer an antrea-ui-side authorization gate that would
// incidentally present the credential to the API server on the way past.
func TestFlowStreamRejectsUnvalidatedBearerToken(t *testing.T) {
	t.Run("rejected token gets no data", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, _, handlerReturned := newStreamingServer(t, subscriber)
		ts.credentialValidator.rejected["bogus"] = true
		resp, cleanup := openStream(t, ts, "bogus", "clusterWide=true")
		defer cleanup()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assertRejectedWithoutSubscribing(t, handlerReturned, subscriber, "an unauthenticated request must not reach the Flow Aggregator")
	})

	t.Run("valid token streams", func(t *testing.T) {
		ts, _, _ := newStreamingServer(t, &flowingSubscriber{})
		resp, cleanup := openStream(t, ts, "good", "clusterWide=true")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "a validated token should receive flow data")
	})
}

// Authorization for the flow stream is entirely the Flow Aggregator's: it checks Kubernetes RBAC
// against a virtual "flows" resource in the scope the request names, and redacts each endpoint to
// the tier the caller is entitled to. antrea-ui used to gate the route on "is an administrator"
// (requireFlowVisibility), which had to go: it would make every per-Namespace grant unreachable,
// refusing a Namespace administrator holding flow access in their own Namespace before the Flow
// Aggregator ever saw the request.
//
// These cases pin the inversion. An ordinary caller now reaches the Flow Aggregator, and a refusal
// is reported as the Flow Aggregator's, with its own error code, rather than manufactured here.
func TestFlowStreamAuthorizationIsTheFlowAggregators(t *testing.T) {
	t.Run("an ordinary user reaches the Flow Aggregator", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, fakeAPIServer, _ := newStreamingServer(t, subscriber)
		// The caller is emphatically not a cluster admin, which the removed gate would have
		// refused on. Whether they may see any flows is now the Flow Aggregator's call.
		fakeAPIServer.clusterAdmin = false
		resp, cleanup := openStream(t, ts, "good", "observedNamespace=ns-b")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "an ordinary user's request must reach the Flow Aggregator")
		assert.Equal(t, int32(1), subscriber.subscribes.Load())
	})

	t.Run("no SelfSubjectAccessReview is issued for the route", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, fakeAPIServer, _ := newStreamingServer(t, subscriber)
		// Nothing at all is reachable, so an antrea-ui-side review would fail the request
		// rather than quietly answer. That is the strongest available form of "no review":
		// the route works with the review endpoint broken.
		fakeAPIServer.statusOverride["selfsubjectaccessreviews"] = http.StatusInternalServerError
		var sawReview atomic.Bool
		inner := fakeAPIServer.Config.Handler
		// Safe to swap: the server has served nothing yet, and openStream below is what
		// starts the first request.
		fakeAPIServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "selfsubjectaccessreviews") {
				sawReview.Store(true)
			}
			inner.ServeHTTP(w, r)
		})

		resp, cleanup := openStream(t, ts, "good", "observedNamespace=ns-b")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp))
		assert.False(t, sawReview.Load(), "opening a flow stream must not cost a SelfSubjectAccessReview")
	})

	t.Run("the static admin reaches the Flow Aggregator", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, fakeAPIServer, _ := newStreamingServer(t, subscriber)
		fakeAPIServer.clusterAdmin = false
		fakeAPIServer.statusOverride["selfsubjectaccessreviews"] = http.StatusInternalServerError

		srv := httptest.NewServer(ts.router)
		defer srv.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/flows/stream?clusterWide=true", nil)
		require.NoError(t, err)
		ts.authorizeRequestAs(req, session.ModeAdmin)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		// The built-in admin presents a minted antrea-ui-admin token to the Flow Aggregator,
		// which honours only Kubernetes RBAC - hence the flows rule this change adds to the
		// antrea-ui-admin-core ClusterRole in the chart.
		assert.True(t, receivesFlow(t, resp), "the built-in admin should receive flow data")
	})

	t.Run("the Flow Aggregator's PermissionDenied is surfaced", func(t *testing.T) {
		// What a caller without the flows grant gets now: the Flow Aggregator's own refusal,
		// wrapped the way the real subscriber wraps it, carrying the code and the
		// not-retryable flag the frontend acts on. Never a 401, and a real 403 rather than the
		// generic 502 - see statusForStreamErr.
		subscriber := &flowingSubscriber{err: fmt.Errorf("stream refused: %w", &flowstream.StreamError{
			Code:      flowstream.StreamErrorCodeForbidden,
			Retryable: false,
		})}
		ts, fakeAPIServer, _ := newStreamingServer(t, subscriber)
		fakeAPIServer.clusterAdmin = false
		resp, cleanup := openStream(t, ts, "good", "observedNamespace=ns-b")
		defer cleanup()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, int32(1), subscriber.subscribes.Load(), "the request must have reached the Flow Aggregator to be refused by it")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var evt apisv1.FlowStreamErrorEvent
		require.NoError(t, json.Unmarshal(body, &evt))
		assert.Equal(t, flowstream.StreamErrorCodeForbidden, evt.Code)
		assert.False(t, evt.Retryable, "a permanent refusal must not be retried in a reconnect loop")
	})

	t.Run("the scope reaches the subscriber", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, _, _ := newStreamingServer(t, subscriber)
		resp, cleanup := openStream(t, ts, "good", "observedNamespace=ns-b&namespaces=ns-c")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.True(t, receivesFlow(t, resp))
		scope := subscriber.scope.Load()
		require.NotNil(t, scope)
		assert.Equal(t, "ns-b", scope.ObservedNamespace)
		assert.False(t, scope.ClusterWide)
	})

	t.Run("a request with no scope is rejected without subscribing", func(t *testing.T) {
		subscriber := &flowingSubscriber{}
		ts, _, handlerReturned := newStreamingServer(t, subscriber)
		resp, cleanup := openStream(t, ts, "good", "")
		defer cleanup()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assertRejectedWithoutSubscribing(t, handlerReturned, subscriber, "a request naming no scope must not reach the Flow Aggregator")
	})

	t.Run("disabled path answers 501 for every authenticated user", func(t *testing.T) {
		// A nil subscriber registers flowStreamDisabled: every authenticated user should get
		// the same 501 "not enabled" answer. Whether the integration is on is public in GET
		// /api/v1/settings anyway.
		ts, fakeAPIServer := newTestServerForAccess(t, nil)
		fakeAPIServer.clusterAdmin = false
		req := httptest.NewRequest("GET", "/api/v1/flows/stream?clusterWide=true", nil)
		ts.authorizeRequestAs(req, session.ModeToken)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotImplemented, rr.Code)
	})
}

// featuregates reaches the Antrea Service, which does delegate to Kubernetes - but the validation
// now happens up front, so the rejection is the authenticator's, not the upstream's.
func TestBearerRejectedBeforeReachingUpstream(t *testing.T) {
	ts := newTestServer(t)
	ts.credentialValidator.rejected["bogus"] = true
	req := httptest.NewRequest("GET", "/api/v1/featuregates", nil)
	req.Header.Set("Authorization", "Bearer bogus")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
