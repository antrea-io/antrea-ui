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
)

// silentSubscriber never sends anything, which is the interesting case here: only the keepalive
// ticker runs, and that is where the session is re-checked.
type silentSubscriber struct{}

func (s *silentSubscriber) Subscribe(ctx context.Context, _ *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent)
	errCh := make(chan error)
	go func() {
		<-ctx.Done()
		close(flowsCh)
		close(errCh)
	}()
	return flowsCh, errCh
}

// sessionRouter serves the SSE endpoint behind a session, the way the real API server does.
func sessionRouter(handler *SSEHandler, store session.Store, sessionID string) *gin.Engine {
	router := gin.New()
	router.GET("/api/v1/flows/stream", func(c *gin.Context) {
		sess, err := store.Get(c.Request.Context(), sessionID)
		if err != nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		ra := session.NewSessionAuth(store, sess)
		c.Request = c.Request.WithContext(session.WithRequestAuth(c.Request.Context(), ra))
		handler.StreamFlows(c)
	})
	return router
}

// A flow stream is a single request that can run for hours. Since last-seen is only bumped at
// request start, an active stream would otherwise idle out its own session while streaming.
func TestStreamKeepsSessionAlive(t *testing.T) {
	const idleTimeout = 200 * time.Millisecond

	store := session.NewStore(testr.New(t), session.Options{
		IdleTimeout: idleTimeout,
		MaxLifetime: time.Hour,
		MaxSessions: 10,
	})
	sess, err := store.Create(&session.Spec{
		Mode:       session.ModeSAToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	require.NoError(t, err)

	handler := NewSSEHandler(testr.New(t), &silentSubscriber{})
	handler.keepAliveInterval = 20 * time.Millisecond
	ts := httptest.NewServer(sessionRouter(handler, store, sess.ID()))
	defer ts.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/flows/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Read keepalives for well past the idle timeout. If the stream did not touch its session,
	// the session would be gone by now.
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(4 * idleTimeout)
	keepalives := 0
	for time.Now().Before(deadline) && scanner.Scan() {
		if strings.Contains(scanner.Text(), "keepalive") {
			keepalives++
		}
	}
	assert.Greater(t, keepalives, 1, "expected the stream to keep emitting keepalives")

	_, err = store.Get(t.Context(), sess.ID())
	assert.NoError(t, err, "the session should have been kept alive by the active stream")
}

// Nothing else terminates an in-flight stream when its session ends, so a user who logs out in
// another tab (or hits the absolute lifetime cap) would keep receiving flow data.
func TestStreamStopsWhenSessionEnds(t *testing.T) {
	store := session.NewStore(testr.New(t), session.Options{
		IdleTimeout: time.Hour,
		MaxLifetime: time.Hour,
		MaxSessions: 10,
	})
	sess, err := store.Create(&session.Spec{
		Mode:       session.ModeSAToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	require.NoError(t, err)

	handler := NewSSEHandler(testr.New(t), &silentSubscriber{})
	handler.keepAliveInterval = 20 * time.Millisecond
	ts := httptest.NewServer(sessionRouter(handler, store, sess.ID()))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/flows/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Simulate a logout from another tab.
	time.AfterFunc(50*time.Millisecond, func() { store.Delete(sess.ID()) })

	// The stream must end on its own, without the client aborting it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() { //nolint:revive // drain until EOF
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not close after the session was deleted")
	}
}

// The handler cannot check whether a session is still alive without the identity the
// authentication middleware resolves. If that is missing, the handler was wired up wrong, and a
// stream that can run for hours must not be the thing that discovers it: it fails closed.
func TestStreamStopsWithoutResolvedIdentity(t *testing.T) {
	handler := NewSSEHandler(testr.New(t), &silentSubscriber{})
	handler.keepAliveInterval = 20 * time.Millisecond

	// Deliberately no session.WithRequestAuth on the request context.
	router := gin.New()
	router.GET("/api/v1/flows/stream", handler.StreamFlows)
	ts := httptest.NewServer(router)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/flows/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() { //nolint:revive // drain until EOF
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream kept running with no resolved identity")
	}
}

// ephemeralRouter serves the SSE endpoint behind an Authorization: Bearer credential, the way the
// real API server does for a non-browser client. There is no session behind it.
func ephemeralRouter(handler *SSEHandler, cred session.Credential) *gin.Engine {
	router := gin.New()
	router.GET("/api/v1/flows/stream", func(c *gin.Context) {
		ra := session.NewEphemeralAuth(cred, "alice")
		c.Request = c.Request.WithContext(session.WithRequestAuth(c.Request.Context(), ra))
		handler.StreamFlows(c)
	})
	return router
}

// A bearer request has no session, so none of the session lifetimes bound it. The credential's own
// expiry is what has to stop a stream that would otherwise run until the client disconnects - the
// flow stream never presents the credential again, so nothing else would ever notice.
func TestStreamStopsWhenBearerCredentialExpires(t *testing.T) {
	readUntilClosed := func(t *testing.T, cred session.Credential) int {
		t.Helper()
		handler := NewSSEHandler(testr.New(t), &silentSubscriber{})
		handler.keepAliveInterval = 20 * time.Millisecond
		ts := httptest.NewServer(ephemeralRouter(handler, cred))
		defer ts.Close()

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/flows/stream", nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		scanner := bufio.NewScanner(resp.Body)
		deadline := time.Now().Add(300 * time.Millisecond)
		keepalives := 0
		for time.Now().Before(deadline) && scanner.Scan() {
			if strings.Contains(scanner.Text(), "keepalive") {
				keepalives++
			}
		}
		return keepalives
	}

	t.Run("expired credential closes the stream", func(t *testing.T) {
		cred := session.Credential{
			Kind:      session.KindBearer,
			Token:     []byte("tok"),
			ExpiresAt: time.Now().Add(-1 * time.Minute),
		}
		assert.Zero(t, readUntilClosed(t, cred), "an expired credential must not keep streaming")
	})

	t.Run("valid credential keeps streaming", func(t *testing.T) {
		cred := session.Credential{
			Kind:      session.KindBearer,
			Token:     []byte("tok"),
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}
		assert.Greater(t, readUntilClosed(t, cred), 1)
	})

	// An opaque token carries no expiry claim, so there is nothing to enforce and the stream
	// runs until the client goes away. Documented, not accidental.
	t.Run("credential with no expiry keeps streaming", func(t *testing.T) {
		cred := session.Credential{Kind: session.KindBearer, Token: []byte("opaque")}
		assert.Greater(t, readUntilClosed(t, cred), 1)
	})
}
