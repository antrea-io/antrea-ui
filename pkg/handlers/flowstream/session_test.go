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
	"bytes"
	"context"
	"io"
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

// silentSubscriber closes ready immediately and then never sends anything else, which is the
// interesting case here: only the keepalive ticker runs, and that is where the session is
// re-checked. It commits to the stream the way a live-but-quiet Flow Aggregator would (a narrow
// filter matching nothing), as opposed to the initialResponseTimeout fallback, which is for a
// Flow Aggregator that accepts the call and then never responds - a different case, and not what
// any test below is exercising.
type silentSubscriber struct{}

func (s *silentSubscriber) Subscribe(ctx context.Context, _ *FlowStreamScope, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flowsCh := make(chan apisv1.FlowStreamEvent)
	errCh := make(chan error)
	ready := make(chan struct{})
	close(ready)
	go func() {
		<-ctx.Done()
		close(flowsCh)
		close(errCh)
	}()
	return flowsCh, errCh, ready
}

// silentStream is a flow stream opened by startSilentStream, as its client sees it.
type silentStream struct {
	resp *http.Response
	// committedAfter is how long the client waited for the response header.
	committedAfter time.Duration
	// received accumulates the response body as it arrives. It is written to in the background:
	// use body to read it.
	received bytes.Buffer
	// disconnect makes the client go away, by canceling its request.
	disconnect context.CancelFunc
	// done is closed once StreamFlows returns.
	done <-chan struct{}
}

// body returns what the client has received of the response body so far. It waits for the bubble
// to settle first, so that this includes everything the handler has written so far.
func (s *silentStream) body() string {
	synctest.Wait()
	return s.received.String()
}

// startSilentStream opens a flow stream against a silentSubscriber, with ra (if not nil) as the
// request's resolved identity, the way the authentication middleware would attach it. It returns
// once the client has received the response header. The client goes away when the calling test
// ends, which is what ends a stream that does not end on its own.
//
// The stream is served by a real server, so that these tests cover what a client actually gets,
// and what happens when it goes away. The server uses the in-memory network of
// httptest.NewTestServer and not a loopback socket, as these tests run under testing/synctest: a
// goroutine blocked on real network I/O is never durably blocked.
func startSilentStream(t *testing.T, ra *session.RequestAuth) *silentStream {
	t.Helper()
	return startSilentStreamFrom(t, &silentSubscriber{}, ra)
}

// startSilentStreamFrom is startSilentStream for another subscriber which, like silentSubscriber,
// confirms the stream right away and then sends nothing.
func startSilentStreamFrom(t *testing.T, subscriber FlowStreamSubscriber, ra *session.RequestAuth) *silentStream {
	t.Helper()
	handler := NewSSEHandler(testr.New(t), subscriber)
	done := make(chan struct{})
	router := gin.New()
	router.GET("/api/v1/flows/stream", func(c *gin.Context) {
		defer close(done)
		if ra != nil {
			c.Request = c.Request.WithContext(session.WithRequestAuth(c.Request.Context(), ra))
		}
		handler.StreamFlows(c)
	})
	srv := httptest.NewTestServer(t, router)
	// Only the server's own client can reach it. The first call to Client is also what starts
	// the server and sets its URL.
	client := srv.Client()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/flows/stream?clusterWide=true", nil)
	require.NoError(t, err)
	start := time.Now()
	resp, err := client.Do(req)
	require.NoError(t, err)
	s := &silentStream{resp: resp, committedAfter: time.Since(start), disconnect: cancel, done: done}
	// Returns when the stream ends or when the client goes away, which is no later than the end
	// of the test: t.Context is canceled before any cleanup runs, and with it the request.
	go func() {
		defer resp.Body.Close()
		_, _ = io.Copy(&s.received, resp.Body)
	}()
	return s
}

// requireStreamCommitted fails the test unless the handler committed to the SSE stream as soon as
// the subscriber confirmed that it was live: a 200 with the SSE Content-Type, which the client got
// without having to wait for the first keepalive to flush it.
func requireStreamCommitted(t *testing.T, s *silentStream) {
	t.Helper()
	require.Zero(t, s.committedAfter, "the stream was not committed right away")
	require.Equal(t, http.StatusOK, s.resp.StatusCode)
	require.Equal(t, "text/event-stream", s.resp.Header.Get("Content-Type"))
}

// requireStreamEnds fails the test unless the stream started by startSilentStream ends on its own
// within two keepalive intervals. Every reason to stop these tests set up is in place before the
// first tick, so one interval is the longest it can take to notice; the second is margin.
func requireStreamEnds(t *testing.T, done <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * keepAliveInterval):
		require.FailNow(t, msg)
	}
}

func newSessionStore(t *testing.T, idleTimeout time.Duration) session.Store {
	t.Helper()
	return session.NewStore(testr.New(t), session.Options{
		IdleTimeout: idleTimeout,
		MaxLifetime: time.Hour,
		MaxSessions: 10,
	})
}

// A flow stream is a single request that can run for hours. Since last-seen is only bumped at
// request start, an active stream would otherwise idle out its own session while streaming.
func TestStreamKeepsSessionAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const idleTimeout = 3 * keepAliveInterval

		store := newSessionStore(t, idleTimeout)
		sess, err := store.Create(&session.Spec{
			Mode:       session.ModeToken,
			Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
		})
		require.NoError(t, err)

		s := startSilentStream(t, session.NewSessionAuth(store, sess))

		// Stream for well past the idle timeout. If the stream did not touch its session, the
		// session would be gone by now.
		synctest.Sleep(4 * idleTimeout)
		requireStreamCommitted(t, s)
		assert.Greater(t, strings.Count(s.body(), "keepalive"), 1, "expected the stream to keep emitting keepalives")
		select {
		case <-s.done:
			require.FailNow(t, "the stream must still be running")
		default:
		}

		_, err = store.Get(t.Context(), sess.ID())
		assert.NoError(t, err, "the session should have been kept alive by the active stream")
	})
}

