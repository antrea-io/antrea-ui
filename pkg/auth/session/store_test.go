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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	testingclock "k8s.io/utils/clock/testing"
)

var testStartTime = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T, mutators ...func(*Options)) (Store, *testingclock.FakeClock) {
	fakeClock := testingclock.NewFakeClock(testStartTime)
	opts := Options{
		IdleTimeout: 30 * time.Minute,
		MaxLifetime: 12 * time.Hour,
		MaxSessions: 10,
		Clock:       fakeClock,
	}
	for _, m := range mutators {
		m(&opts)
	}
	return NewStore(testr.New(t), opts), fakeClock
}

func bearerSpec(token string) *Spec {
	return bearerSpecForUser("system:serviceaccount:default:tester", token)
}

func bearerSpecForUser(username, token string) *Spec {
	return &Spec{
		Mode:       ModeSAToken,
		Username:   username,
		Credential: Credential{Kind: KindBearer, Token: []byte(token)},
	}
}

// distinctUserSpec builds a spec for a user nobody else in the test shares, for the cases that are
// about the global cap and must not trip the per-user one.
func distinctUserSpec(i int) *Spec {
	return bearerSpecForUser(fmt.Sprintf("user-%d", i), fmt.Sprintf("tok-%d", i))
}

func TestCreateAndGet(t *testing.T) {
	st, _ := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)
	// The session ID is the only bearer of the session's authority, so it must carry the
	// full 32 bytes of entropy (64 hex characters).
	assert.Len(t, s.ID(), 2*sessionIDBytes)

	got, err := st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Same(t, s, got)
	assert.Equal(t, ModeSAToken, got.Mode())
	assert.Equal(t, "system:serviceaccount:default:tester", got.Username())
}

func TestGetUnknownSession(t *testing.T) {
	st, _ := newTestStore(t)
	_, err := st.Get(t.Context(), "does-not-exist")
	assert.ErrorIs(t, err, ErrNotFound)
}

// Peek must not care that the session is past every expiry that would make Get reject it — it's
// for callers (like Logout) that are deleting the session anyway and just want its credential.
func TestPeekIgnoresExpiry(t *testing.T) {
	st, fakeClock := newTestStore(t)
	spec := bearerSpec("tok")
	spec.Credential.ExpiresAt = testStartTime.Add(1 * time.Hour)
	s, err := st.Create(spec)
	require.NoError(t, err)

	fakeClock.Step(2 * time.Hour)
	got, ok := st.Peek(s.ID())
	require.True(t, ok)
	assert.Same(t, s, got)

	_, ok = st.Peek("does-not-exist")
	assert.False(t, ok)
}

