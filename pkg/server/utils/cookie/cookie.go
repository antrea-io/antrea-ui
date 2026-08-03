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
	"strings"
)

const (
	// SessionCookieName holds the opaque session ID. The credential itself stays server-side.
	SessionCookieName = "antrea-ui-session"
	// The cookie is scoped to the whole site: the session authenticates /api/v1 requests as
	// well as the /auth endpoints.
	sessionCookiePath = "/"

	// #nosec G101: not credentials
	refreshTokenCookieName = "antrea-ui-refresh-token"
	refreshTokenCookiePath = "/auth"
	// #nosec G101: not credentials
	legacyOIDCIDTokenCookieName = "antrea-ui-oidc-id-token"
)

// SetSessionCookie sets the session cookie. sameSite is a parameter only so that development
// mode can relax it; production always uses Strict, which is the first half of antrea-ui's CSRF
// defence (the Origin/Sec-Fetch-Site check being the second).
func SetSessionCookie(w http.ResponseWriter, sessionID string, cookieSecure bool, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     sessionCookiePath,
		MaxAge:   0, // make it a session cookie
		Secure:   cookieSecure,
		HttpOnly: true,
		SameSite: sameSite,
	})
}

// GetSessionCookie returns the session ID from the request, if present.
func GetSessionCookie(req *http.Request) (string, bool) {
	cookie, err := req.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}

// UnsetSessionCookie clears the session cookie.
func UnsetSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   SessionCookieName,
		Value:  "",
		Path:   sessionCookiePath,
		MaxAge: -1,
	})
}

// UnsetLegacyCookies clears the cookies used by the pre-session authentication scheme.
//
// The old refresh-token cookie is scoped to Path=/auth, so the new Path=/ session cookie does not
// overwrite it: without this, a browser that was logged in before the upgrade would keep a stale
// cookie indefinitely. The old id_token cookie is no longer needed at all, since the id_token now
// lives in the session. Both can be dropped a release after the session rework ships.
func UnsetLegacyCookies(req *http.Request, w http.ResponseWriter) {
	if _, err := req.Cookie(refreshTokenCookieName); err == nil {
		http.SetCookie(w, &http.Cookie{
			Name:   refreshTokenCookieName,
			Value:  "",
			Path:   refreshTokenCookiePath,
			MaxAge: -1,
		})
	}
	// The id_token cookie may have been split into chunks, so clear every chunk we see.
	for _, c := range req.Cookies() {
		if strings.HasPrefix(c.Name, legacyOIDCIDTokenCookieName) {
			http.SetCookie(w, &http.Cookie{
				Name:   c.Name,
				Value:  "",
				Path:   refreshTokenCookiePath,
				MaxAge: -1,
			})
		}
	}
}
