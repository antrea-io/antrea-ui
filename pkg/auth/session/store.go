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
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"

	"antrea.io/antrea-ui/pkg/utils/random"
)

const (
	// DefaultIdleTimeout is how long a session survives without any request.
	DefaultIdleTimeout = 30 * time.Minute
	// DefaultMaxLifetime is the absolute cap on a session's lifetime, regardless of activity.
	DefaultMaxLifetime = 12 * time.Hour
	// DefaultMaxSessions bounds the size of the store. Sessions are small, but every successful
	// login creates a new one regardless of identity — there is no cap tying attempts to a
	// credential — so the map still needs a ceiling against repeated successful logins.
	DefaultMaxSessions = 1000
	// DefaultMaxSessionsPerUser bounds how much of the store one identity can occupy, so that a
	// single user cannot fill it and lock everyone else out. It is generous compared to what a
	// person does (one session per browser they log in from), because the login that trips it
	// evicts that user's own oldest session rather than failing.
	DefaultMaxSessionsPerUser = 10

	// sessionIDBytes is the entropy of a session ID, which is the only thing standing between
	// an attacker and a live Kubernetes credential.
	sessionIDBytes = 32
	// refreshWindow is how far ahead of credential expiry a refresh is triggered.
	refreshWindow = 60 * time.Second
	// refreshTimeout bounds a credential refresh. It is deliberately shorter than any sensible
	// client timeout, so that a slow identity provider surfaces as a failed refresh rather than
	// as a request that hangs until the browser gives up.
	refreshTimeout = 20 * time.Second
	// gcPeriod is how often expired sessions are swept.
	gcPeriod = 1 * time.Minute
	// gcBatchSize bounds how many sessions are deleted while holding the store lock.
	gcBatchSize = 100
)

var (
	// ErrNotFound means there is no session with that ID (it never existed, or it was
	// deleted, or the backend restarted).
	ErrNotFound = errors.New("session not found")
	// ErrExpired means the session existed but is past its idle timeout or its absolute cap.
	ErrExpired = errors.New("session expired")
	// ErrTooManySessions means the store is at capacity.
	ErrTooManySessions = errors.New("too many active sessions")
)

// Store keeps live sessions. The only implementation is in-memory and deliberately so: session
// credentials are never persisted to a Secret, a ConfigMap, or a volume. A backend restart logs
// everyone out.
type Store interface {
	// Create registers a new session and returns it, with its ID assigned.
	Create(spec *Spec) (*Session, error)
	// Get looks up a session, refreshes its credential if it is about to expire, and bumps
	// its last-seen time. It returns ErrNotFound or ErrExpired if the session is not usable.
	//
	// This is the only way to keep a session alive, deliberately: anything that extends a
	// session must also renew the credential behind it, or the session outlives its own
	// credential and a revocation at the identity provider stops being noticed.
	Get(ctx context.Context, id string) (*Session, error)
	// Peek looks up a session without checking liveness, refreshing its credential, or bumping
	// its last-seen time. For callers that are about to Delete the session anyway and just want
	// whatever credential it currently holds (e.g. an id_token to use as an OIDC logout hint,
	// which does not need to be currently valid).
	Peek(id string) (*Session, bool)
	// Delete evicts a session and zeroes its credential material.
	Delete(id string)
	// Run sweeps expired sessions until stopCh is closed.
	Run(stopCh <-chan struct{})
}

// Options configures a Store. Zero values fall back to the Default* constants.
type Options struct {
	IdleTimeout time.Duration
	MaxLifetime time.Duration
	MaxSessions int
	// MaxSessionsPerUser bounds how many concurrent sessions one identity may hold. See
	// DefaultMaxSessionsPerUser.
	MaxSessionsPerUser int
	// Clock is injectable for tests.
	Clock clock.Clock
}

func (o *Options) setDefaults() {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.MaxLifetime <= 0 {
		o.MaxLifetime = DefaultMaxLifetime
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = DefaultMaxSessions
	}
	if o.MaxSessionsPerUser <= 0 {
		o.MaxSessionsPerUser = DefaultMaxSessionsPerUser
	}
	if o.MaxSessionsPerUser > o.MaxSessions {
		o.MaxSessionsPerUser = o.MaxSessions
	}
	if o.Clock == nil {
		o.Clock = clock.RealClock{}
	}
}