func TestIdleTimeout(t *testing.T) {
	st, fakeClock := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	// Just inside the idle window: still alive, and the access resets the window.
	fakeClock.Step(29 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)

	fakeClock.Step(29 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)

	fakeClock.Step(31 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrExpired)

	// An expired session is evicted, not just reported as expired.
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestMaxLifetime(t *testing.T) {
	st, fakeClock := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	// Stay active throughout, so only the absolute cap can end the session.
	for i := 0; i < 47; i++ {
		fakeClock.Step(15 * time.Minute)
		_, err := st.Get(t.Context(), s.ID())
		require.NoErrorf(t, err, "session should still be alive after %d minutes", 15*(i+1))
	}
	// 11h45m in and still active, so this can only be the absolute cap firing, not the idle
	// timeout.
	fakeClock.Step(16 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrExpired)
}

func TestExpiryIsMinOfCapAndCredential(t *testing.T) {
	st, _ := newTestStore(t)

	t.Run("credential expires first", func(t *testing.T) {
		spec := bearerSpec("tok")
		spec.Credential.ExpiresAt = testStartTime.Add(1 * time.Hour)
		s, err := st.Create(spec)
		require.NoError(t, err)
		assert.Equal(t, testStartTime.Add(1*time.Hour), s.ExpiresAt())
	})

	t.Run("cap is reached first", func(t *testing.T) {
		spec := bearerSpec("tok")
		spec.Credential.ExpiresAt = testStartTime.Add(48 * time.Hour)
		s, err := st.Create(spec)
		require.NoError(t, err)
		assert.Equal(t, testStartTime.Add(12*time.Hour), s.ExpiresAt())
	})

	t.Run("credential never expires", func(t *testing.T) {
		s, err := st.Create(&Spec{
			Mode:       ModeAdmin,
			Credential: Credential{Kind: KindImpersonate, UserName: "system:serviceaccount:kube-system:antrea-ui-admin"},
		})
		require.NoError(t, err)
		assert.Equal(t, testStartTime.Add(12*time.Hour), s.ExpiresAt())
	})
}

// A bearer credential has no refresher, so it must die exactly at its own expiry, well before
// the absolute cap — nothing can renew it.
func TestNonRefreshableCredentialExpiresOnItsOwn(t *testing.T) {
	// A generous idle timeout isolates credential expiry as the thing under test.
	st, fakeClock := newTestStore(t, func(o *Options) { o.IdleTimeout = 24 * time.Hour })
	spec := bearerSpec("tok")
	spec.Credential.ExpiresAt = testStartTime.Add(1 * time.Hour)
	s, err := st.Create(spec)
	require.NoError(t, err)

	fakeClock.Step(59 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)

	fakeClock.Step(2 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrExpired)
}

// A session must not outlive a credential that nothing can renew, even while it is being actively
// used - a long-running SSE stream keeps calling Get, and must not be able to keep streaming with a
// credential that has expired.
func TestGetRejectsExpiredNonRefreshableCredential(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) { o.IdleTimeout = 24 * time.Hour })
	spec := bearerSpec("tok")
	spec.Credential.ExpiresAt = testStartTime.Add(1 * time.Hour)
	s, err := st.Create(spec)
	require.NoError(t, err)

	fakeClock.Step(59 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)

	fakeClock.Step(2 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrExpired)
}

// A refreshable credential is the opposite case: it must not be terminal on expiry alone, or a
// session that missed its refresh window could never be saved. Get renews it instead.
func TestGetRenewsRefreshableCredentialPastExpiry(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Minute)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)

	// Well past the credential's expiry, and so past the refresh window too.
	fakeClock.Step(12 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
}

// Credential material should stop being reachable when the credential dies, not one idle timeout
// later, so the sweep has to evict on credential expiry too.
func TestGCSweepsExpiredNonRefreshableCredential(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) { o.IdleTimeout = 24 * time.Hour })
	token := []byte("super-secret-token")
	s, err := st.Create(&Spec{
		Mode:       ModeSAToken,
		Credential: Credential{Kind: KindBearer, Token: token, ExpiresAt: testStartTime.Add(1 * time.Hour)},
	})
	require.NoError(t, err)

	fakeClock.Step(2 * time.Hour)
	st.(*store).doGC()

	_, ok := st.Peek(s.ID())
	assert.False(t, ok)
	assert.Equal(t, make([]byte, len(token)), token)
}

// The global cap only comes into play once the sessions belong to different users: one user's
// share of the store is bounded well below it by the per-user cap.
func TestMaxSessions(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) { o.MaxSessions = 3 })
	for i := 0; i < 3; i++ {
		_, err := st.Create(distinctUserSpec(i))
		require.NoError(t, err)
	}
	_, err := st.Create(bearerSpecForUser("someone-else", "one-too-many"))
	assert.ErrorIs(t, err, ErrTooManySessions)
}

// Without a per-user cap, MaxSessions is a shared resource with nothing tying consumption to an
// identity: one caller scripting logins fills the store and everyone else is refused a session
// until those sessions idle out.
func TestMaxSessionsPerUser(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) {
		o.MaxSessions = 10
		o.MaxSessionsPerUser = 3
	})
	concrete := st.(*store)

	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		s, err := st.Create(bearerSpecForUser("greedy", fmt.Sprintf("tok-%d", i)))
		// The login past the cap succeeds; it is that user's own oldest session that goes.
		require.NoError(t, err)
		ids = append(ids, s.ID())
		fakeClock.Step(1 * time.Minute)
	}

	concrete.mutex.RLock()
	total := len(concrete.sessions)
	concrete.mutex.RUnlock()
	assert.Equal(t, 3, total, "one identity must not hold more than the per-user cap")

	// The two oldest are gone, the three newest remain.
	for _, id := range ids[:2] {
		_, ok := st.Peek(id)
		assert.False(t, ok, "the oldest sessions should have been evicted")
	}
	for _, id := range ids[2:] {
		_, ok := st.Peek(id)
		assert.True(t, ok, "the newest sessions should have been kept")
	}

	// And another user is unaffected: the store still has room for them.
	_, err := st.Create(bearerSpecForUser("bystander", "tok"))
	assert.NoError(t, err)
}

