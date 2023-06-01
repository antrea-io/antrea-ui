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
)

const (
	// #nosec G101: not credentials
	refreshTokenCookieName = "antrea-ui-refresh-token"
	refreshTokenCookiePath = "/auth"
)

func SetRefreshTokenCookie(w http.ResponseWriter, token string, cookieSecure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshTokenCookieName,
		Value:    token,
		Path:     refreshTokenCookiePath,
		MaxAge:   0, // make it a session cookie
		Secure:   cookieSecure,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func UnsetRefreshTokenCookie(req *http.Request, w http.ResponseWriter) (string, bool) {
	cookie, err := req.Cookie(refreshTokenCookieName)
	if err != nil {
		// no cookie
		return "", false
	}
	token := cookie.Value
	http.SetCookie(w, &http.Cookie{
		Name:   refreshTokenCookieName,
		Value:  "",
		Path:   refreshTokenCookiePath,
		MaxAge: -1,
	})
	return token, true
}

func GetRefreshTokenFromCookie(req *http.Request) (string, bool) {
	cookie, err := req.Cookie(refreshTokenCookieName)
	if err != nil {
		// no cookie
		return "", false
	}
	return cookie.Value, true
}