type store struct {
	logger   logr.Logger
	opts     Options
	mutex    sync.RWMutex
	sessions map[string]*Session
}

// NewStore creates an in-memory session store.
func NewStore(logger logr.Logger, opts Options) Store {
	opts.setDefaults()
	return &store{
		logger:   logger,
		opts:     opts,
		sessions: make(map[string]*Session),
	}
}

// effectiveExpiry is min(absolute cap, credential expiry). A zero credential expiry (an
// impersonation credential, or an opaque token with no "exp") means the cap alone applies.
func effectiveExpiry(createdAt time.Time, maxLifetime time.Duration, credExpiry time.Time) time.Time {
	absolute := createdAt.Add(maxLifetime)
	if credExpiry.IsZero() || credExpiry.After(absolute) {
		return absolute
	}
	return credExpiry
}

func (st *store) Create(spec *Spec) (*Session, error) {
	id, err := random.HexString(sessionIDBytes)
	if err != nil {
		return nil, err
	}
	now := st.opts.Clock.Now()
	s := &Session{
		id:           id,
		mode:         spec.Mode,
		username:     spec.Username,
		capKey:       perUserCapKey(spec),
		refresher:    spec.Refresher,
		createdAt:    now,
		credential:   spec.Credential,
		refreshToken: spec.RefreshToken,
		lastSeen:     now,
		expiresAt:    effectiveExpiry(now, st.opts.MaxLifetime, spec.Credential.ExpiresAt),
	}

	st.mutex.Lock()
	defer st.mutex.Unlock()
	// Enforce the per-user cap first: it is what keeps one identity from filling the store and
	// denying logins to everyone else, so it has to run whether or not the store is full.
	st.enforcePerUserLimitLocked(s.capKey)
	if len(st.sessions) >= st.opts.MaxSessions && !st.evictOneExpiredLocked(now) {
		// Do not zero spec.Credential here: the caller owns it and may want to retry or
		// report. It is zeroed by the caller's own error path.
		return nil, ErrTooManySessions
	}
	st.sessions[id] = s
	return s, nil
}

// perUserCapKey is the identity the per-user session cap groups a new session under. An empty key
// means the session is not subject to the cap at all, and falls through to the global one.
//
// ModeAdmin is exempt. Every static-admin-password login authenticates as the same literal
// "admin", so grouping on it would give everyone using that password a single shared budget: with
// the default cap of 10, the 11th browser to log in would silently sign out whoever logged in
// first. There is no per-user identity there to bound, and nothing to bound it against — the cap
// exists to stop one identity from starving the others, and a mode where every session is the same
// identity has no others to starve. Nor does it weaken anything: reaching mode 4 at all requires
// the admin password, and the global cap plus the login rate limiter still bound the store.
//
// It is also a separate field from Session.username rather than the username itself, so that a
// Kubernetes identity that genuinely resolves to "admin" cannot collide with mode 4's sessions and
// evict them.
func perUserCapKey(spec *Spec) string {
	if spec.Mode == ModeAdmin {
		return ""
	}
	return spec.Username
}

// enforcePerUserLimitLocked drops the least-recently-seen sessions grouped under capKey until they
// have room for one more. The caller must hold st.mutex for writing.
//
// Without a per-user cap, MaxSessions is a shared resource with nothing tying consumption to an
// identity: one caller scripting logins fills the store, and every other user is refused a session
// until the attacker's sessions idle out. Evicting the offender's own oldest session instead of
// refusing the login keeps the pressure on the identity causing it — a real user with more browsers
// than the cap just loses their stalest session, which is the same thing every "you have been
// signed out of your oldest device" scheme does.
//
// An empty capKey means the session has no identity to bound (see perUserCapKey). Those sessions
// are not grouped together — that would let one of them evict an unrelated one — and fall through
// to the global cap alone.
func (st *store) enforcePerUserLimitLocked(capKey string) {
	if capKey == "" {
		return
	}
	// The store holds at most MaxSessions entries and logins are rate-limited, so an O(n) scan
	// here is cheaper than maintaining a by-identity index that Delete and doGC would also have
	// to keep in step.
	owned := make([]*Session, 0, st.opts.MaxSessionsPerUser+1)
	for _, s := range st.sessions {
		if s.capKey == capKey {
			owned = append(owned, s)
		}
	}
	if len(owned) < st.opts.MaxSessionsPerUser {
		return
	}
	// Oldest last-seen first, so the sessions dropped are the ones the user is least likely to
	// still have a tab open on. Creation time breaks ties, which matters when several logins
	// land inside one clock tick and none of them has been used since.
	sort.Slice(owned, func(i, j int) bool {
		li, lj := owned[i].LastSeen(), owned[j].LastSeen()
		if !li.Equal(lj) {
			return li.Before(lj)
		}
		return owned[i].createdAt.Before(owned[j].createdAt)
	})
	evict := owned[:len(owned)-st.opts.MaxSessionsPerUser+1]
	for _, s := range evict {
		delete(st.sessions, s.id)
		// s.zero() takes the session's own lock, never the store's, so this is safe to do
		// while holding st.mutex.
		s.zero()
	}
	st.logger.V(2).Info("Evicted oldest sessions to stay within the per-user limit",
		"count", len(evict), "limit", st.opts.MaxSessionsPerUser)
}

