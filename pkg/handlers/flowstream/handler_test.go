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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func TestParseFlowStreamFilter(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		expected    *FlowStreamFilter
		expectError bool
	}{
		{
			name:     "empty query defaults",
			query:    "",
			expected: &FlowStreamFilter{},
		},
		{
			name:  "namespaces comma-separated",
			query: "namespaces=default,kube-system",
			expected: &FlowStreamFilter{
				Namespaces: []string{"default", "kube-system"},
			},
		},
		{
			name:  "pods and services",
			query: "pods=pod-a,pod-b&services=svc-a",
			expected: &FlowStreamFilter{
				PodNames:     []string{"pod-a", "pod-b"},
				ServiceNames: []string{"svc-a"},
			},
		},
		{
			name:  "flowTypes parsed from string names",
			query: "flowTypes=intra-node,inter-node",
			expected: &FlowStreamFilter{
				FlowTypes: []apisv1.FlowType{apisv1.FlowTypeIntraNode, apisv1.FlowTypeInterNode},
			},
		},
		{
			name:  "flowTypes case-insensitive",
			query: "flowTypes=To-External,FROM-EXTERNAL",
			expected: &FlowStreamFilter{
				FlowTypes: []apisv1.FlowType{apisv1.FlowTypeToExternal, apisv1.FlowTypeFromExternal},
			},
		},
		{
			name:        "invalid flowType returns error",
			query:       "flowTypes=unknown-type",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?"+tt.query, nil)

			filter, err := parseFlowStreamFilter(c)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, filter)
		})
	}
}

// stubFlowStreamSubscriber is a minimal FlowStreamHandler for testing the SSE handler.
type stubFlowStreamSubscriber struct {
	events []apisv1.FlowStreamEvent
	err    error
	// closeFlowsChOnErr additionally closes flowsCh right after buffering err into errCh,
	// reproducing the real GRPCFlowStreamSubscriber.Subscribe shape: every one of its error
	// paths does "errCh <- err; return", and its deferred close(flowsCh)/close(errCh) then run
	// with both channels ready at once. Left false by default (leaving flowsCh open) for
	// tests that don't care about that race; see TestStreamFlowsErrorSurvivesFlowsChRace.
	closeFlowsChOnErr bool
	// delay, if set, buffers err into errCh (and closes flowsCh, if closeFlowsChOnErr) from a
	// goroutine after delay instead of before Subscribe returns. Used to place the error after
	// StreamFlows's initial synchronous-failure peek (see errorPeekTimeout) so a test can still
	// exercise the flowsCh/errCh race inside the later c.Stream loop.
	delay time.Duration
}

func (s *stubFlowStreamSubscriber) Subscribe(_ context.Context, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent, len(s.events)+1)
	errCh := make(chan error, 1)

	if s.err != nil {
		deliver := func() {
			errCh <- s.err
			if s.closeFlowsChOnErr {
				close(flowsCh)
			}
			// Otherwise leave flowsCh open so the select picks up errCh first.
		}
		if s.delay > 0 {
			go func() { time.Sleep(s.delay); deliver() }()
		} else {
			deliver()
		}
	} else {
		for _, e := range s.events {
			flowsCh <- e
		}
		close(flowsCh)
	}

	return flowsCh, errCh
}

func newTestRouter(handler *SSEHandler) *gin.Engine {
	router := gin.New()
	router.GET("/api/v1/flows/stream", handler.StreamFlows)
	return router
}

func TestStreamFlowsHappyPath(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{
		events: []apisv1.FlowStreamEvent{
			{
				Flows: []apisv1.Flow{
					{
						ID:      "flow-1",
						StartTs: "2026-03-25T00:00:00Z",
						EndTs:   "2026-03-25T00:01:00Z",
						IP: apisv1.FlowIP{
							Version:     apisv1.IPVersionIPv4,
							Source:      "10.0.0.1",
							Destination: "10.0.0.2",
						},
						Transport: apisv1.FlowTransport{
							ProtocolNumber:  6,
							SourcePort:      12345,
							DestinationPort: 80,
						},
					},
				},
			},
		},
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	scanner := bufio.NewScanner(resp.Body)
	var foundFlowEvent bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			var event apisv1.FlowStreamEvent
			err := json.Unmarshal([]byte(data), &event)
			require.NoError(t, err)
			assert.Len(t, event.Flows, 1)
			assert.Equal(t, "flow-1", event.Flows[0].ID)
			foundFlowEvent = true
		}
	}
	require.NoError(t, scanner.Err())
	assert.True(t, foundFlowEvent, "expected at least one flow event in SSE stream")
}

