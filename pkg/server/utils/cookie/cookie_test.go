// Copyright 2023 Antrea Authors.
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

package cookie

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The session ID is the browser's only credential, so the attributes that keep it out of reach of
// script and of cross-site requests are part of the contract, not incidental.
func TestSetSessionCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	SetSessionCookie(rr, "0123456789abcdef", true, http.SameSiteStrictMode)

	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.Equal(t, SessionCookieName, c.Name)
	assert.Equal(t, "0123456789abcdef", c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	// A session cookie: it must not survive the browser being closed.
	assert.Zero(t, c.MaxAge)
	assert.True(t, c.Expires.IsZero())
}

func TestSetSessionCookieInsecure(t *testing.T) {
	rr := httptest.NewRecorder()
	SetSessionCookie(rr, "0123456789abcdef", false, http.SameSiteLaxMode)

	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.False(t, cookies[0].Secure)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	// HttpOnly is not negotiable, even in development mode.
	assert.True(t, cookies[0].HttpOnly)
}

func TestGetSessionCookie(t *testing.T) {
	testCases := []struct {
		name          string
		cookie        *http.Cookie
		expectedID    string
		expectedFound bool
	}{
		{
			name:          "present",
			cookie:        &http.Cookie{Name: SessionCookieName, Value: "0123456789abcdef"},
			expectedID:    "0123456789abcdef",
			expectedFound: true,
		},
		{
			name:          "absent",
			cookie:        nil,
			expectedFound: false,
		},
		{
			name:          "other cookie",
			cookie:        &http.Cookie{Name: "unrelated", Value: "x"},
			expectedFound: false,
		},
		{
			// An empty value is what UnsetSessionCookie writes, so it must not read back as
			// a session ID.
			name:          "empty value",
			cookie:        &http.Cookie{Name: SessionCookieName, Value: ""},
			expectedFound: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			id, found := GetSessionCookie(req)
			assert.Equal(t, tc.expectedFound, found)
			assert.Equal(t, tc.expectedID, id)
		})
	}
}

func TestUnsetSessionCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	UnsetSessionCookie(rr)

	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, SessionCookieName, cookies[0].Name)
	assert.Empty(t, cookies[0].Value)
	// Path must match the one used when setting it, or the browser keeps the original.
	assert.Equal(t, "/", cookies[0].Path)
	assert.Equal(t, -1, cookies[0].MaxAge)
}

// The pre-session cookies are scoped to Path=/auth, so the new Path=/ session cookie does not
// overwrite them. A browser that was logged in across the upgrade would otherwise keep them.
func TestUnsetLegacyCookies(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "antrea-ui-refresh-token", Value: "stale-refresh-token"})
	// The id_token cookie could be split into chunks, so every chunk has to be cleared.
	req.AddCookie(&http.Cookie{Name: "antrea-ui-oidc-id-token", Value: "3:aaa"})
	req.AddCookie(&http.Cookie{Name: "antrea-ui-oidc-id-token-1", Value: "bbb"})
	req.AddCookie(&http.Cookie{Name: "antrea-ui-oidc-id-token-2", Value: "ccc"})
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "0123456789abcdef"})

	rr := httptest.NewRecorder()
	UnsetLegacyCookies(req, rr)

	cleared := make(map[string]*http.Cookie)
	for _, c := range rr.Result().Cookies() {
		cleared[c.Name] = c
	}
	require.Len(t, cleared, 4)
	for _, name := range []string{
		"antrea-ui-refresh-token",
		"antrea-ui-oidc-id-token",
		"antrea-ui-oidc-id-token-1",
		"antrea-ui-oidc-id-token-2",
	} {
		c, ok := cleared[name]
		require.True(t, ok, "cookie %s was not cleared", name)
		assert.Empty(t, c.Value)
		assert.Equal(t, "/auth", c.Path)
		assert.Equal(t, -1, c.MaxAge)
	}
	// The current session cookie is not a legacy cookie and must be left alone.
	assert.NotContains(t, cleared, SessionCookieName)
}

// Nothing to clear means no Set-Cookie headers at all, so an ordinary request does not carry
// pointless cookie churn on every response.
func TestUnsetLegacyCookiesNoop(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "0123456789abcdef"})

	rr := httptest.NewRecorder()
	UnsetLegacyCookies(req, rr)

	assert.Empty(t, rr.Result().Cookies())
}
