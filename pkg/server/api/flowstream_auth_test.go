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
// actually received flow data rather than just a status code. It also counts Subscribe calls, so a
// denied request can assert the stronger property: not merely that the caller got no data, but that
// the backend never asked the Flow Aggregator for any on their behalf.
type flowingSubscriber struct {
	subscribes atomic.Int32
}

func (s *flowingSubscriber) Subscribe(ctx context.Context, _ *flowstream.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	s.subscribes.Add(1)
	flowsCh := make(chan apisv1.FlowStreamEvent, 1)
	errCh := make(chan error)
	flowsCh <- apisv1.FlowStreamEvent{Flows: []apisv1.Flow{{}}}
	return flowsCh, errCh
}

// newStreamingServer builds a Server whose flow stream route is the real one (a nil subscriber
// registers flowStreamDisabled instead, and with it no requireFlowVisibility middleware), on top
// of the fake K8s API server so the gate's SelfSubjectAccessReview can be answered.
func newStreamingServer(t *testing.T) (*testServer, *fakeAccessK8sAPIServer, *flowingSubscriber, <-chan struct{}) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	subscriber := &flowingSubscriber{}
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
	return ts, fakeAPIServer, subscriber, handlerReturned
}

// httptest.ResponseRecorder does not implement http.CloseNotifier, which gin's Stream needs, so
// the stream cases need a real server.
func openStream(t *testing.T, ts *testServer, token string) (*http.Response, func()) {
	srv := httptest.NewServer(ts.router)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/flows/stream", nil)
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
// A gate that stopped aborting would keep the response open and stream instead of returning, so the
// chain never completes and this reports that directly. The wait cannot use testing/synctest: the
// stream cases need a real server (see openStream), and goroutines blocked on real network I/O are
// never durably blocked, so a bubble would simply hang.
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

// The flow stream *handler* never presents the caller's credential to Kubernetes: it reads from
// the Flow Aggregator over antrea-ui's own connection. So "the API server will reject a bad token"
// is not true of the handler, and a token that nothing validated would simply be believed. The
// interim gate does present it (its SelfSubjectAccessReview runs as the caller), which happens to
// catch a bogus token today — but that is a side effect of a temporary restriction, not the
// guarantee, and it disappears when the gate does. The check in authn.bearer is what this test
// pins. Flow data has no per-user authorization either, so the only thing narrowing what a
// validated caller sees is requireFlowVisibility (see TestFlowStreamRequiresAdmin).
func TestFlowStreamRejectsUnvalidatedBearerToken(t *testing.T) {
	t.Run("rejected token gets no data", func(t *testing.T) {
		ts, fakeAPIServer, subscriber, handlerReturned := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		ts.credentialValidator.rejected["bogus"] = true
		resp, cleanup := openStream(t, ts, "bogus")
		defer cleanup()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assertRejectedWithoutSubscribing(t, handlerReturned, subscriber, "an unauthenticated request must not reach the Flow Aggregator")
	})

	t.Run("valid token streams", func(t *testing.T) {
		ts, fakeAPIServer, _, _ := newStreamingServer(t)
		// The token still has to clear requireFlowVisibility, which this test is not about.
		fakeAPIServer.clusterAdmin = true
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "a validated token should receive flow data")
	})
}

// requireFlowVisibility is a temporary restriction: flow data has no per-user authorization, so
// the endpoint is limited to the built-in admin and to Kubernetes cluster admins
// (antrea-io/antrea-ui#1387).
func TestFlowStreamRequiresAdmin(t *testing.T) {
	t.Run("static admin allowed without a K8s call", func(t *testing.T) {
		ts, fakeAPIServer, _, _ := newStreamingServer(t)
		// False, so an allow can only come from the ModeAdmin short-circuit. That matches
		// reality: the antrea-ui-admin ServiceAccount the static-admin session impersonates
		// holds no */*/* rule.
		fakeAPIServer.clusterAdmin = false
		// And nothing at all is reachable, so a call would fail rather than answer false.
		fakeAPIServer.statusOverride["selfsubjectaccessreviews"] = http.StatusInternalServerError

		srv := httptest.NewServer(ts.router)
		defer srv.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/flows/stream", nil)
		require.NoError(t, err)
		ts.authorizeRequestAs(req, session.ModeAdmin)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "the built-in admin should receive flow data")
	})

	t.Run("cluster admin token allowed", func(t *testing.T) {
		ts, fakeAPIServer, _, _ := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "a cluster admin should receive flow data")
	})

	t.Run("ordinary user forbidden", func(t *testing.T) {
		ts, fakeAPIServer, subscriber, handlerReturned := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = false
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		// The point of the gate, and what the status code alone does not pin down: HandleError
		// writes a response without aborting, so only requireFlowVisibility's c.Abort() keeps
		// StreamFlows — and with it the gRPC subscription — from running anyway. Asserted before
		// the body is read, which would block against a handler that did start streaming.
		assertRejectedWithoutSubscribing(t, handlerReturned, subscriber, "a forbidden caller must not reach the Flow Aggregator")
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "restricted to administrators")
	})

	// The gate has to ask the API server "may *this caller* do everything", so it must present
	// the caller's own credential. Nothing else in the test suite pins that: the fake API server
	// answers from a field regardless of who asks, so a gate rewritten to use antrea-ui's own
	// ServiceAccount would pass every other case here. That rewrite is not hypothetical-only —
	// it is the shape a well-meaning "avoid a per-request client" refactor takes — and it turns
	// the gate into a property of the deployment rather than of the caller: everyone is denied
	// under the default chart (the ServiceAccount holds no */*/* rule), and everyone is allowed
	// wherever an operator has bound it cluster-admin.
	t.Run("the review is issued with the caller's own credential", func(t *testing.T) {
		ts, fakeAPIServer, _, _ := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		var reviewAuth atomic.Pointer[string]
		inner := fakeAPIServer.Config.Handler
		// Safe to swap: the server has served nothing yet, and openStream below is what starts
		// the first request.
		fakeAPIServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "selfsubjectaccessreviews") {
				h := r.Header.Get("Authorization")
				reviewAuth.Store(&h)
			}
			inner.ServeHTTP(w, r)
		})

		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		seen := reviewAuth.Load()
		require.NotNil(t, seen, "the gate did not issue a SelfSubjectAccessReview at all")
		assert.Equal(t, "Bearer good", *seen, "the review must carry the caller's token, not antrea-ui's own credential")
	})

	t.Run("review failure fails closed", func(t *testing.T) {
		ts, fakeAPIServer, subscriber, handlerReturned := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		fakeAPIServer.statusOverride["selfsubjectaccessreviews"] = http.StatusInternalServerError
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assertRejectedWithoutSubscribing(t, handlerReturned, subscriber, "a review we could not evaluate must not reach the Flow Aggregator")
	})

	t.Run("disabled path is not gated", func(t *testing.T) {
		// A nil subscriber registers flowStreamDisabled, deliberately without the gate: every
		// authenticated user should get the same 501 "not enabled" answer, not a 403 giving
		// the wrong reason. Whether the integration is on is public in GET /api/v1/settings
		// anyway.
		ts, fakeAPIServer := newTestServerForAccess(t, nil)
		fakeAPIServer.clusterAdmin = false
		req := httptest.NewRequest("GET", "/api/v1/flows/stream", nil)
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
