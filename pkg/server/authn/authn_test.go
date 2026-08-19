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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clocktesting "k8s.io/utils/clock/testing"

	"antrea.io/antrea-ui/pkg/auth/session"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

// fakeValidator stands in for the API server's SelfSubjectReview. A bearer request has no login
// step, so this is what the authenticator consults on every cache miss.
type fakeValidator struct {
	mutex    sync.Mutex
	calls    int
	username string
	// rejected tokens get the answer a real API server gives for a credential it does not
	// accept; anything in failed gets a transient error instead.
	rejected map[string]bool
	failed   map[string]bool
}

func newFakeValidator() *fakeValidator {
	return &fakeValidator{
		username: "alice",
		rejected: map[string]bool{},
		failed:   map[string]bool{},
	}
}

func (v *fakeValidator) ValidateCredential(_ context.Context, cred *session.Credential) (string, error) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	v.calls++
	token := string(cred.Token)
	if v.failed[token] {
		return "", apierrors.NewServiceUnavailable("API server is having a bad day")
	}
	if v.rejected[token] {
		return "", apierrors.NewUnauthorized("invalid bearer token")
	}
	return v.username, nil
}

func (v *fakeValidator) callCount() int {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	return v.calls
}

func newTestAuthenticator(t *testing.T, mutators ...func(*Config)) (*Authenticator, session.Store) {
	a, store, _ := newTestAuthenticatorWithValidator(t, mutators...)
	return a, store
}

func newTestAuthenticatorWithValidator(t *testing.T, mutators ...func(*Config)) (*Authenticator, session.Store, *fakeValidator) {
	store := session.NewStore(testr.New(t), session.Options{})
	validator := newFakeValidator()
	config := Config{Store: store, BearerFallbackEnabled: true, BearerValidator: validator}
	for _, m := range mutators {
		m(&config)
	}
	a, err := New(testr.New(t), config)
	require.NoError(t, err)
	return a, store, validator
}

func newRouter(a *Authenticator) *gin.Engine {
	router := gin.New()
	router.GET("/protected", a.Middleware(), func(c *gin.Context) {
		ra, ok := RequestAuthFromGin(c)
		if !ok {
			c.String(http.StatusInternalServerError, "middleware did not attach an identity")
			return
		}
		c.String(http.StatusOK, string(ra.Mode))
	})
	return router
}

func newSessionCookie(t *testing.T, store session.Store) *http.Cookie {
	t.Helper()
	sess, err := store.Create(&session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	require.NoError(t, err)
	return &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()}
}

// The frontend dev server runs on a different origin from the backend by construction, so neither
// Sec-Fetch-Site nor Origin can ever match. Without an explicit exemption, a strict origin check
// breaks local development outright.
func TestDevModeOriginExemption(t *testing.T) {
	testCases := []struct {
		name         string
		devMode      bool
		origin       string
		fetchSite    string
		expectedCode int
	}{
		{
			name:         "dev server origin, dev mode on",
			devMode:      true,
			origin:       DevOrigin,
			fetchSite:    "same-site",
			expectedCode: http.StatusOK,
		},
		{
			name:         "dev server origin, dev mode off",
			devMode:      false,
			origin:       DevOrigin,
			fetchSite:    "same-site",
			expectedCode: http.StatusForbidden,
		},
		{
			// The exemption is for one specific origin, not for cross-origin in general.
			name:         "other origin, dev mode on",
			devMode:      true,
			origin:       "http://localhost:3001",
			fetchSite:    "cross-site",
			expectedCode: http.StatusForbidden,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			devMode := tc.devMode
			a, store := newTestAuthenticator(t, func(c *Config) { c.DevMode = devMode })
			req := httptest.NewRequest("GET", "/protected", nil)
			req.AddCookie(newSessionCookie(t, store))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			rr := httptest.NewRecorder()
			newRouter(a).ServeHTTP(rr, req)
			assert.Equal(t, tc.expectedCode, rr.Code)
		})
	}
}

// SameSite=Strict is the primary CSRF defence; in dev it has to relax so the cookie survives the
// cross-origin trip from the dev server.
func TestSameSite(t *testing.T) {
	a, _ := newTestAuthenticator(t)
	assert.Equal(t, http.SameSiteStrictMode, a.SameSite())

	devAuth, _ := newTestAuthenticator(t, func(c *Config) { c.DevMode = true })
	assert.Equal(t, http.SameSiteLaxMode, devAuth.SameSite())
}

