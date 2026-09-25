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
	"sync"
	"testing"
	"testing/synctest"
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
	// StreamFlows's initial synchronous wait so a test can still exercise the flowsCh/errCh race
	// inside the later c.Stream loop.
	delay time.Duration
}

func (s *stubFlowStreamSubscriber) Subscribe(_ context.Context, _ *FlowStreamScope, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flowsCh := make(chan apisv1.FlowStreamEvent, len(s.events)+1)
	errCh := make(chan error, 1)
	ready := make(chan struct{})

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
		// Left open: a failure that never reached a live stream must never signal readiness,
		// the same as the real GRPCFlowStreamSubscriber (see grpc.go's Subscribe).
	} else {
		close(ready)
		for _, e := range s.events {
			flowsCh <- e
		}
		close(flowsCh)
	}

	return flowsCh, errCh, ready
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

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?clusterWide=true")
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

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?clusterWide=true")
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

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?clusterWide=true")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var evt apisv1.FlowStreamErrorEvent
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&evt))
	assert.Equal(t, StreamErrorCodeResourceExhausted, evt.Code)
	assert.True(t, evt.Retryable, "the client reconnects off this flag, so it has to reach the client")
}

// StreamErrorCodeForbidden must map to a real 403: FlowStreamClient checks response.status === 403
// specifically to render the missing-flows-grant panel instead of a generic error, the same way it
// already does for the 403 pkg/server/api's own RBAC gate used to return before authorization moved
// to the Flow Aggregator.
func TestStreamFlowsForbiddenIsA403(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamSubscriber{
		err: &StreamError{msg: "not allowed to watch flows cluster-wide", Code: StreamErrorCodeForbidden, Retryable: false},
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?clusterWide=true")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var evt apisv1.FlowStreamErrorEvent
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&evt))
	assert.Equal(t, StreamErrorCodeForbidden, evt.Code)
	assert.False(t, evt.Retryable)
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

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?clusterWide=true")
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

// closeNotifyRecorder adapts httptest.ResponseRecorder to gin's ResponseWriter, whose Stream
// method type-asserts the underlying http.ResponseWriter to http.CloseNotifier (see gin's
// (*responseWriter).CloseNotify) - httptest.ResponseRecorder alone does not implement it, so
// calling c.Stream against a bare recorder panics. closec is never closed: nothing in these
// tests simulates a client disconnect.
type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closec chan bool
}

func newCloseNotifyRecorder() *closeNotifyRecorder {
	return &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closec: make(chan bool)}
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool { return r.closec }

// writeGate blocks the first Write to the underlying recorder until told to proceed, signaling
// started first. TestStreamFlowsErrorSurvivesFlowsChRace uses it to force the exact interleaving
// its race needs: the c.Stream loop's select has to be busy delivering a flow event - not parked
// in select - when the stub buffers its error and closes flowsCh, so that by the time the loop
// returns to select, both cases are already ready simultaneously rather than one becoming ready
// before the loop gets back around to waiting.
type writeGate struct {
	*closeNotifyRecorder
	once    sync.Once
	started chan struct{}
	proceed chan struct{}
}

func newWriteGate() *writeGate {
	return &writeGate{
		closeNotifyRecorder: newCloseNotifyRecorder(),
		started:             make(chan struct{}),
		proceed:             make(chan struct{}),
	}
}

func (w *writeGate) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.proceed
	return w.closeNotifyRecorder.Write(p)
}

// WriteString must funnel through Write rather than httptest.ResponseRecorder's own WriteString
// (which would otherwise be promoted as-is): gin's ResponseWriter writes SSE payloads with
// io.WriteString, which prefers a io.StringWriter implementation over Write when the underlying
// writer has one - bypassing the gate above entirely and writing the event before started fires.
func (w *writeGate) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// flowsChErrChRaceSubscriber sends one flow event and then, once told the caller is busy
// delivering it (see writeGate), fails the same way every GRPCFlowStreamSubscriber.Subscribe error
// path does: buffer err into errCh, then close flowsCh (its deferred close(flowsCh) then
// close(errCh) cleanup, run in that order - see grpc.go's Subscribe), and finally signals
// delivered. The test waits on delivered - not just started - before letting the blocked Write
// return: started and delivered are both unblocked by the same close(s.started), racing the test
// goroutine against this one, so without waiting for delivered too, the test could let Write
// return, and the c.Stream loop back into select, before errCh/flowsCh actually reach their final
// state - the very thing this test exists to force.
type flowsChErrChRaceSubscriber struct {
	err       error
	started   <-chan struct{}
	delivered chan struct{}
}

func (s *flowsChErrChRaceSubscriber) Subscribe(_ context.Context, _ *FlowStreamScope, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flowsCh := make(chan apisv1.FlowStreamEvent, 1)
	errCh := make(chan error, 1)
	ready := make(chan struct{})
	close(ready)
	flowsCh <- apisv1.FlowStreamEvent{Flows: []apisv1.Flow{{ID: "flow-1"}}}
	go func() {
		<-s.started
		errCh <- s.err
		close(flowsCh)
		close(s.delivered)
	}()
	return flowsCh, errCh, ready
}

