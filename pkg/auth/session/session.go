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

// Package session implements antrea-ui's server-side session store.
//
// A session holds the Kubernetes credential that antrea-ui presents to the kube-apiserver on
// behalf of the end user. The browser only ever holds an opaque session ID (see the
// antrea-ui-session cookie); the credential itself never leaves the backend's memory.
//
// The credential material held here is the user's own Kubernetes credential. It must never be
// written to disk, included in a log line (at any verbosity) or in an error message, or exposed
// through a debug endpoint. Credential and Session are deliberately unprintable: see
// Credential.String and Credential.MarshalJSON.
package session

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Kind identifies how a Credential authenticates to the Kubernetes API server.
type Kind string

const (
	// KindBearer authenticates with a bearer token: an OIDC id_token (mode 1), a pasted
	// ServiceAccount token (mode 5), or a token extracted from a kubeconfig (mode 3).
	KindBearer Kind = "bearer"
	// KindCert authenticates with a TLS client certificate extracted from a kubeconfig
	// (mode 3). Unlike a bearer token, this is a per-connection credential and so needs its
	// own transport (and its own connection pool).
	KindCert Kind = "cert"
	// KindImpersonate authenticates as antrea-ui's own ServiceAccount and asks the API server
	// to authorize the request as UserName instead. This is how the static admin password
	// mode (mode 4) reaches the API server, as antrea-ui-admin.
	KindImpersonate Kind = "impersonate"
)

// Mode identifies how the user authenticated to antrea-ui.
type Mode string

const (
	// ModeOIDC is mode 1: the user authenticated against an OIDC provider that the
	// kube-apiserver also trusts, and the id_token is used as the K8s credential.
	ModeOIDC Mode = "oidc"
	// ModeKubeconfig is mode 3: the user uploaded a kubeconfig.
	ModeKubeconfig Mode = "kubeconfig"
	// ModeAdmin is mode 4: the user authenticated with the static admin password, and K8s
	// calls are impersonated as the antrea-ui-admin ServiceAccount.
	ModeAdmin Mode = "admin"
	// ModeToken is mode 5: the user pasted a bearer token, typically (but not necessarily) for a
	// ServiceAccount. This names the mechanism, not the account kind — see the Username field of
	// the resolved RequestAuth for what kind of account actually authenticated.
	ModeToken Mode = "token"
)

// Credential is the Kubernetes credential antrea-ui presents on behalf of the end user.
//
// Callers must treat Token, CertPEM and KeyPEM as secret: never log them, never include them in
// an error message, never write them anywhere but memory.
type Credential struct {
	Kind Kind
	// Token is the bearer token, for KindBearer.
	Token []byte
	// CertPEM and KeyPEM are the PEM-encoded client certificate and private key, for KindCert.
	CertPEM []byte
	KeyPEM  []byte
	// UserName is the identity to impersonate, for KindImpersonate.
	UserName string
	// ExpiresAt is when the credential itself stops being valid: the "exp" claim for a JWT
	// bearer token, the certificate's notAfter for KindCert. Zero means no known expiry,
	// which is the case for KindImpersonate and for opaque (non-JWT) tokens.
	ExpiresAt time.Time
}

// String makes Credential safe to interpolate with %v/%s: it never reveals credential material.
func (c Credential) String() string {
	return fmt.Sprintf("Credential{Kind:%s,UserName:%s,Redacted}", c.Kind, c.UserName)
}

