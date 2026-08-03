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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"antrea.io/antrea-ui/pkg/auth/session"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func newTestAuthenticator(t *testing.T, mutators ...func(*Config)) (*Authenticator, session.Store) {
	store := session.NewStore(testr.New(t), session.Options{})
	config := Config{Store: store}
	for _, m := range mutators {
		m(&config)
	}
	a, err := New(testr.New(t), config)
	require.NoError(t, err)
	return a, store
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
		Mode:       session.ModeSAToken,
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