// Eviction is by last-seen, not by creation time: the session the user is still actively using
// must survive, even if it is the one they opened first.
func TestPerUserLimitEvictsLeastRecentlySeen(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) { o.MaxSessionsPerUser = 2 })

	first, err := st.Create(bearerSpecForUser("alice", "tok-1"))
	require.NoError(t, err)
	fakeClock.Step(1 * time.Minute)
	second, err := st.Create(bearerSpecForUser("alice", "tok-2"))
	require.NoError(t, err)

	// Alice keeps using the session she opened first, and leaves the second one idle.
	fakeClock.Step(1 * time.Minute)
	_, err = st.Get(t.Context(), first.ID())
	require.NoError(t, err)

	third, err := st.Create(bearerSpecForUser("alice", "tok-3"))
	require.NoError(t, err)

	_, ok := st.Peek(second.ID())
	assert.False(t, ok, "the least-recently-seen session should have been evicted")
	_, ok = st.Peek(first.ID())
	assert.True(t, ok, "the actively-used session should have survived")
	_, ok = st.Peek(third.ID())
	assert.True(t, ok)
}

// A session evicted by the per-user cap must have its credential material zeroed, exactly like one
// that expired: the whole point of the in-memory store is that a credential does not outlive the
// session holding it.
func TestPerUserLimitZeroesEvictedCredential(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) { o.MaxSessionsPerUser = 1 })

	token := []byte("secret-token")
	spec := bearerSpecForUser("alice", "")
	spec.Credential.Token = token
	_, err := st.Create(spec)
	require.NoError(t, err)

	_, err = st.Create(bearerSpecForUser("alice", "replacement"))
	require.NoError(t, err)

	assert.Equal(t, make([]byte, len(token)), token, "the evicted session's token should be zeroed")
}

// Every static-admin-password login authenticates as the same literal "admin", so applying the
// per-user cap to them would give everyone sharing that password a single budget between them: with
// the default cap of 10, the 11th browser to log in would silently sign out whoever logged in
// first. Mode 4 is exempt.
func TestPerUserLimitExemptsAdminPassword(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) {
		o.MaxSessions = 10
		o.MaxSessionsPerUser = 2
	})
	ids := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		s, err := st.Create(&Spec{
			Mode:       ModeAdmin,
			Username:   "admin",
			Credential: Credential{Kind: KindImpersonate, UserName: "system:serviceaccount:default:antrea-ui-admin"},
		})
		require.NoError(t, err)
		ids = append(ids, s.ID())
	}
	for i, id := range ids {
		_, ok := st.Peek(id)
		assert.True(t, ok, "admin-password session %d should not have been evicted by the per-user cap", i)
	}
}

// The per-user cap groups on a key that is not the username, so a Kubernetes identity that happens
// to resolve to "admin" cannot collide with mode 4's sessions and evict them.
func TestPerUserLimitDoesNotConflateAdminModeWithAnAdminUsername(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) {
		o.MaxSessions = 10
		o.MaxSessionsPerUser = 1
	})
	adminSession, err := st.Create(&Spec{
		Mode:       ModeAdmin,
		Username:   "admin",
		Credential: Credential{Kind: KindImpersonate, UserName: "system:serviceaccount:default:antrea-ui-admin"},
	})
	require.NoError(t, err)

	// A token login whose SelfSubjectReview resolved to the username "admin".
	_, err = st.Create(bearerSpecForUser("admin", "tok"))
	require.NoError(t, err)

	_, ok := st.Peek(adminSession.ID())
	assert.True(t, ok, "the admin-password session should not be evicted by an unrelated identity named \"admin\"")
}