func TestBearerFallbackCanBeDisabled(t *testing.T) {
	a, _ := newTestAuthenticator(t, func(c *Config) { c.BearerFallbackEnabled = false })
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rr := httptest.NewRecorder()
	newRouter(a).ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestMalformedAuthorizationHeader(t *testing.T) {
	for _, header := range []string{"Bearer", "Bearer ", "Basic dXNlcjpwYXNz", "token abc"} {
		a, _ := newTestAuthenticator(t)
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", header)
		rr := httptest.NewRecorder()
		newRouter(a).ServeHTTP(rr, req)
		assert.Equalf(t, http.StatusUnauthorized, rr.Code, "header %q should not authenticate", header)
	}
}

func TestInvalidServerURL(t *testing.T) {
	_, err := New(testr.New(t), Config{ServerURL: "not a url"})
	assert.ErrorContains(t, err, "invalid server URL")
}

// CSRFMiddleware is what guards the routes that create or destroy a session, which the gate
// inside Resolve cannot cover: it only runs for requests that already carry a session cookie.
func TestCSRFMiddleware(t *testing.T) {
	testCases := []struct {
		name         string
		navigation   bool
		headers      map[string]string
		expectedCode int
	}{
		{
			name:         "same-origin fetch",
			headers:      map[string]string{"Sec-Fetch-Site": "same-origin"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "user-initiated navigation",
			headers:      map[string]string{"Sec-Fetch-Site": "none"},
			expectedCode: http.StatusOK,
		},
		{
			// A non-browser client sends neither header and is not a CSRF vector: it has
			// no cookie jar attaching credentials on the attacker's behalf.
			name:         "no browser headers",
			expectedCode: http.StatusOK,
		},
		{
			name:         "cross-site fetch",
			headers:      map[string]string{"Sec-Fetch-Site": "cross-site"},
			expectedCode: http.StatusForbidden,
		},
		{
			// SameSite is keyed on the registrable domain, so a sibling subdomain is
			// "same-site" while still being a different origin.
			name:         "same-site fetch",
			headers:      map[string]string{"Sec-Fetch-Site": "same-site"},
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "foreign origin, no Sec-Fetch-Site",
			headers:      map[string]string{"Origin": "https://evil.example.com"},
			expectedCode: http.StatusForbidden,
		},
		{
			// The frontend sends the user to /auth/logout with window.location, which is
			// cross-site whenever the frontend and the backend are on different origins.
			name:       "cross-site top-level navigation, navigation route",
			navigation: true,
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site",
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Dest": "document",
			},
			expectedCode: http.StatusOK,
		},
		{
			// The same relaxation must not extend to an <img> or an iframe, which is how a
			// forced logout would actually be delivered.
			name:       "cross-site subresource, navigation route",
			navigation: true,
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site",
				"Sec-Fetch-Mode": "no-cors",
				"Sec-Fetch-Dest": "image",
			},
			expectedCode: http.StatusForbidden,
		},
		{
			name:       "cross-site top-level navigation, strict route",
			navigation: false,
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site",
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Dest": "document",
			},
			expectedCode: http.StatusForbidden,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestAuthenticator(t)
			middleware := a.CSRFMiddleware()
			if tc.navigation {
				middleware = a.NavigationCSRFMiddleware()
			}
			router := gin.New()
			router.POST("/gated", middleware, func(c *gin.Context) { c.Status(http.StatusOK) })
			req := httptest.NewRequest("POST", "/gated", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			assert.Equal(t, tc.expectedCode, rr.Code)
		})
	}
}

