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
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
)

// createTokenFunc mirrors ServiceAccountInterface.CreateToken.
type createTokenFunc func(ctx context.Context, name string, tr *authenticationv1.TokenRequest, opts metav1.CreateOptions) (*authenticationv1.TokenRequest, error)

// newContextAwareClientset returns a kubernetes.Interface whose CreateToken calls are handled by
// createToken, with the real request context - unlike the fake clientset's own reactors, which
// never see it (client-go's fake.Fake.Invokes takes no context argument). AdminTokenSource's
// cancellation and timeout behavior can only be tested against a context the fake actually
// observes.
func newContextAwareClientset(createToken createTokenFunc) kubernetes.Interface {
	base := k8sfake.NewSimpleClientset()
	return &contextAwareClientset{
		Interface: base,
		core: &contextAwareCoreV1{
			CoreV1Interface: base.CoreV1(),
			sa: &contextAwareServiceAccounts{
				ServiceAccountInterface: base.CoreV1().ServiceAccounts("ns"),
				createToken:             createToken,
			},
		},
	}
}

type contextAwareClientset struct {
	kubernetes.Interface
	core *contextAwareCoreV1
}

func (c *contextAwareClientset) CoreV1() corev1client.CoreV1Interface { return c.core }

type contextAwareCoreV1 struct {
	corev1client.CoreV1Interface
	sa *contextAwareServiceAccounts
}

func (c *contextAwareCoreV1) ServiceAccounts(string) corev1client.ServiceAccountInterface {
	return c.sa
}

type contextAwareServiceAccounts struct {
	corev1client.ServiceAccountInterface
	createToken createTokenFunc
}

func (s *contextAwareServiceAccounts) CreateToken(ctx context.Context, name string, tr *authenticationv1.TokenRequest, opts metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
	return s.createToken(ctx, name, tr, opts)
}

// A token close enough to expiry (inside adminTokenRenewBefore) must be re-minted rather than
// handed out, but a fresh one must not be re-minted on every call.
func TestAdminTokenSourceCachesUntilRenewWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		createToken := func(_ context.Context, _ string, _ *authenticationv1.TokenRequest, _ metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
			n := calls.Add(1)
			return &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
				Token:               fmt.Sprintf("tok-%d", n),
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(adminTokenExpiration)),
			}}, nil
		}
		src := NewAdminTokenSource(newContextAwareClientset(createToken), "ns", "sa")

		tok1, err := src.Token(t.Context())
		require.NoError(t, err)
		tok2, err := src.Token(t.Context())
		require.NoError(t, err)
		assert.Equal(t, tok1, tok2, "a fresh token must be served from cache")
		assert.Equal(t, int32(1), calls.Load(), "a cached token must not be re-minted")

		// Advance past the point where the cached token enters its renew window.
		time.Sleep(adminTokenExpiration - adminTokenRenewBefore + time.Second)

		tok3, err := src.Token(t.Context())
		require.NoError(t, err)
		assert.NotEqual(t, tok1, tok3, "a token close to expiry must be renewed, not handed out again")
		assert.Equal(t, int32(2), calls.Load())
	})
}

// Concurrent callers that all miss the cache must collapse into one CreateToken call via
// singleflight, not one per caller.
func TestAdminTokenSourceDeduplicatesConcurrentMints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		createToken := func(_ context.Context, _ string, _ *authenticationv1.TokenRequest, _ metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
			calls.Add(1)
			<-release
			return &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
				Token:               "shared-token",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(adminTokenExpiration)),
			}}, nil
		}
		src := NewAdminTokenSource(newContextAwareClientset(createToken), "ns", "sa")

		type result struct {
			token string
			err   error
		}
		const n = 5
		results := make(chan result, n)
		for range n {
			go func() {
				tok, err := src.Token(t.Context())
				results <- result{tok, err}
			}()
		}

		// Let every caller reach either singleflight.Do or the blocking createToken call.
		synctest.Wait()
		close(release)

		for range n {
			r := <-results
			require.NoError(t, r.err)
			assert.Equal(t, "shared-token", r.token)
		}
		assert.Equal(t, int32(1), calls.Load(), "concurrent callers must collapse into one CreateToken call")
	})
}

// The singleflight leader's CreateToken call must not use any single caller's context directly: if
// the leader happens to be the caller whose tab gets closed mid-mint, every other caller riding the
// same call would otherwise fail with a spurious "context canceled" too.
func TestAdminTokenSourceMintSurvivesCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		proceed := make(chan struct{})
		createToken := func(ctx context.Context, _ string, _ *authenticationv1.TokenRequest, _ metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
			close(started)
			<-proceed
			// If the caller's cancellation had propagated here, ctx would already be done.
			assert.NoError(t, ctx.Err(), "the caller's cancellation must not reach the mint call's context")
			return &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
				Token:               "tok",
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(adminTokenExpiration)),
			}}, nil
		}
		src := NewAdminTokenSource(newContextAwareClientset(createToken), "ns", "sa")

		callerCtx, cancel := context.WithCancel(context.Background())
		type result struct {
			token string
			err   error
		}
		results := make(chan result, 1)
		go func() {
			tok, err := src.Token(callerCtx)
			results <- result{tok, err}
		}()

		<-started
		cancel()
		synctest.Wait()
		close(proceed)

		r := <-results
		require.NoError(t, r.err)
		assert.Equal(t, "tok", r.token, "canceling the caller must not fail the mint it triggered")
	})
}

// adminTokenMintTimeout must bound the mint call independent of any caller's own lifetime, so a
// wedged API server cannot block the singleflight leader (and every follower behind it) forever.
func TestAdminTokenSourceMintTimesOutIndependently(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		createToken := func(ctx context.Context, _ string, _ *authenticationv1.TokenRequest, _ metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
			<-ctx.Done() // simulates a wedged API server: never returns on its own.
			return nil, ctx.Err()
		}
		src := NewAdminTokenSource(newContextAwareClientset(createToken), "ns", "sa")

		_, err := src.Token(context.Background())
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