// evictOneExpiredLocked makes room for one new session by dropping an expired one that the sweep
// has not reached yet, and reports whether it found one. Without it, a burst of sessions that all
// expire together would keep the store nominally full — and refuse logins — for up to gcPeriod
// after they stopped being usable. The caller must hold st.mutex for writing.
//
// One is enough: this runs on every Create that finds the store full, so a backlog drains at the
// rate logins arrive, and doGC still sweeps the rest on its own schedule.
func (st *store) evictOneExpiredLocked(now time.Time) bool {
	for id, s := range st.sessions {
		if st.alive(s, now) {
			continue
		}
		delete(st.sessions, id)
		// s.zero() takes the session's own lock, never the store's, so this is safe to do
		// while holding st.mutex.
		s.zero()
		return true
	}
	return false
}

func (st *store) lookup(id string) (*Session, bool) {
	st.mutex.RLock()
	defer st.mutex.RUnlock()
	s, ok := st.sessions[id]
	return s, ok
}

func (st *store) Peek(id string) (*Session, bool) {
	return st.lookup(id)
}

// alive reports whether s is still within its idle timeout, its absolute lifetime cap, and — for
// a credential that nothing can renew — its credential's own expiry.
//
// A *refreshable* credential having expired is deliberately not terminal here: killing the
// session on it would mean maybeRefresh never gets to run (see Get, which calls this first), so
// a session that missed its refresh window could never be saved. Get catches the case where the
// refresh could not save it, via credentialExpired.
//
// A non-refreshable one is terminal, and has to be caught here rather than in Get alone: alive is
// what doGC sweeps on, and credential material should be zeroed when the credential dies, not one
// idle timeout later.
func (st *store) alive(s *Session, now time.Time) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if now.After(s.createdAt.Add(st.opts.MaxLifetime)) {
		return false
	}
	// s.refresher is set once, at Create, and never mutated.
	if s.refresher == nil && !s.credential.ExpiresAt.IsZero() && now.After(s.credential.ExpiresAt) {
		return false
	}
	return now.Sub(s.lastSeen) <= st.opts.IdleTimeout
}

// credentialExpired reports whether the session's current credential is past its own expiry.
// Called after maybeRefresh has had its chance, so this only fires for a refreshable credential
// that refresh could not save: one whose provider handed back a credential that is already
// expired, or that was never renewed because it expired outside the refresh window's reach. A
// non-refreshable credential never reaches here — alive already rejected it.
func credentialExpired(s *Session, now time.Time) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return !s.credential.ExpiresAt.IsZero() && now.After(s.credential.ExpiresAt)
}