// An empty username means the caller could not resolve an identity. Those must not be grouped
// together and evict each other; they fall through to the global cap alone.
func TestPerUserLimitIgnoresEmptyUsername(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) {
		o.MaxSessions = 10
		o.MaxSessionsPerUser = 2
	})
	for i := 0; i < 5; i++ {
		_, err := st.Create(bearerSpecForUser("", fmt.Sprintf("tok-%d", i)))
		require.NoError(t, err)
	}
	concrete := st.(*store)
	concrete.mutex.RLock()
	defer concrete.mutex.RUnlock()
	assert.Len(t, concrete.sessions, 5)
}

// The per-user cap can never exceed the global one, however the two are configured.
func TestPerUserLimitClampedToMaxSessions(t *testing.T) {
	st, _ := newTestStore(t, func(o *Options) {
		o.MaxSessions = 2
		o.MaxSessionsPerUser = 100
	})
	assert.Equal(t, 2, st.(*store).opts.MaxSessionsPerUser)
}

// The sweep only runs once a minute, so sessions that have expired but not yet been swept must not
// hold the store at capacity: a burst of logins that all idle out together would otherwise lock
// everyone out for up to gcPeriod after those sessions stopped being usable.
func TestMaxSessionsReclaimsExpiredBeforeGC(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) {
		o.MaxSessions = 3
		o.IdleTimeout = 30 * time.Minute
	})
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		s, err := st.Create(distinctUserSpec(i))
		require.NoError(t, err)
		ids = append(ids, s.ID())
	}
	_, err := st.Create(bearerSpecForUser("someone-else", "one-too-many"))
	require.ErrorIs(t, err, ErrTooManySessions)

	// Everyone idles out. No sweep has run.
	fakeClock.Step(31 * time.Minute)

	s, err := st.Create(bearerSpec("after-expiry"))
	require.NoError(t, err)
	assert.NotEmpty(t, s.ID())

	// The reclaimed session's credential was zeroed, not merely dropped from the map.
	evicted := 0
	for _, id := range ids {
		if _, ok := st.Peek(id); !ok {
			evicted++
		}
	}
	assert.Equal(t, 1, evicted, "exactly one expired session should have been reclaimed")
}

func TestDeleteZeroesCredential(t *testing.T) {
	st, _ := newTestStore(t)
	token := []byte("super-secret-token")
	certPEM := []byte("-----BEGIN CERTIFICATE-----")
	keyPEM := []byte("-----BEGIN RSA PRIVATE KEY-----")
	refreshToken := []byte("refresh-me")
	s, err := st.Create(&Spec{
		Mode:         ModeKubeconfig,
		Credential:   Credential{Kind: KindCert, Token: token, CertPEM: certPEM, KeyPEM: keyPEM},
		RefreshToken: refreshToken,
	})
	require.NoError(t, err)

	st.Delete(s.ID())

	// The store must overwrite the material, not merely drop its reference to it.
	assert.Equal(t, make([]byte, len(token)), token)
	assert.Equal(t, make([]byte, len(certPEM)), certPEM)
	assert.Equal(t, make([]byte, len(keyPEM)), keyPEM)
	assert.Equal(t, make([]byte, len(refreshToken)), refreshToken)

	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestDeleteClosesCachedTransports(t *testing.T) {
	st, _ := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	var closed atomic.Bool
	build := func(cred *Credential) (http.RoundTripper, func(), error) {
		return http.DefaultTransport, func() { closed.Store(true) }, nil
	}
	rt, err := s.transportFor(TransportKeyK8s, build)
	require.NoError(t, err)
	assert.NotNil(t, rt)

	st.Delete(s.ID())
	assert.True(t, closed.Load(), "cached transport should be cleaned up on eviction")
}

func TestTransportIsBuiltOncePerSession(t *testing.T) {
	st, _ := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	var builds atomic.Int32
	build := func(cred *Credential) (http.RoundTripper, func(), error) {
		builds.Add(1)
		return http.DefaultTransport, nil, nil
	}
	for i := 0; i < 5; i++ {
		_, err := s.transportFor(TransportKeyK8s, build)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), builds.Load())
}

