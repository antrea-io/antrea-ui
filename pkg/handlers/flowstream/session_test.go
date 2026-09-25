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

// startSilentStream runs StreamFlows against a silentSubscriber in the background, with ra (if
// not nil) as the request's resolved identity, the way the authentication middleware would attach
// it. It returns the recorder the stream writes to and a channel closed once StreamFlows returns.
// The request's context is canceled when the calling test ends, which is what ends a stream that
// does not end on its own.
//
// The handler is driven directly through a recorder rather than through httptest.NewServer, so
// these tests can run under testing/synctest: a real server's response body blocks on a socket,
// which synctest does not consider durably blocked. The recorder must only be read once the
// handler goroutine is blocked (after synctest.Wait) or has returned (after done is closed).
func startSilentStream(t *testing.T, ra *session.RequestAuth) (*closeNotifyRecorder, <-chan struct{}) {
	t.Helper()
	ctx := t.Context()
	if ra != nil {
		ctx = session.WithRequestAuth(ctx, ra)
	}
	w := newCloseNotifyRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?clusterWide=true", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewSSEHandler(testr.New(t), &silentSubscriber{}).StreamFlows(c)
	}()
	return w, done
}

// requireStreamCommitted fails the test unless the handler committed to the SSE stream: a 200 with
// the SSE Content-Type, flushed to the client. Checking w.Code alone would prove nothing, since a
// recorder reports 200 until something else is written.
func requireStreamCommitted(t *testing.T, w *closeNotifyRecorder) {
	t.Helper()
	require.True(t, w.Flushed, "the stream was never committed")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
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

		w, done := startSilentStream(t, session.NewSessionAuth(store, sess))

		// Stream for well past the idle timeout. If the stream did not touch its session, the
		// session would be gone by now.
		time.Sleep(4 * idleTimeout)
		synctest.Wait()
		requireStreamCommitted(t, w)
		assert.Greater(t, strings.Count(w.Body.String(), "keepalive"), 1, "expected the stream to keep emitting keepalives")
		select {
		case <-done:
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

		w, done := startSilentStream(t, session.NewSessionAuth(store, sess))
		synctest.Wait()
		requireStreamCommitted(t, w)

		// Simulate a logout from another tab. The stream must end on its own, without the
		// client aborting it.
		store.Delete(sess.ID())
		requireStreamEnds(t, done, "stream did not close after the session was deleted")
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
		w, done := startSilentStream(t, nil)
		requireStreamEnds(t, done, "stream kept running with no resolved identity")
		requireStreamCommitted(t, w)
		assert.Zero(t, strings.Count(w.Body.String(), "keepalive"), "no keepalive may be sent without a resolved identity")
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
			w, done := startSilentStream(t, ephemeralAuth(time.Now().Add(-1*time.Minute)))
			requireStreamEnds(t, done, "an expired credential must not keep streaming")
			requireStreamCommitted(t, w)
			assert.Zero(t, strings.Count(w.Body.String(), "keepalive"), "an expired credential must not keep streaming")
		})
	})

	t.Run("valid credential keeps streaming", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, done := startSilentStream(t, ephemeralAuth(time.Now().Add(1*time.Hour)))
			time.Sleep(3 * keepAliveInterval)
			synctest.Wait()
			requireStreamCommitted(t, w)
			assert.Greater(t, strings.Count(w.Body.String(), "keepalive"), 1)
			select {
			case <-done:
				require.FailNow(t, "a valid credential must keep streaming")
			default:
			}
		})
	})

	// An opaque token carries no expiry claim, so there is nothing to enforce and the stream
	// runs until the client goes away. Documented, not accidental.
	t.Run("credential with no expiry keeps streaming", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w, done := startSilentStream(t, ephemeralAuth(time.Time{}))
			time.Sleep(3 * keepAliveInterval)
			synctest.Wait()
			requireStreamCommitted(t, w)
			assert.Greater(t, strings.Count(w.Body.String(), "keepalive"), 1)
			select {
			case <-done:
				require.FailNow(t, "a credential with no expiry must keep streaming")
			default:
			}
		})
	})
}