// A redirect target that comes from the query string turns antrea-ui's own URL into a way to
// bounce a browser anywhere, which is what makes a phishing link look legitimate.
func TestSafeRedirectURL(t *testing.T) {
	const serverURL = "https://antrea-ui.example.com"
	testCases := []struct {
		name     string
		raw      string
		expected string
	}{
		{name: "empty", raw: "", expected: ""},
		{name: "absolute path", raw: "/somepage?msg=hello", expected: "/somepage?msg=hello"},
		{name: "own origin", raw: serverURL + "/somepage", expected: serverURL + "/somepage"},
		{name: "own origin, different case", raw: "https://ANTREA-UI.example.com/x", expected: "https://ANTREA-UI.example.com/x"},
		{name: "foreign origin", raw: "https://evil.example.com/", expected: ""},
		{name: "own host, wrong scheme", raw: "http://antrea-ui.example.com/", expected: ""},
		// "//evil.example.com" parses with an empty scheme but is protocol-relative: a
		// browser follows it off the origin.
		{name: "protocol-relative", raw: "//evil.example.com/", expected: ""},
		{name: "javascript URL", raw: "javascript:alert(1)", expected: ""},
		{name: "relative with no leading slash", raw: "somepage", expected: ""},
		// net/url parses these as an ordinary path with an empty Host, but a browser
		// resolving them against an http(s) base treats "\" as "/" in the path-start state
		// (WHATWG URL), so they are protocol-relative where it counts.
		{name: "backslash-prefixed", raw: `/\evil.example.com`, expected: ""},
		{name: "backslash then slash", raw: `/\/evil.example.com`, expected: ""},
		{name: "leading backslash", raw: `\\evil.example.com`, expected: ""},
		{name: "backslash later in path", raw: `/somepage\x`, expected: ""},
	}

	a, _ := newTestAuthenticator(t, func(c *Config) { c.ServerURL = serverURL })
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/auth/logout", nil)
			assert.Equal(t, tc.expected, a.SafeRedirectURL(c, tc.raw))
		})
	}
}

// With no config.URL (a deployment that does not use OIDC, where it is not required), the
// comparison falls back to the request's own Host.
func TestSafeRedirectURLWithoutServerURL(t *testing.T) {
	a, _ := newTestAuthenticator(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/auth/logout", nil)
	c.Request.Host = "antrea-ui.example.com"

	assert.Equal(t, "https://antrea-ui.example.com/x", a.SafeRedirectURL(c, "https://antrea-ui.example.com/x"))
	assert.Equal(t, "", a.SafeRedirectURL(c, "https://evil.example.com/x"))
}

// A bearer request has no login step, so Resolve is the only place its credential is ever checked.
// Leaving it to the upstream call is not enough: GET /auth/session and the flow stream resolve an
// identity and then never talk to Kubernetes, so an unvalidated token there is simply believed.
func TestBearerTokenIsValidated(t *testing.T) {
	t.Run("rejected token", func(t *testing.T) {
		a, _, validator := newTestAuthenticatorWithValidator(t)
		validator.rejected["bogus"] = true
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer bogus")
		rr := httptest.NewRecorder()
		newRouter(a).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.Equal(t, 1, validator.callCount())
	})

	t.Run("accepted token", func(t *testing.T) {
		a, _, validator := newTestAuthenticatorWithValidator(t)
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		newRouter(a).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, 1, validator.callCount())
	})

	// An API server that is unreachable is not the same as a token it refused. Answering 401
	// would tell a client with a perfectly good token to go and get another one.
	t.Run("API server unavailable", func(t *testing.T) {
		a, _, validator := newTestAuthenticatorWithValidator(t)
		validator.failed["good"] = true
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		newRouter(a).ServeHTTP(rr, req)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})
}

// The identity comes from the API server, not from the caller.
func TestBearerUsernameComesFromValidation(t *testing.T) {
	a, _, validator := newTestAuthenticatorWithValidator(t)
	validator.username = "system:serviceaccount:default:reader"
	router := gin.New()
	router.GET("/whoami", a.Middleware(), func(c *gin.Context) {
		ra, _ := RequestAuthFromGin(c)
		c.String(http.StatusOK, ra.Username)
	})
	req := httptest.NewRequest("GET", "/whoami", nil)
	req.Header.Set("Authorization", "Bearer good")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "system:serviceaccount:default:reader", rr.Body.String())
}

// Validating on every request would put an API server call in front of every API call, so a
// successful validation is cached for a short while. A failure is deliberately not cached.
func TestBearerValidationIsCached(t *testing.T) {
	a, _, validator := newTestAuthenticatorWithValidator(t)
	fakeClock := clocktesting.NewFakeClock(time.Now())
	a.bearer.clock = fakeClock
	router := newRouter(a)
	send := func(token string) int {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}

	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusOK, send("good"))
	}
	assert.Equal(t, 1, validator.callCount(), "repeat requests with the same token should be served from the cache")

	// A different token is a different cache entry.
	require.Equal(t, http.StatusOK, send("also-good"))
	assert.Equal(t, 2, validator.callCount())

	// A revoked token keeps working until its cache entry ages out, and stops afterwards. That
	// window is the price of not calling the API server on every request.
	validator.rejected["good"] = true
	require.Equal(t, http.StatusOK, send("good"))
	fakeClock.Step(bearerCacheTTL + time.Second)
	assert.Equal(t, http.StatusUnauthorized, send("good"))
}