// Nothing else terminates an in-flight stream when its session ends, so a user who logs out in
// another tab (or hits the absolute lifetime cap) would keep receiving flow data.
func TestStreamStopsWhenSessionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newSessionStore(t, time.Hour)
		sess, err := store.Create(&session.Spec{
			Mode:       session.ModeToken,
			Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
		})
		require.NoError(t, err)

		s := startSilentStream(t, session.NewSessionAuth(store, sess))
		requireStreamCommitted(t, s)

		// Simulate a logout from another tab. The stream must end on its own, without the
		// client aborting it.
		store.Delete(sess.ID())
		requireStreamEnds(t, s.done, "stream did not close after the session was deleted")
	})
}

// The handler cannot check whether a session is still alive without the identity the
// authentication middleware resolves. If that is missing, the handler was wired up wrong, and a
// stream that can run for hours must not be the thing that discovers it: it fails closed.
func TestStreamStopsWithoutResolvedIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Deliberately no RequestAuth on the request context. The stream commits first: the
		// identity is only needed on the first keepalive tick, and failing it there is the only
		// way this stream can end before the test does.
		s := startSilentStream(t, nil)
		requireStreamEnds(t, s.done, "stream kept running with no resolved identity")
		requireStreamCommitted(t, s)
		assert.Zero(t, strings.Count(s.body(), "keepalive"), "no keepalive may be sent without a resolved identity")
	})
}

// A bearer request has no session, so none of the session lifetimes bound it. The credential's own
// expiry is what has to stop a stream that would otherwise run until the client disconnects - the
// flow stream never presents the credential again, so nothing else would ever notice.
func TestStreamStopsWhenBearerCredentialExpires(t *testing.T) {
	// ephemeralAuth is the identity the authentication middleware resolves for an
	// Authorization: Bearer request from a non-browser client. There is no session behind it.
	ephemeralAuth := func(expiresAt time.Time) *session.RequestAuth {
		return session.NewEphemeralAuth(session.Credential{
			Kind:      session.KindBearer,
			Token:     []byte("tok"),
			ExpiresAt: expiresAt,
		}, "alice")
	}

	t.Run("expired credential closes the stream", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := startSilentStream(t, ephemeralAuth(time.Now().Add(-1*time.Minute)))
			requireStreamEnds(t, s.done, "an expired credential must not keep streaming")
			requireStreamCommitted(t, s)
			assert.Zero(t, strings.Count(s.body(), "keepalive"), "an expired credential must not keep streaming")
		})
	})

	t.Run("valid credential keeps streaming", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := startSilentStream(t, ephemeralAuth(time.Now().Add(1*time.Hour)))
			synctest.Sleep(3 * keepAliveInterval)
			requireStreamCommitted(t, s)
			assert.Greater(t, strings.Count(s.body(), "keepalive"), 1)
			select {
			case <-s.done:
				require.FailNow(t, "a valid credential must keep streaming")
			default:
			}
		})
	})

	// An opaque token carries no expiry claim, so there is nothing to enforce and the stream
	// runs until the client goes away. Documented, not accidental.
	t.Run("credential with no expiry keeps streaming", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := startSilentStream(t, ephemeralAuth(time.Time{}))
			synctest.Sleep(3 * keepAliveInterval)
			requireStreamCommitted(t, s)
			assert.Greater(t, strings.Count(s.body(), "keepalive"), 1)
			select {
			case <-s.done:
				require.FailNow(t, "a credential with no expiry must keep streaming")
			default:
			}
		})
	})
}

// obliviousSubscriber commits to the stream like silentSubscriber does, but then ignores its
// context: its channels stay open when the request is canceled, so it is never what ends a stream.
type obliviousSubscriber struct{}

func (obliviousSubscriber) Subscribe(context.Context, *FlowStreamScope, *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	ready := make(chan struct{})
	close(ready)
	return make(chan apisv1.FlowStreamEvent), make(chan error), ready
}

// Nothing above ends a stream whose session and credential stay valid: it runs until its client
// goes away. The handler has to notice that by itself and return right away. It must not depend on
// its subscriber to end the stream, nor wait for the next keepalive, when writing to the client
// fails.
func TestStreamStopsWhenClientDisconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A credential with no expiry, and a subscriber which does not react to the request
		// being canceled: only the handler noticing that its client is gone can end this
		// stream.
		s := startSilentStreamFrom(t, obliviousSubscriber{}, session.NewEphemeralAuth(session.Credential{
			Kind:  session.KindBearer,
			Token: []byte("tok"),
		}, "alice"))
		requireStreamCommitted(t, s)
		synctest.Wait()
		select {
		case <-s.done:
			require.FailNow(t, "the stream must still be running")
		default:
		}

		s.disconnect()
		// No time passes in the bubble while waiting, so a handler that only notices at the
		// next keepalive is still running at this point.
		synctest.Wait()
		select {
		case <-s.done:
		default:
			require.FailNow(t, "the stream kept running after its client went away")
		}
	})
}