// A versioned transport key ("<upstream>/<version>") supersedes the older versions of the same
// upstream. The Antrea Service rebuilds its factory, and its key, whenever the Antrea CA bundle
// rotates; without this the transport built against the previous bundle would sit in the session
// for the rest of its life, unusable, and for a KindCert credential still holding a connection pool
// that only its cleanup closes.
func TestVersionedTransportKeySupersedesOlderVersions(t *testing.T) {
	st, _ := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	var closed atomic.Int32
	build := func(cred *Credential) (http.RoundTripper, func(), error) {
		return http.DefaultTransport, func() { closed.Add(1) }, nil
	}
	_, err = s.transportFor("antreasvc/1", build)
	require.NoError(t, err)
	// An unrelated upstream must not be touched by the rotation.
	_, err = s.transportFor(TransportKeyK8s, build)
	require.NoError(t, err)

	_, err = s.transportFor("antreasvc/2", build)
	require.NoError(t, err)
	assert.Equal(t, int32(1), closed.Load(), "the superseded transport should have been cleaned up")

	s.mutex.RLock()
	defer s.mutex.RUnlock()
	assert.NotContains(t, s.transports, "antreasvc/1")
	assert.Contains(t, s.transports, "antreasvc/2")
	assert.Contains(t, s.transports, TransportKeyK8s, "an unversioned key supersedes nothing and is not superseded")
}

// An attached flow stream calls Get every few seconds, which is what lets it outlive the idle
// timeout - including while its browser tab is in the background, the one place antrea-ui does not
// require a visible tab. The absolute cap is what still bounds it.
func TestGetKeepsAnActiveSessionAliveUpToTheAbsoluteCap(t *testing.T) {
	st, fakeClock := newTestStore(t)
	s, err := st.Create(bearerSpec("tok"))
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		fakeClock.Step(20 * time.Minute)
		_, err = st.Get(t.Context(), s.ID())
		require.NoError(t, err, "an actively-used session should not idle out")
	}

	fakeClock.Step(10 * time.Hour)
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrExpired)

	_, err = st.Get(t.Context(), "no-such-session")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestGC(t *testing.T) {
	st, fakeClock := newTestStore(t, func(o *Options) { o.MaxSessions = 500 })
	concrete := st.(*store)

	ids := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		s, err := st.Create(distinctUserSpec(i))
		require.NoError(t, err)
		ids = append(ids, s.ID())
	}
	// Keep one session alive across the sweep; the other 199 go idle.
	fakeClock.Step(20 * time.Minute)
	_, err := st.Get(t.Context(), ids[0])
	require.NoError(t, err)
	fakeClock.Step(20 * time.Minute)

	concrete.doGC()

	concrete.mutex.RLock()
	remaining := len(concrete.sessions)
	concrete.mutex.RUnlock()
	assert.Equal(t, 1, remaining)
	_, err = st.Get(t.Context(), ids[0])
	assert.NoError(t, err)
}

type fakeRefresher struct {
	calls     atomic.Int32
	nextToken atomic.Int32
	expiresIn time.Duration
	err       error
	delay     chan struct{}
	// honorCtx makes the refresher fail if its context is already done, the way a real HTTP
	// call to the identity provider would.
	honorCtx bool
}

func (f *fakeRefresher) Refresh(ctx context.Context, refreshToken []byte) (Credential, []byte, error) {
	f.calls.Add(1)
	if f.delay != nil {
		<-f.delay
	}
	if f.honorCtx && ctx.Err() != nil {
		return Credential{}, nil, ctx.Err()
	}
	if f.err != nil {
		return Credential{}, nil, f.err
	}
	n := f.nextToken.Add(1)
	return Credential{
		Kind:      KindBearer,
		Token:     []byte(fmt.Sprintf("id-token-%d", n)),
		ExpiresAt: testStartTime.Add(f.expiresIn),
	}, []byte(fmt.Sprintf("refresh-token-%d", n)), nil
}