// Subscribe failing before StreamFlows commits to a 200 (the common case: every failure this
// endpoint can hit today is discovered on Subscribe's first GetFlows call or its first Recv, not
// partway through an established stream) must surface as a real HTTP error status with a JSON
// body, not as a 200 followed by an SSE "error" event buried in prose.
func TestStreamFlowsErrorPath(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{
		err: fmt.Errorf("upstream connection lost"),
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "upstream connection lost")
}

// classifyStreamErr's StreamError.Code maps to a specific, more useful HTTP status than the
// generic default.
func TestStreamFlowsErrorPathClassifiedStatus(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{
		err: &StreamError{msg: "at capacity", Code: StreamErrorCodeResourceExhausted, Retryable: true},
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var evt apisv1.FlowStreamErrorEvent
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&evt))
	assert.Equal(t, StreamErrorCodeResourceExhausted, evt.Code)
	assert.True(t, evt.Retryable, "the client reconnects off this flag, so it has to reach the client")
}

// A credential the Flow Aggregator rejects must never come back as a 401. A 401 from any
// antrea-ui endpoint means "your antrea-ui session is over", and the frontend acts on it by
// logging the user out of the whole UI - but FA is a different server than the kube-apiserver
// (see statusForStreamErr and classifyStreamErr), so a credential it rejects can still be
// perfectly valid for every other antrea-ui call. The failure has to reach the client as
// something only the flow-visibility page reacts to.
func TestStreamFlowsUnauthenticatedIsNotA401(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{
		err: &StreamError{msg: "FlowAggregator rejected the credential", Code: StreamErrorCodeUnauthenticated, Retryable: false},
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)

	// The status is deliberately generic, so the code/retryable fields are the only thing that
	// tells the client this one is not worth retrying.
	var evt apisv1.FlowStreamErrorEvent
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&evt))
	assert.Equal(t, StreamErrorCodeUnauthenticated, evt.Code)
	assert.False(t, evt.Retryable)
	assert.Contains(t, evt.Message, "rejected the credential")
}

// When Subscribe's error paths buffer an error into errCh and then close both channels (the real
// GRPCFlowStreamSubscriber shape - see closeFlowsChOnErr), the closed flowsCh and the buffered
// errCh value become two independently-ready select cases at once. Go's select picks uniformly
// among ready cases, so without draining errCh in the flowsCh branch, roughly half of all calls
// would take the closed-flowsCh path and silently drop the error. Run enough iterations that a
// regression would very likely produce at least one miss.
func TestStreamFlowsErrorSurvivesFlowsChRace(t *testing.T) {
	logger := testr.New(t)

	for i := 0; i < 50; i++ {
		stub := &stubFlowStreamSubscriber{
			err:               fmt.Errorf("at capacity"),
			closeFlowsChOnErr: true,
			// Past errorPeekTimeout below, so the error lands after StreamFlows has
			// already committed to a 200 and is running the c.Stream loop this test
			// means to exercise, instead of being caught by the earlier peek.
			delay: 5 * time.Millisecond,
		}
		sseHandler := NewSSEHandler(logger, stub)
		sseHandler.errorPeekTimeout = time.Millisecond
		ts := httptest.NewServer(newTestRouter(sseHandler))

		resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
		require.NoError(t, err)

		scanner := bufio.NewScanner(resp.Body)
		var body strings.Builder
		for scanner.Scan() {
			body.WriteString(scanner.Text())
			body.WriteString("\n")
		}
		require.NoError(t, scanner.Err())
		resp.Body.Close()
		ts.Close()

		assert.Contains(t, body.String(), "event:error", "iteration %d: error event must survive the flowsCh/errCh race", i)
		assert.Contains(t, body.String(), "at capacity", "iteration %d", i)
	}
}

func TestStreamFlowsBadFilter(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?flowTypes=abc")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