// The cache is keyed on a hash, never on the token: it outlives the request, and credential
// material must not sit in a long-lived structure.
func TestBearerCacheKeyIsHashed(t *testing.T) {
	a, _, _ := newTestAuthenticatorWithValidator(t)
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rr := httptest.NewRecorder()
	newRouter(a).ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	for _, key := range a.bearer.cache.Keys() {
		assert.NotContains(t, key, "super-secret-token")
	}
	assert.True(t, a.bearer.cache.Contains(cacheKey([]byte("super-secret-token"))))
}

// Without a throttle on cache misses, antrea-ui is an unauthenticated, unthrottled way to test
// Kubernetes credentials against an API server the caller may not be able to reach directly - and
// every attempt is an API server request that a caller with no credential at all can trigger.
func TestBearerValidationMissesAreRateLimited(t *testing.T) {
	a, _, validator := newTestAuthenticatorWithValidator(t)
	router := newRouter(a)
	send := func(token string) int {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}

	// Every token is distinct, so every request is a cache miss and reaches the validator.
	throttled := false
	for i := 0; i < bearerMissBurst+20; i++ {
		if send(fmt.Sprintf("token-%d", i)) == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	assert.True(t, throttled, "a burst of distinct tokens should be throttled")
	assert.LessOrEqual(t, validator.callCount(), bearerMissBurst+1,
		"throttled requests must not reach the API server")
}

// Accepting bearer tokens with no way to check them would mean believing every one of them, so
// this is refused at startup rather than at runtime.
func TestBearerFallbackRequiresValidator(t *testing.T) {
	store := session.NewStore(testr.New(t), session.Options{})
	_, err := New(testr.New(t), Config{Store: store, BearerFallbackEnabled: true})
	assert.ErrorContains(t, err, "no credential validator")

	// Disabled needs no validator.
	_, err = New(testr.New(t), Config{Store: store, BearerFallbackEnabled: false})
	assert.NoError(t, err)
}

// jwtWithExpiry builds an unsigned JWT-shaped token with the given "exp". Nothing here verifies
// the signature - the API server does, which is what makes the claim usable once validation has
// succeeded.
func jwtWithExpiry(expiry time.Time) string {
	enc := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	return enc(`{"alg":"RS256","typ":"JWT"}`) + "." + enc(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())) + ".signature"
}

// The expiry is only read once the API server has accepted the token, so it is a claim that has
// been checked rather than one the caller asserted. It is what bounds a long-running request that
// never presents the credential again.
func TestBearerCredentialCarriesValidatedExpiry(t *testing.T) {
	expiry := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	token := jwtWithExpiry(expiry)

	a, _, _ := newTestAuthenticatorWithValidator(t)
	var got *session.RequestAuth
	router := gin.New()
	router.GET("/protected", a.Middleware(), func(c *gin.Context) {
		got, _ = RequestAuthFromGin(c)
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, got)
	assert.Equal(t, expiry, got.Credential().ExpiresAt)
	assert.True(t, got.KeepAlive(t.Context()), "a credential that has not expired keeps the request alive")

	// An opaque token has no expiry claim, so there is nothing to enforce.
	a2, _, _ := newTestAuthenticatorWithValidator(t)
	router2 := gin.New()
	router2.GET("/protected", a2.Middleware(), func(c *gin.Context) {
		got, _ = RequestAuthFromGin(c)
		c.Status(http.StatusOK)
	})
	req = httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer legacy-opaque-token") // #nosec G101: not a real credential
	rr = httptest.NewRecorder()
	router2.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, got.Credential().ExpiresAt.IsZero())
}

// A cache entry must not outlive the token it vouches for: a token expiring sooner than the TTL
// would otherwise keep being accepted from the cache after it stopped being valid.
func TestBearerCacheEntryCappedAtTokenExpiry(t *testing.T) {
	a, _, validator := newTestAuthenticatorWithValidator(t)
	fakeClock := clocktesting.NewFakeClock(time.Now())
	a.bearer.clock = fakeClock
	router := newRouter(a)

	// Expires well inside the cache TTL.
	token := jwtWithExpiry(fakeClock.Now().Add(5 * time.Second))
	send := func() int {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}

	require.Equal(t, http.StatusOK, send())
	require.Equal(t, 1, validator.callCount())
	fakeClock.Step(10 * time.Second)
	// Still inside bearerCacheTTL, but past the token's own expiry, so the cache must not answer.
	require.Equal(t, http.StatusOK, send())
	assert.Equal(t, 2, validator.callCount(), "an entry must not outlive the token's expiry")
}
