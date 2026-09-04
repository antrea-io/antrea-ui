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
// actually received flow data rather than just a status code.
type flowingSubscriber struct{}

func (s *flowingSubscriber) Subscribe(ctx context.Context, _ *flowstream.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent, 1)
	errCh := make(chan error)
	flowsCh <- apisv1.FlowStreamEvent{Flows: []apisv1.Flow{{}}}
	return flowsCh, errCh
}

// newStreamingServer builds a Server whose flow stream route is the real one (a nil subscriber
// registers flowStreamDisabled instead, and with it no requireFlowVisibility middleware), on top
// of the fake K8s API server so the gate's SelfSubjectAccessReview can be answered.
func newStreamingServer(t *testing.T) (*testServer, *fakeAccessK8sAPIServer) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	ts.s.flowStreamSSEHandler = flowstream.NewSSEHandler(testr.New(t), &flowingSubscriber{})
	router := gin.New()
	ts.s.AddRoutes(&router.RouterGroup)
	ts.router = router
	return ts, fakeAPIServer
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

// The flow stream is one of only two routes that resolve an identity and then never present the
// credential to Kubernetes: it reads from the Flow Aggregator over antrea-ui's own connection. So
// "the API server will reject a bad token" is not true here, and a token that nothing validated is
// simply believed. Flow data has no per-user authorization either, so the only thing narrowing
// what a validated caller sees is requireFlowVisibility (see TestFlowStreamRequiresAdmin).
func TestFlowStreamRejectsUnvalidatedBearerToken(t *testing.T) {
	t.Run("rejected token gets no data", func(t *testing.T) {
		ts, fakeAPIServer := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		ts.credentialValidator.rejected["bogus"] = true
		resp, cleanup := openStream(t, ts, "bogus")
		defer cleanup()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("valid token streams", func(t *testing.T) {
		ts, fakeAPIServer := newStreamingServer(t)
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
		ts, fakeAPIServer := newStreamingServer(t)
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
		ts, fakeAPIServer := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, receivesFlow(t, resp), "a cluster admin should receive flow data")
	})

	t.Run("ordinary user forbidden", func(t *testing.T) {
		ts, fakeAPIServer := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = false
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "restricted to administrators")
	})

	t.Run("review failure fails closed", func(t *testing.T) {
		ts, fakeAPIServer := newStreamingServer(t)
		fakeAPIServer.clusterAdmin = true
		fakeAPIServer.statusOverride["selfsubjectaccessreviews"] = http.StatusInternalServerError
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
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