func TestRefreshOnGet(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Minute)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)
	assert.Equal(t, testStartTime.Add(10*time.Minute), s.ExpiresAt())

	// Well before expiry: no refresh.
	fakeClock.Step(5 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Equal(t, int32(0), refresher.calls.Load())

	// Inside the refresh window: the credential is renewed and the session expiry moves out.
	fakeClock.Step(4*time.Minute + 30*time.Second)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Equal(t, int32(1), refresher.calls.Load())
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
	assert.Equal(t, testStartTime.Add(2*time.Hour), s.ExpiresAt())
}

// If no request lands inside the refresh window (a short id_token lifetime plus a sparse
// keepalive interval can both conspire to skip it), the credential is discovered already
// expired. It must still be refreshed rather than treated as a dead session.
func TestRefreshAfterMissedWindow(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Minute)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)

	// Past expiry, not just inside the 60s window.
	fakeClock.Step(12 * time.Minute)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Equal(t, int32(1), refresher.calls.Load())
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
	assert.Equal(t, testStartTime.Add(2*time.Hour), s.ExpiresAt())
}

// The refresh belongs to the session, not to the request that happened to trigger it. A user who
// navigates away (or hits Escape) while a refresh is in flight cancels that request's context; if
// the refresh rode on it, it would fail and Get would destroy a session that was about to be
// renewed successfully.
func TestRefreshSurvivesRequestCancellation(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour, honorCtx: true}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Minute)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	fakeClock.Step(9*time.Minute + 30*time.Second)
	got, err := st.Get(ctx, s.ID())
	require.NoError(t, err)
	assert.Equal(t, int32(1), refresher.calls.Load())
	assert.Equal(t, []byte("id-token-1"), got.Credential().Token)

	// And the session is still in the store, rather than having been evicted.
	_, ok := st.Peek(s.ID())
	assert.True(t, ok)
}

// The flow stream is a single request that stays open for hours and calls KeepAlive on a timer.
// That has to renew the credential on the way, not just bump last-seen: an OIDC session would
// otherwise sit there with an id_token that expired minutes in, since store.alive deliberately does
// not treat a refreshable credential's expiry as terminal.
func TestKeepAliveRefreshesTheCredential(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Second)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)
	ra := NewSessionAuth(st, s)

	fakeClock.Step(1 * time.Second)
	assert.True(t, ra.KeepAlive(t.Context()))
	assert.Equal(t, int32(1), refresher.calls.Load(), "KeepAlive should have renewed the expiring credential")
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
}

// oidcSpec builds an OIDC session whose credential expires expiresIn from the start of the test.
func oidcSpec(refresher Refresher, expiresIn time.Duration) *Spec {
	return &Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(expiresIn)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	}
}

// A failed refresh must not end the session on its own. The refresh fires refreshWindow *before*
// the credential expires, so the session still holds a credential that works, and the identity
// provider being briefly unreachable is indistinguishable here from a refresh token that was
// revoked. Logging a user out over a network blip is the failure mode this avoids; the window is
// the retry budget.
func TestGetSurvivesAFailedRefreshWhileTheCredentialIsStillValid(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour, err: fmt.Errorf("identity provider is unreachable")}
	s, err := st.Create(oidcSpec(refresher, 10*time.Second))
	require.NoError(t, err)
	ra := NewSessionAuth(st, s)

	// Inside the refresh window, but the credential has 9s of life left.
	fakeClock.Step(1 * time.Second)
	assert.True(t, ra.KeepAlive(t.Context()), "a network blip must not log the user out")
	require.Equal(t, int32(1), refresher.calls.Load())
	_, ok := st.Peek(s.ID())
	assert.True(t, ok, "the session should still be there")

	// The provider comes back before the credential expires, and the next tick saves the session.
	refresher.err = nil
	fakeClock.Step(1 * time.Second)
	assert.True(t, ra.KeepAlive(t.Context()))
	assert.Equal(t, int32(2), refresher.calls.Load(), "the failed refresh should have been retried")
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
}