// MarshalJSON makes Credential safe to log through a structured logger (logr/zap serializes
// unknown values by reflection, which would otherwise dump the raw bytes).
func (c Credential) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"kind":%q,"userName":%q,"redacted":true}`, c.Kind, c.UserName)), nil
}

// Zero overwrites the credential material in place and drops the references.
//
// This is best effort: the Go runtime may already have copied these bytes elsewhere (a growing
// slice, an escaped string conversion). It still bounds how long the material stays trivially
// recoverable from the process heap after logout or expiry.
//
// One known, permanent exception: a KindBearer credential's cached transport
// (pkg/k8s.ClientFactory.TransportFor) holds the token as a client-go bearerAuthRoundTripper,
// which stores it as an immutable Go string set once at construction. That copy cannot be
// scrubbed by this method, or by anything else — the field is private to client-go and strings
// cannot be mutated in place. Dropping the session's reference to that transport (see
// dropTransportsLocked) only makes the copy eligible for garbage collection; unlike every other
// field here, it is not actively erased the instant the session ends, and Go's GC gives no
// timing guarantee and does not zero reclaimed memory. Avoidable only by not using client-go's
// string-based bearer round-tripper helper, which was deliberately not done here to avoid
// drifting from client-go's own transport handling (see the cert-transport case below for the
// same reasoning cutting the other way).
func (c *Credential) Zero() {
	zeroBytes(c.Token)
	zeroBytes(c.CertPEM)
	zeroBytes(c.KeyPEM)
	c.Token = nil
	c.CertPEM = nil
	c.KeyPEM = nil
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// TransportBuilder builds an http.RoundTripper that authenticates as cred.
//
// cleanup, when not nil, is called once the transport is discarded (on credential refresh or
// session eviction); it is where a transport that owns its own connection pool closes it.
type TransportBuilder func(cred *Credential) (rt http.RoundTripper, cleanup func(), err error)

// Refresher renews an expiring credential. Only OIDC sessions have one.
type Refresher interface {
	// Refresh exchanges refreshToken for a fresh credential, and returns the refresh token to
	// use next time (identity providers with refresh-token rotation return a new one).
	// Implementations must not log or persist either token.
	//
	// The returned error must not contain credential material either: a failed refresh is
	// logged by the store (see Get), so anything the error carries is written to the log.
	// Identity providers do echo the refresh token back in their error responses, which is why
	// oidcRefresher.Refresh deliberately does not wrap what it gets from the provider.
	Refresh(ctx context.Context, refreshToken []byte) (cred Credential, newRefreshToken []byte, err error)
}

// Spec describes a session to create. It is consumed by Store.Create.
type Spec struct {
	Mode         Mode
	Credential   Credential
	RefreshToken []byte
	// Username is for display only (the /auth/session response). It is never used for
	// authorization: that is the API server's job, from the credential.
	Username string
	// Refresher renews Credential before it expires. Nil means the session simply ends when
	// the credential does.
	Refresher Refresher
}

// Zero wipes every piece of credential material the Spec carries.
//
// Call it on any path that abandons the login the Spec describes: a credential the API server
// refused, or a session the store would not accept, must not outlive the request that carried it.
// Zeroing the Credential alone is not enough - the refresh token is credential material too, and
// is what would let an attacker mint fresh id_tokens.
func (s *Spec) Zero() {
	s.Credential.Zero()
	zeroBytes(s.RefreshToken)
	s.RefreshToken = nil
}

type cachedTransport struct {
	rt      http.RoundTripper
	cleanup func()
}

// Session is one logged-in user. It is safe for concurrent use.
type Session struct {
	id   string
	mode Mode
	// createdAt and username are immutable after creation, so they need no locking.
	createdAt time.Time
	username  string
	// capKey groups sessions for the per-user cap; empty means the session is not subject to
	// it. It is deliberately not the username: see perUserCapKey.
	capKey    string
	refresher Refresher

	// mutex protects every field below.
	mutex        sync.RWMutex
	credential   Credential
	refreshToken []byte
	lastSeen     time.Time
	// expiresAt is min(createdAt+maxLifetime, credential expiry). It moves forward (up to the
	// absolute cap) when the credential is refreshed.
	expiresAt  time.Time
	transports map[string]cachedTransport

	// refreshMutex serializes credential refreshes for this session. The summary page fires
	// three API requests in one Promise.all; without this they would each start a refresh,
	// and with refresh-token rotation the later ones would invalidate the earlier ones.
	refreshMutex sync.Mutex
}

func (s *Session) ID() string           { return s.id }
func (s *Session) Mode() Mode           { return s.mode }
func (s *Session) Username() string     { return s.username }
func (s *Session) CreatedAt() time.Time { return s.createdAt }

func (s *Session) ExpiresAt() time.Time {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.expiresAt
}

func (s *Session) LastSeen() time.Time {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.lastSeen
}

// Credential returns the current credential. The returned struct shares its byte slices with the
// session, so callers must not modify (or retain) them: they are zeroed on eviction.
func (s *Session) Credential() Credential {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.credential
}

// String keeps a Session from leaking credential material if it is ever logged.
func (s *Session) String() string {
	return fmt.Sprintf("Session{ID:%s,Mode:%s,Redacted}", s.id, s.mode)
}

// MarshalJSON keeps a Session from leaking credential material through a structured logger.
func (s *Session) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"id":%q,"mode":%q,"redacted":true}`, s.id, s.mode)), nil
}

