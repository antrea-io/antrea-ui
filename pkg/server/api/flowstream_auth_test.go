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

// The flow stream is one of only two routes that resolve an identity and then never present the
// credential to Kubernetes: it reads from the Flow Aggregator over antrea-ui's own connection. So
// "the API server will reject a bad token" is not true here, and a token that nothing validated is
// simply believed - which, since flow data has no per-user authorization either, is the whole of
// the access control on this endpoint.
func TestFlowStreamRejectsUnvalidatedBearerToken(t *testing.T) {
	newStreamingServer := func(t *testing.T) *testServer {
		ts := newTestServer(t)
		ts.s.flowStreamSSEHandler = flowstream.NewSSEHandler(testr.New(t), &flowingSubscriber{})
		router := gin.New()
		ts.s.AddRoutes(&router.RouterGroup)
		ts.router = router
		return ts
	}

	// httptest.ResponseRecorder does not implement http.CloseNotifier, which gin's Stream needs,
	// so the stream cases need a real server.
	openStream := func(t *testing.T, ts *testServer, token string) (*http.Response, func()) {
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

	t.Run("rejected token gets no data", func(t *testing.T) {
		ts := newStreamingServer(t)
		ts.credentialValidator.rejected["bogus"] = true
		resp, cleanup := openStream(t, ts, "bogus")
		defer cleanup()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("valid token streams", func(t *testing.T) {
		ts := newStreamingServer(t)
		resp, cleanup := openStream(t, ts, "good")
		defer cleanup()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		gotFlow := false
		scanner := bufio.NewScanner(resp.Body)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data:") {
				gotFlow = true
				break
			}
		}
		assert.True(t, gotFlow, "a validated token should receive flow data")
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