// Once the credential really is gone, though, the session goes with it: a refresh token revoked at
// the provider ends the session within refreshWindow, and the stream stops. This is what makes
// falling through on a failed refresh safe - credentialExpired is what fails closed.
func TestGetDropsTheSessionOnceAFailedRefreshLetsTheCredentialExpire(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour, err: fmt.Errorf("refresh token was rejected by the OIDC provider")}
	s, err := st.Create(oidcSpec(refresher, 10*time.Second))
	require.NoError(t, err)
	ra := NewSessionAuth(st, s)

	fakeClock.Step(1 * time.Second)
	require.True(t, ra.KeepAlive(t.Context()))

	// Past the credential's own expiry, with every refresh still failing.
	fakeClock.Step(10 * time.Second)
	assert.False(t, ra.KeepAlive(t.Context()), "the stream must stop once the credential is gone")
	_, ok := st.Peek(s.ID())
	assert.False(t, ok, "a session whose credential cannot be renewed should be dropped")
}

func TestRefreshIsSingleFlight(t *testing.T) {
	st, fakeClock := newTestStore(t)
	// The refresher blocks until we release it, so every concurrent Get is in flight at once.
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour, delay: make(chan struct{})}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Second)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)
	fakeClock.Step(1 * time.Second)

	// This is the antrea-summary-page shape: three requests fired in one Promise.all. With
	// refresh-token rotation, three concurrent refreshes would invalidate each other.
	const concurrency = 3
	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = st.Get(t.Context(), s.ID())
		}(i)
	}
	// Give the goroutines a chance to pile up on the refresh mutex before releasing.
	assert.Eventually(t, func() bool { return refresher.calls.Load() == 1 }, time.Second, time.Millisecond)
	close(refresher.delay)
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, int32(1), refresher.calls.Load(), "concurrent Get calls should trigger exactly one refresh")
	assert.Equal(t, []byte("id-token-1"), s.Credential().Token)
}

// Get's contract on a failed refresh: serve the request with the credential the session still
// holds, and only give up once that credential is itself past expiry. A refresh token revoked at
// the provider therefore ends the session within refreshWindow rather than instantly, and a
// provider that is merely unreachable for a moment costs nothing at all.
func TestRefreshFailureEndsSessionOnlyOnceTheCredentialExpires(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{err: fmt.Errorf("refresh token revoked")}
	s, err := st.Create(oidcSpec(refresher, 10*time.Second))
	require.NoError(t, err)

	// Inside the refresh window: the refresh is attempted and fails, but the credential is
	// still good, so the request is served with it.
	fakeClock.Step(1 * time.Second)
	got, err := st.Get(t.Context(), s.ID())
	require.NoError(t, err)
	assert.Equal(t, []byte("id-token-0"), got.Credential().Token)
	require.Equal(t, int32(1), refresher.calls.Load())

	// Past the credential's expiry, with the refresh still failing: now the session goes.
	fakeClock.Step(10 * time.Second)
	_, err = st.Get(t.Context(), s.ID())
	require.ErrorIs(t, err, ErrExpired)
	// Evicted rather than left to fail every subsequent request.
	_, err = st.Get(t.Context(), s.ID())
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRefreshDropsCachedTransport(t *testing.T) {
	st, fakeClock := newTestStore(t)
	refresher := &fakeRefresher{expiresIn: 2 * time.Hour}
	s, err := st.Create(&Spec{
		Mode:         ModeOIDC,
		Credential:   Credential{Kind: KindBearer, Token: []byte("id-token-0"), ExpiresAt: testStartTime.Add(10 * time.Second)},
		RefreshToken: []byte("refresh-token-0"),
		Refresher:    refresher,
	})
	require.NoError(t, err)

	var tokens []string
	build := func(cred *Credential) (http.RoundTripper, func(), error) {
		tokens = append(tokens, string(cred.Token))
		return http.DefaultTransport, nil, nil
	}
	_, err = s.transportFor(TransportKeyK8s, build)
	require.NoError(t, err)

	fakeClock.Step(1 * time.Second)
	_, err = st.Get(t.Context(), s.ID())
	require.NoError(t, err)

	// The old transport embeds the old token, so it must not be reused after a refresh.
	_, err = s.transportFor(TransportKeyK8s, build)
	require.NoError(t, err)
	assert.Equal(t, []string{"id-token-0", "id-token-1"}, tokens)
}