func (st *store) Get(ctx context.Context, id string) (*Session, error) {
	s, ok := st.lookup(id)
	if !ok {
		return nil, ErrNotFound
	}
	now := st.opts.Clock.Now()
	if !st.alive(s, now) {
		st.Delete(id)
		return nil, ErrExpired
	}
	if err := st.maybeRefresh(ctx, s, now); err != nil {
		// A failed refresh is deliberately not terminal on its own. The refresh fires
		// refreshWindow (60s) *before* the credential expires, so at this point the session
		// still holds a credential that works: a revoked refresh token and a momentarily
		// unreachable identity provider are indistinguishable here (see oidcRefresher.Refresh,
		// which cannot tell them apart either), and destroying the session over the latter
		// logs the user out for a network blip. Falling through leaves the window as a retry
		// budget - the flow stream re-enters this path every few seconds - and
		// credentialExpired below is what fails closed once the credential really is gone. A
		// revoked refresh token therefore still ends the session, within refreshWindow.
		st.logger.V(2).Info("Failed to refresh session credential, will retry until it expires",
			"err", err.Error())
	}
	if credentialExpired(s, st.opts.Clock.Now()) {
		st.Delete(id)
		return nil, ErrExpired
	}
	s.mutex.Lock()
	s.lastSeen = st.opts.Clock.Now()
	s.mutex.Unlock()
	return s, nil
}

func (st *store) Delete(id string) {
	st.mutex.Lock()
	s, ok := st.sessions[id]
	delete(st.sessions, id)
	st.mutex.Unlock()
	if ok {
		s.zero()
	}
}

// maybeRefresh renews the credential if it is within refreshWindow of expiring. It is
// single-flight per session: concurrent requests for the same session wait for one refresh
// rather than each starting their own (which, with refresh-token rotation, would invalidate
// each other).
func (st *store) maybeRefresh(ctx context.Context, s *Session, now time.Time) error {
	if !s.needsRefresh(now, refreshWindow) {
		return nil
	}
	s.refreshMutex.Lock()
	defer s.refreshMutex.Unlock()
	// Re-check: another goroutine may have refreshed while we waited for the lock.
	if !s.needsRefresh(st.opts.Clock.Now(), refreshWindow) {
		return nil
	}
	refreshToken := s.refreshTokenCopy()
	defer zeroBytes(refreshToken)
	// Detach from the request context. The refresh belongs to the session, not to whichever
	// request happened to arrive inside the refresh window: if the user navigates away or hits
	// Escape mid-refresh, the cancellation would surface here as a refresh failure and Get would
	// destroy a perfectly good session. Worse, with refresh-token rotation the provider may
	// already have burned the old token by then, so the cancelled attempt is not even a no-op.
	// Cancellation control is replaced by an explicit timeout.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	cred, newRefreshToken, err := s.refresher.Refresh(refreshCtx, refreshToken)
	if err != nil {
		return err
	}
	st.applyRefresh(s, cred, newRefreshToken)
	return nil
}

func (st *store) applyRefresh(s *Session, cred Credential, newRefreshToken []byte) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.credential.Zero()
	s.credential = cred
	if newRefreshToken != nil {
		zeroBytes(s.refreshToken)
		s.refreshToken = newRefreshToken
	}
	// The cached transports embed the old token, so they are no longer usable.
	s.dropTransportsLocked()
	s.expiresAt = effectiveExpiry(s.createdAt, st.opts.MaxLifetime, cred.ExpiresAt)
}

func (st *store) doGC() {
	now := st.opts.Clock.Now()
	expired := func() []string {
		ids := make([]string, 0)
		st.mutex.RLock()
		defer st.mutex.RUnlock()
		for id, s := range st.sessions {
			if !st.alive(s, now) {
				ids = append(ids, id)
			}
		}
		return ids
	}()

	// Delete in batches, so a large sweep does not hold the store lock for the whole run.
	idx := 0
	for idx < len(expired) {
		evicted := make([]*Session, 0, gcBatchSize)
		func() {
			st.mutex.Lock()
			defer st.mutex.Unlock()
			for k := 0; k < gcBatchSize && idx < len(expired); k++ {
				if s, ok := st.sessions[expired[idx]]; ok {
					evicted = append(evicted, s)
					delete(st.sessions, expired[idx])
				}
				idx++
			}
		}()
		// Zeroing touches each session's own lock, so do it outside the store lock.
		for _, s := range evicted {
			s.zero()
		}
	}
	if len(expired) > 0 {
		st.logger.V(2).Info("Evicted expired sessions", "count", len(expired))
	}
}

func (st *store) Run(stopCh <-chan struct{}) {
	//lint:ignore SA1019 apimachinery doesn't provide a correct alternative yet
	wait.BackoffUntil(st.doGC, wait.NewJitteredBackoffManager(gcPeriod, 0.0, st.opts.Clock), true, stopCh)
}