// When Subscribe's error paths buffer an error into errCh and then close both channels (see
// flowsChErrChRaceSubscriber), the closed flowsCh and the buffered errCh value become two
// independently-ready select cases at once. Go's select picks uniformly among ready cases, so
// without draining errCh in the flowsCh branch, roughly half of all calls would take the
// closed-flowsCh path and silently drop the error. Run enough iterations that a regression would
// very likely produce at least one miss.
//
// Runs under synctest with the handler driven directly (via httptest.ResponseRecorder, see
// closeNotifyRecorder) rather than against a real httptest.NewServer/http.Get pair: an
// httptest.NewServer response body blocks on a real socket, which synctest does not consider
// durably blocked, and the writeGate synchronization below relies on synctest recognizing the
// StreamFlows goroutine as durably blocked in Write so this goroutine's <-w.started only proceeds
// once it truly is - a real server makes both goroutines' progress a matter of OS scheduling
// instead, which is exactly the kind of real-time dependency this rewrite removes.
func TestStreamFlowsErrorSurvivesFlowsChRace(t *testing.T) {
	logger := testr.New(t)

	for i := 0; i < 50; i++ {
		synctest.Test(t, func(t *testing.T) {
			w := newWriteGate()
			stub := &flowsChErrChRaceSubscriber{err: fmt.Errorf("at capacity"), started: w.started, delivered: make(chan struct{})}
			sseHandler := NewSSEHandler(logger, stub)

			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?clusterWide=true", nil)

			done := make(chan struct{})
			go func() {
				defer close(done)
				sseHandler.StreamFlows(c)
			}()

			<-stub.delivered
			close(w.proceed)
			<-done

			body := w.Body.String()
			assert.Contains(t, body, "event:error", "iteration %d: error event must survive the flowsCh/errCh race", i)
			assert.Contains(t, body, "at capacity", "iteration %d", i)
		})
	}
}

// unresponsiveSubscriber stands in for a Flow Aggregator that accepts the call and then never
// responds: Subscribe neither closes ready nor reports an error.
type unresponsiveSubscriber struct{}

func (unresponsiveSubscriber) Subscribe(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	return make(chan apisv1.FlowStreamEvent), make(chan error), make(chan struct{})
}

// When Subscribe never answers, StreamFlows must give up after initialResponseTimeout with a
// retryable pre-200 error, rather than commit to a 200 that would show "Connected" on an empty
// page with no error and no retry.
func TestStreamFlowsInitialResponseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sseHandler := NewSSEHandler(testr.New(t), unresponsiveSubscriber{})
		w := newCloseNotifyRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?clusterWide=true", nil)

		start := time.Now()
		sseHandler.StreamFlows(c)
		assert.Equal(t, initialResponseTimeout, time.Since(start))

		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.NotEqual(t, "text/event-stream", w.Header().Get("Content-Type"))
		var evt apisv1.FlowStreamErrorEvent
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &evt))
		assert.Equal(t, StreamErrorCodeInternal, evt.Code)
		assert.True(t, evt.Retryable)
	})
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

// A stream's scope is what the Flow Aggregator authorizes it against, so every way of failing to
// name exactly one scope has to be a local 400 rather than something the Flow Aggregator is asked
// to adjudicate. Upstream would reject all of these with INVALID_ARGUMENT anyway; answering here
// names the query parameter at fault and costs no connection.
func TestParseFlowStreamScope(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		expected    *FlowStreamScope
		expectError bool
	}{
		{
			name:     "a single observed namespace",
			query:    "observedNamespace=ns-a",
			expected: &FlowStreamScope{ObservedNamespace: "ns-a"},
		},
		{
			name:     "cluster-wide",
			query:    "clusterWide=true",
			expected: &FlowStreamScope{ClusterWide: true},
		},
		{
			name:     "surrounding whitespace is trimmed",
			query:    "observedNamespace=%20ns-a%20",
			expected: &FlowStreamScope{ObservedNamespace: "ns-a"},
		},
		{
			name:     "clusterWide=false alongside a namespace is not a conflict",
			query:    "observedNamespace=ns-a&clusterWide=false",
			expected: &FlowStreamScope{ObservedNamespace: "ns-a"},
		},
		{
			name: "filter namespaces are not a scope",
			// The peer filter must not be mistaken for the scope: the two are separate
			// fields on the wire and a filter Namespace is legal outside the scope.
			query:       "namespaces=ns-a",
			expectError: true,
		},
		{
			name:        "neither set",
			query:       "",
			expectError: true,
		},
		{
			name:        "both set",
			query:       "observedNamespace=ns-a&clusterWide=true",
			expectError: true,
		},
		{
			name:        "blank observed namespace",
			query:       "observedNamespace=%20",
			expectError: true,
		},
		{
			name: "a comma-separated list is rejected, not truncated",
			// Truncating would leave the client believing it observes both.
			query:       "observedNamespace=ns-a,ns-b",
			expectError: true,
		},
		{
			name:        "a repeated parameter is rejected, not truncated",
			query:       "observedNamespace=ns-a&observedNamespace=ns-b",
			expectError: true,
		},
		{
			name:        "clusterWide with a non-boolean value",
			query:       "clusterWide=yes-please",
			expectError: true,
		},
		{
			name:        "a repeated clusterWide parameter is rejected, not silently the first value",
			query:       "clusterWide=true&clusterWide=false",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?"+tt.query, nil)

			scope, err := parseFlowStreamScope(c)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, scope)
		})
	}
}

// StreamFlows must reject a scope-less request itself, before Subscribe, so a request that cannot
// be authorized never costs a connection to the Flow Aggregator.
func TestStreamFlowsRejectsMissingScope(t *testing.T) {
	stub := &stubFlowStreamSubscriber{}
	handler := NewSSEHandler(testr.New(t), stub)
	ts := httptest.NewServer(newTestRouter(handler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
