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
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
)

// controlledSubscriber makes readiness, event delivery, and subscription cancellation observable
// independently of the handler. It leaves the event channels open on cancellation so that the
// handler must notice its context or its failed write rather than depend on channel closure.
type controlledSubscriber struct {
	flows  chan apisv1.FlowStreamEvent
	errors chan error
	opened chan context.Context
	done   chan struct{}
}

func newControlledSubscriber() *controlledSubscriber {
	return &controlledSubscriber{
		flows: make(chan apisv1.FlowStreamEvent), errors: make(chan error),
		opened: make(chan context.Context, 1), done: make(chan struct{}),
	}
}

func (s *controlledSubscriber) Subscribe(ctx context.Context, _ *FlowStreamScope, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	ready := make(chan struct{})
	close(ready)
	s.opened <- ctx
	go func() {
		<-ctx.Done()
		close(s.done)
	}()
	return s.flows, s.errors, ready
}

// flowPipeListener serves a single net.Pipe connection. Unlike httptest's in-memory network,
// net.Pipe has no buffer, so the server's write blocks exactly when the client stops reading.
type flowPipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
}

func (l *flowPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *flowPipeListener) Close() error   { close(l.closed); return nil }
func (l *flowPipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "flow-pipe", Net: "pipe"} }

// unwrappingWriter models middleware which exposes ResponseController capabilities through
// Unwrap instead of implementing SetWriteDeadline itself.
type unwrappingWriter struct{ http.ResponseWriter }

func (w unwrappingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w unwrappingWriter) Flush()                      { w.ResponseWriter.(http.Flusher).Flush() }
func (w unwrappingWriter) CloseNotify() <-chan bool {
	return w.ResponseWriter.(http.CloseNotifier).CloseNotify()
}

func TestStreamFlowsUnreadStream(t *testing.T) {
	for _, tc := range []struct {
		name        string
		readHeaders bool
		event       *apisv1.FlowStreamEvent
		streamErr   error
		wantElapsed time.Duration
	}{
		{name: "headers", wantElapsed: flowWriteTimeout},
		{name: "keepalive", readHeaders: true, wantElapsed: keepAliveInterval + flowWriteTimeout},
		{name: "flow flush", readHeaders: true, event: &apisv1.FlowStreamEvent{
			Flows: []apisv1.Flow{{ID: "flow-1"}},
		}, wantElapsed: flowWriteTimeout},
		{name: "flow write", readHeaders: true, event: &apisv1.FlowStreamEvent{
			Flows: []apisv1.Flow{{ID: strings.Repeat("x", 32*1024)}},
		}, wantElapsed: flowWriteTimeout},
		{name: "dropped", readHeaders: true, event: &apisv1.FlowStreamEvent{
			DroppedCount: 1,
		}, wantElapsed: flowWriteTimeout},
		{name: "error flush", readHeaders: true, streamErr: errors.New("upstream failed"), wantElapsed: flowWriteTimeout},
		{name: "error write", readHeaders: true, streamErr: errors.New(strings.Repeat("x", 32*1024)), wantElapsed: flowWriteTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				subscriber := newControlledSubscriber()
				handler := NewSSEHandler(testr.New(t), subscriber)
				handlerDone := make(chan struct{})
				router := gin.New()
				router.GET("/api/v1/flows/stream", func(c *gin.Context) {
					defer close(handlerDone)
					c.Request = c.Request.WithContext(session.WithRequestAuth(c.Request.Context(), session.NewEphemeralAuth(
						session.Credential{Kind: session.KindBearer, Token: []byte("token")}, "alice")))
					handler.StreamFlows(c)
				})
				serverConn, clientConn := net.Pipe()
				listener := &flowPipeListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
				listener.connections <- serverConn
				srv := &http.Server{
					Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						router.ServeHTTP(unwrappingWriter{w}, r)
					}),
					ReadHeaderTimeout: time.Minute,
				}
				serverDone := make(chan error, 1)
				go func() { serverDone <- srv.Serve(listener) }()
				t.Cleanup(func() {
					assert.NoError(t, srv.Close())
					assert.NoError(t, clientConn.Close())
					assert.ErrorIs(t, <-serverDone, http.ErrServerClosed)
				})

				req := httptest.NewRequest(http.MethodGet, "http://antrea-ui/api/v1/flows/stream?clusterWide=true", nil)
				require.NoError(t, req.Write(clientConn))
				ctx := <-subscriber.opened
				if tc.readHeaders {
					resp, err := http.ReadResponse(bufio.NewReader(clientConn), req)
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, resp.StatusCode)
					require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
				}

				// No reads occur after this point. The header handshake prevents accidentally
				// testing the header flush instead of the requested body write or flush.
				start := time.Now()
				stillOpen := time.After(2 * (keepAliveInterval + flowWriteTimeout))
				if tc.event != nil || tc.streamErr != nil {
					// Make the message's deadline distinct from the headers' deadline without
					// making a keepalive ready. synctest advances fake time only.
					synctest.Sleep(keepAliveInterval / 2)
					start = time.Now()
					if tc.event != nil {
						select {
						case subscriber.flows <- *tc.event:
						case <-stillOpen:
							require.FailNow(t, "handler did not receive the flow event")
						}
					} else {
						select {
						case subscriber.errors <- tc.streamErr:
						case <-stillOpen:
							require.FailNow(t, "handler did not receive the terminal error")
						}
					}
				}
				select {
				case <-handlerDone:
				case <-stillOpen:
					require.FailNow(t, "an unread stream kept its handler running")
				}
				assert.Equal(t, tc.wantElapsed, time.Since(start))
				synctest.Wait()
				assert.ErrorIs(t, ctx.Err(), context.Canceled)
				select {
				case <-subscriber.done:
				default:
					require.FailNow(t, "the unread stream's subscription is still active")
				}
			})
		})
	}
}

func TestStreamFlowsUnsupportedWriteDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		subscriber := newControlledSubscriber()
		handler := NewSSEHandler(testr.New(t), subscriber)
		// A plain ResponseRecorder has no SetWriteDeadline. The handler must fail closed
		// rather than start a stream that can keep its subscription indefinitely, and must
		// say so with a permanent error instead of an empty 200 stream that clients reconnect to.
		w := httptest.NewRecorder()
		newTestRouter(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?clusterWide=true", nil))
		ctx := <-subscriber.opened
		assert.ErrorIs(t, ctx.Err(), context.Canceled)
		assert.False(t, w.Flushed)
		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Header().Get("Content-Type"), "text/event-stream")
		var evt apisv1.FlowStreamErrorEvent
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &evt))
		assert.Equal(t, StreamErrorCodeInternal, evt.Code)
		assert.False(t, evt.Retryable)
	})
}

// failingStreamWriter reports failures without canceling the request, as a middleware writer
// might. The handler must stop itself and cancel the subscription on these paths as well.
type failingStreamWriter struct {
	*closeNotifyRecorder
	deadlineCalls int
	failDeadline  int
	writeCalls    int
}

func (w *failingStreamWriter) SetWriteDeadline(time.Time) error {
	w.deadlineCalls++
	if w.deadlineCalls == w.failDeadline {
		return errors.New("cannot set write deadline")
	}
	return nil
}

func (w *failingStreamWriter) Write([]byte) (int, error) {
	w.writeCalls++
	return 0, errors.New("client write failed")
}

func TestStreamFlowsWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failDeadline  int
		wantWrites    int
		wantDeadlines int
	}{
		// The failed deadline is reported with a JSON error body, which the writer also rejects.
		{name: "initial deadline", failDeadline: 1, wantWrites: 1, wantDeadlines: 1},
		{name: "event deadline", failDeadline: 2, wantDeadlines: 2},
		{name: "event write", wantWrites: 1, wantDeadlines: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				subscriber := newControlledSubscriber()
				subscriber.flows = make(chan apisv1.FlowStreamEvent, 1)
				// Failure to write the dropped event must also prevent the following flow
				// event. The channel stays open, so an ignored error cannot finish the test.
				subscriber.flows <- apisv1.FlowStreamEvent{
					DroppedCount: 1, Flows: []apisv1.Flow{{ID: "flow-1"}},
				}
				w := &failingStreamWriter{closeNotifyRecorder: newCloseNotifyRecorder(), failDeadline: tc.failDeadline}
				handler := NewSSEHandler(testr.New(t), subscriber)
				start := time.Now()
				newTestRouter(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?clusterWide=true", nil))
				assert.Zero(t, time.Since(start), "a failed write must return without waiting for keepalive")
				assert.Equal(t, tc.wantWrites, w.writeCalls)
				assert.Equal(t, tc.wantDeadlines, w.deadlineCalls)
				assert.ErrorIs(t, (<-subscriber.opened).Err(), context.Canceled)
			})
		})
	}
}