// transportFor returns the RoundTripper cached under key for this session's current credential,
// building it with build on first use. Caching matters most for KindCert, where each transport
// owns a connection pool and a TLS handshake.
//
// key is either a bare name for an upstream whose transports never go stale on their own, or
// "<upstream>/<version>" for one whose settings can change under a live session — the Antrea
// Service, whose transports are rebuilt whenever the Antrea CA bundle rotates. A new version
// supersedes the older ones under the same upstream, which are released rather than left to
// accumulate for the rest of the session.
func (s *Session) transportFor(key string, build TransportBuilder) (http.RoundTripper, error) {
	s.mutex.RLock()
	cached, ok := s.transports[key]
	s.mutex.RUnlock()
	if ok {
		return cached.rt, nil
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()
	// Another goroutine may have built it while we were upgrading the lock.
	if cached, ok := s.transports[key]; ok {
		return cached.rt, nil
	}
	s.dropSupersededTransportsLocked(key)
	rt, cleanup, err := build(&s.credential)
	if err != nil {
		return nil, err
	}
	if s.transports == nil {
		s.transports = make(map[string]cachedTransport)
	}
	s.transports[key] = cachedTransport{rt: rt, cleanup: cleanup}
	return rt, nil
}

// transportUpstream is the part of a transport cache key before the first "/". A key with no "/"
// names an upstream that never supersedes anything, and reports false.
func transportUpstream(key string) (string, bool) {
	upstream, _, versioned := strings.Cut(key, "/")
	return upstream, versioned
}

// dropSupersededTransportsLocked releases every cached transport for the same upstream as key but
// an older version of it. The caller must hold s.mutex.
//
// Without this, a session that outlives an Antrea CA rotation keeps the transport it built against
// the previous bundle for the rest of its life: unusable, since the CA it trusts is gone, and for
// a KindCert credential still holding an idle connection pool that only its cleanup closes.
func (s *Session) dropSupersededTransportsLocked(key string) {
	upstream, versioned := transportUpstream(key)
	if !versioned {
		return
	}
	for k, cached := range s.transports {
		if k == key {
			continue
		}
		if u, versioned := transportUpstream(k); !versioned || u != upstream {
			continue
		}
		if cached.cleanup != nil {
			cached.cleanup()
		}
		delete(s.transports, k)
	}
}

// dropTransportsLocked discards every cached transport. The caller must hold s.mutex.
func (s *Session) dropTransportsLocked() {
	for key, cached := range s.transports {
		if cached.cleanup != nil {
			cached.cleanup()
		}
		delete(s.transports, key)
	}
}

// needsRefresh reports whether the credential is close enough to expiry that it should be
// renewed before the request that triggered this check is served.
func (s *Session) needsRefresh(now time.Time, window time.Duration) bool {
	if s.refresher == nil {
		return false
	}
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.credential.ExpiresAt.IsZero() {
		return false
	}
	return !now.Add(window).Before(s.credential.ExpiresAt)
}

func (s *Session) refreshTokenCopy() []byte {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.refreshToken == nil {
		return nil
	}
	out := make([]byte, len(s.refreshToken))
	copy(out, s.refreshToken)
	return out
}

// zero wipes every piece of credential material held by the session and releases its transports.
func (s *Session) zero() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.credential.Zero()
	zeroBytes(s.refreshToken)
	s.refreshToken = nil
	s.dropTransportsLocked()
}
