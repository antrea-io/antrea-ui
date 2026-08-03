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

package session

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// TransportKeyK8s is the cache key for the transport used to reach the kube-apiserver. It is
// unversioned: unlike the Antrea Service, the API server's TLS settings do not change under a live
// session. See Session.transportFor for the key format.
const TransportKeyK8s = "k8s"

// RequestAuth is the resolved identity for one in-flight request. The authentication middleware
// puts it in the request context; handlers that call Kubernetes on the user's behalf read it back
// out with RequestAuthFrom.
type RequestAuth struct {
	Mode     Mode
	Username string

	store   Store
	session *Session
	// ephemeralCredential is the credential of a bearer-authenticated request, which has no
	// session to hold one. It is the zero Credential for session-backed requests: a session's
	// credential is deliberately not snapshotted here, because Credential.Zero() overwrites
	// the byte slices in place, and a concurrent refresh or eviction would turn a snapshot
	// into zeros underneath an in-flight request. Read it through Credential() instead.
	ephemeralCredential Credential
}

// NewSessionAuth builds a RequestAuth backed by a stored session, so transports are cached for
// the session's lifetime and the session can be kept alive or invalidated.
func NewSessionAuth(store Store, s *Session) *RequestAuth {
	return &RequestAuth{
		Mode:     s.Mode(),
		Username: s.Username(),
		store:    store,
		session:  s,
	}
}

// NewEphemeralAuth builds a RequestAuth for a request that authenticated with an Authorization:
// Bearer header rather than a session cookie. No session is created: the credential lives only for
// the duration of the request.
//
// username is the identity the API server resolved the token to. Callers must have validated the
// token before calling this: unlike a session, whose credential was checked when the session was
// created, nothing downstream re-checks an ephemeral one, and two routes never present it to
// Kubernetes at all.
//
// Mode is always ModeSAToken, whatever the token actually is - it says how the caller
// authenticated to antrea-ui, not what kind of token they hold. Do not use it to grant anything:
// the one Mode check today (UpdatePassword requiring ModeAdmin) is a denial, which is the safe
// direction.
func NewEphemeralAuth(cred Credential, username string) *RequestAuth {
	return &RequestAuth{
		Mode:                ModeSAToken,
		Username:            username,
		ephemeralCredential: cred,
	}
}

// Credential returns the credential to present to Kubernetes for this request. Do not log it.
//
// It reads through to the session on every call rather than returning a snapshot taken when the
// request was authenticated. The session zeroes its credential material in place on refresh and on
// eviction, so a snapshot held across either would silently become zeros while the request was
// still using it. The returned struct still shares its byte slices with the session, so callers
// must use it immediately and must neither retain nor modify it.
//
// Prefer TransportFor, which handles all of this: this is for the callers that genuinely need the
// credential itself rather than a transport built from it.
func (ra *RequestAuth) Credential() Credential {
	if ra.session != nil {
		return ra.session.Credential()
	}
	return ra.ephemeralCredential
}

// Session returns the backing session, or nil for an ephemeral bearer request.
func (ra *RequestAuth) Session() *Session { return ra.session }

// SessionID returns the ID of the backing session, or "" for an ephemeral bearer request.
func (ra *RequestAuth) SessionID() string {
	if ra.session == nil {
		return ""
	}
	return ra.session.ID()
}

// TransportFor returns an http.RoundTripper that authenticates as this request's credential.
//
// key names the upstream the transport talks to (the kube-apiserver, the Antrea Service), since
// each has its own TLS settings and therefore its own transport. Session-backed requests get a
// transport cached under that key for the session's lifetime; ephemeral ones build a fresh one,
// which is cheap because an ephemeral credential is always a bearer token layered over a shared
// base transport.
func (ra *RequestAuth) TransportFor(key string, build TransportBuilder) (http.RoundTripper, error) {
	if build == nil {
		return nil, fmt.Errorf("no transport builder provided")
	}
	if ra.session != nil {
		return ra.session.transportFor(key, build)
	}
	rt, _, err := build(&ra.ephemeralCredential)
	return rt, err
}

// KeepAlive re-resolves the session the way an ordinary request would, and reports whether it is
// still valid. A long-running request (the flow SSE stream) calls this periodically: it stops the
// stream from idling out its own session, and it tells the stream when the session ended (logout
// in another tab, the absolute lifetime cap, a credential that can no longer be renewed).
//
// An attached stream keeps its session alive whether or not the browser tab is in the foreground.
// That is a deliberate exception, and the only one: everywhere else, "idle" means "no visible tab",
// because the frontend's own keepalive (useSessionKeepalive in App.tsx) pings /auth/session only
// while document.visibilityState is "visible". A flow-visibility tab is something people background
// on purpose and expect to still be collecting when they come back, so the stream extends the
// session on its own. The absolute lifetime cap still applies, and so does the credential's.
//
// It goes through Store.Get, which refreshes the credential on the way, and that is the whole
// reason the exception is safe to make. A tick that only bumped last-seen would let a backgrounded
// stream hold an OIDC session open long past the point its id_token expired - store.alive
// deliberately does not treat a refreshable credential's expiry as terminal - so a token revoked at
// the provider would keep streaming to the 12h cap with nothing left to notice. Anything that
// extends a session has to renew it too. Get is cheap on the common tick: maybeRefresh returns
// immediately unless the credential is inside its refresh window, and is single-flight per session
// when it is.
//
// An ephemeral bearer request has no session, so there is nothing to keep alive - but its
// credential still has to bound the request. Nothing else does: a bearer token is checked when the
// request is authenticated, and the flow stream then runs for hours without presenting it to
// anything. Credential.ExpiresAt is trustworthy here because the token was validated against the
// API server first (see authn.Resolve); an opaque token with no expiry claim has none to enforce,
// and is bounded only by the client disconnecting.
func (ra *RequestAuth) KeepAlive(ctx context.Context) bool {
	if ra.session == nil {
		if ra.ephemeralCredential.ExpiresAt.IsZero() {
			return true
		}
		return time.Now().Before(ra.ephemeralCredential.ExpiresAt)
	}
	_, err := ra.store.Get(ctx, ra.session.ID())
	return err == nil
}

// Invalidate drops the session. It is called when the API server rejects the credential itself
// (upstream 401), which means no later request with this session can succeed either. An upstream
// 403 is an authorization failure, not a credential failure, and must not come through here.
func (ra *RequestAuth) Invalidate() {
	if ra.session == nil {
		return
	}
	ra.store.Delete(ra.session.ID())
}

type requestAuthKeyType struct{}

var requestAuthKey requestAuthKeyType

// WithRequestAuth returns a context carrying ra.
func WithRequestAuth(ctx context.Context, ra *RequestAuth) context.Context {
	return context.WithValue(ctx, requestAuthKey, ra)
}

// RequestAuthFrom retrieves the RequestAuth stored by the authentication middleware.
func RequestAuthFrom(ctx context.Context) (*RequestAuth, bool) {
	ra, ok := ctx.Value(requestAuthKey).(*RequestAuth)
	return ra, ok
}
