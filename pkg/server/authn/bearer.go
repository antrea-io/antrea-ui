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

package authn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/clock"

	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/k8s"
	servererrors "antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/ratelimit"
)

const (
	// bearerCacheTTL is how long a successful validation is reused before the API server is
	// asked again. It bounds how long a token that has since been revoked keeps working: the
	// price of not making an API server call on every single request.
	bearerCacheTTL = 60 * time.Second
	// bearerCacheSize bounds the cache. Entries are tiny (a hash and a username), and the LRU
	// is what keeps an attacker cycling through distinct tokens from growing it without limit.
	bearerCacheSize = 1024

	// bearerMissRate and bearerMissBurst throttle validations that miss the cache, which are
	// the ones that cost an API server call. A legitimate client validates once per token per
	// bearerCacheTTL, so it never comes close; a caller trying tokens does nothing else.
	bearerMissRate  = "5/s"
	bearerMissBurst = 10
	// bearerMissClients bounds the per-client limiter cache.
	bearerMissClients = 10000
)

// CredentialValidator asks the Kubernetes API server whether a credential is accepted, and which
// identity it resolves to. *k8s.ClientFactory implements it.
//
// It is an interface, rather than a *k8s.ClientFactory, so that tests can substitute one without
// standing up an API server.
type CredentialValidator interface {
	ValidateCredential(ctx context.Context, cred *session.Credential) (string, error)
}

type bearerCacheEntry struct {
	username   string
	validUntil time.Time
}

// bearerValidator checks "Authorization: Bearer" tokens against the Kubernetes API server.
//
// Every other authentication path is checked at the point it is established: a login validates
// the credential before creating a session, and a session then carries an already-validated one.
// A bearer request establishes an identity per request, with no login, so this is the only place
// the check can happen.
//
// It cannot be left to the upstream call to reject a bad token, even though for most routes it
// would. Two routes resolve an identity and then never present the credential to Kubernetes at
// all - GET /auth/session, and the flow stream, which reads from the Flow Aggregator over
// antrea-ui's own connection. On those, "the API server will catch it" is not true, and an
// unvalidated token is simply believed.
//
// Validating here rather than in each handler also removes antrea-ui as an unauthenticated,
// unthrottled way to test Kubernetes credentials against an API server the caller may not be able
// to reach directly.
type bearerValidator struct {
	validator   CredentialValidator
	cache       *lru.Cache[string, bearerCacheEntry]
	missLimiter ratelimit.Interface
	clock       clock.Clock
	ttl         time.Duration
}

func newBearerValidator(validator CredentialValidator) (*bearerValidator, error) {
	cache, err := lru.New[string, bearerCacheEntry](bearerCacheSize)
	if err != nil {
		return nil, fmt.Errorf("error when initializing bearer token cache: %w", err)
	}
	limiter, err := ratelimit.NewClientRateLimiter(bearerMissRate, bearerMissBurst, bearerMissClients, ratelimit.ClientKeyIP)
	if err != nil {
		return nil, fmt.Errorf("error when initializing bearer token rate limiter: %w", err)
	}
	return &bearerValidator{
		validator:   validator,
		cache:       cache,
		missLimiter: limiter,
		clock:       clock.RealClock{},
		ttl:         bearerCacheTTL,
	}, nil
}

// cacheKey is the SHA-256 of the token. The token itself is never used as a map key: the cache
// outlives the request, and credential material must not sit in a long-lived structure.
func cacheKey(token []byte) string {
	sum := sha256.Sum256(token)
	return hex.EncodeToString(sum[:])
}

// validate returns the Kubernetes identity behind token, or an error to answer the request with.
func (v *bearerValidator) validate(ctx context.Context, req *http.Request, token []byte) (string, *servererrors.ServerError) {
	key := cacheKey(token)
	now := v.clock.Now()
	if entry, ok := v.cache.Get(key); ok && now.Before(entry.validUntil) {
		return entry.username, nil
	}

	// Only cache misses reach the API server, so that is what is throttled. A rejected token is
	// deliberately not cached: caching failures would let one unlucky validation lock a caller
	// out for the whole TTL, and the rate limiter already bounds the cost of retries.
	if !v.missLimiter.Allow(now, req) {
		return "", &servererrors.ServerError{
			Code:    http.StatusTooManyRequests,
			Message: "Too many token validations, please try again later",
		}
	}

	cred := session.Credential{Kind: session.KindBearer, Token: token}
	username, err := v.validator.ValidateCredential(ctx, &cred)
	if err != nil {
		// An API server that is unreachable or erroring is not the same as a token it
		// refused. Reporting the former as 401 would tell a client with a perfectly good
		// token to go and get another one.
		if !apierrors.IsUnauthorized(err) && !apierrors.IsForbidden(err) {
			return "", &servererrors.ServerError{
				Code:    http.StatusServiceUnavailable,
				Message: "Could not verify the token with Kubernetes, please try again later",
				Err:     fmt.Errorf("bearer token validation failed: %w", err),
			}
		}
		return "", &servererrors.ServerError{
			Code:    http.StatusUnauthorized,
			Message: "Kubernetes rejected this token",
		}
	}

	// The entry must not outlive the token it vouches for. The API server has just accepted the
	// token, so its "exp" is a claim that has been checked, not one the caller asserted.
	validUntil := v.clock.Now().Add(v.ttl)
	if expiry := k8s.JWTExpiry(token); !expiry.IsZero() && expiry.Before(validUntil) {
		validUntil = expiry
	}
	v.cache.Add(key, bearerCacheEntry{username: username, validUntil: validUntil})
	return username, nil
}
