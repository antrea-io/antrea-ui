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

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
)

func getRefreshTokenSetCookie(response *http.Response) *http.Cookie {
	cookies := response.Cookies()
	for _, cookie := range cookies {
		if cookie.Name == "antrea-ui-refresh-token" {
			return cookie
		}
	}
	return nil
}

func TestLogin(t *testing.T) {
	username := "admin"
	password := "xyz"
	wrongPassword := "abc"

	sendRequest := func(ts *testServer, mutators ...func(req *http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
		for _, m := range mutators {
			m(req)
		}
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("valid login", func(t *testing.T) {
		ts := newTestServer(t)
		refreshToken := getTestToken()
		token := getTestToken()
		gomock.InOrder(
			ts.passwordStore.EXPECT().Compare(gomock.Any(), []byte(password)),
			ts.tokenManager.EXPECT().GetRefreshToken().Return(refreshToken, nil),
			ts.tokenManager.EXPECT().GetToken().Return(token, nil),
		)
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, password)
		})
		assert.Equal(t, http.StatusOK, rr.Code)

		// check body of response
		var data apisv1alpha1.Token
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &data))
		assert.Equal(t, token.Raw, data.AccessToken)
		assert.Equal(t, "Bearer", data.TokenType)
		assert.Equal(t, int64(testTokenValidity/time.Second), data.ExpiresIn)

		// check cookie
		cookie := getRefreshTokenSetCookie(rr.Result())
		require.NotNil(t, cookie, "Missing refresh token cookie in response")
		assert.Equal(t, refreshToken.Raw, cookie.Value)
		assert.Equal(t, "/api/v1/auth", cookie.Path)
		assert.Equal(t, "", cookie.Domain)
		assert.Equal(t, 0, cookie.MaxAge)
		assert.True(t, cookie.HttpOnly)
		assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	})

	t.Run("missing basic auth", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequest(ts)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("wrong password", func(t *testing.T) {
		ts := newTestServer(t)
		ts.passwordStore.EXPECT().Compare(gomock.Any(), []byte(wrongPassword)).Return(fmt.Errorf("bad password"))
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, wrongPassword)
		})
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("rate limiting 0/s", func(t *testing.T) {
		ts := newTestServer(t, setMaxLoginsPerSecond(0))
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, password)
		})
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	})

	t.Run("rate limiting 5/s", func(t *testing.T) {
		ts := newTestServer(t, setMaxLoginsPerSecond(5))
		ts.passwordStore.EXPECT().Compare(gomock.Any(), []byte(wrongPassword)).Return(fmt.Errorf("bad password")).AnyTimes()
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, wrongPassword)
		})
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.Eventually(t, func() bool {
			rr := sendRequest(ts, func(req *http.Request) {
				req.SetBasicAuth(username, wrongPassword)
			})
			return rr.Code == http.StatusTooManyRequests
		}, time.Second, 10*time.Millisecond)
		assert.Eventually(t, func() bool {
			rr := sendRequest(ts, func(req *http.Request) {
				req.SetBasicAuth(username, wrongPassword)
			})
			return rr.Code == http.StatusUnauthorized
		}, time.Second, 100*time.Millisecond)
	})
}

func TestRefreshToken(t *testing.T) {
	sendRequestWithAuthorizationHeader := func(ts *testServer, refreshToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/auth/refresh_token", nil)
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", refreshToken))
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	sendRequestWithCookie := func(ts *testServer, refreshToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/auth/refresh_token", nil)
		req.AddCookie(&http.Cookie{
			Name:  "antrea-ui-refresh-token",
			Value: refreshToken,
		})
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	sendRequestNoAuth := func(ts *testServer) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/auth/refresh_token", nil)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("no auth", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequestNoAuth(ts)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	authMethods := []struct {
		name        string
		requestFunc func(ts *testServer, refreshToken string) *httptest.ResponseRecorder
	}{
		{
			name:        "auth header",
			requestFunc: sendRequestWithAuthorizationHeader,
		},
		{
			name:        "cookie",
			requestFunc: sendRequestWithCookie,
		},
	}

	for _, m := range authMethods {
		t.Run(m.name, func(t *testing.T) {
			t.Run("valid refresh", func(t *testing.T) {
				ts := newTestServer(t)
				refreshToken := getTestToken()
				token := getTestToken()
				gomock.InOrder(
					ts.tokenManager.EXPECT().VerifyRefreshToken(refreshToken.Raw),
					ts.tokenManager.EXPECT().GetToken().Return(token, nil),
				)
				rr := m.requestFunc(ts, refreshToken.Raw)
				assert.Equal(t, http.StatusOK, rr.Code)

				// check body of response
				var data apisv1alpha1.Token
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &data))
				assert.Equal(t, token.Raw, data.AccessToken)
				assert.Equal(t, "Bearer", data.TokenType)
				assert.Equal(t, int64(testTokenValidity/time.Second), data.ExpiresIn)
			})

			t.Run("wrong refresh token", func(t *testing.T) {
				ts := newTestServer(t)
				badToken := "bad"
				ts.tokenManager.EXPECT().VerifyRefreshToken(badToken).Return(fmt.Errorf("bad token"))
				rr := m.requestFunc(ts, badToken)
				assert.Equal(t, http.StatusUnauthorized, rr.Code)
			})
		})
	}

}

func TestLogout(t *testing.T) {
	sendRequest := func(ts *testServer, refreshToken *string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
		if refreshToken != nil {
			req.AddCookie(&http.Cookie{
				Name:  "antrea-ui-refresh-token",
				Value: *refreshToken,
			})
		}
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("with cookie", func(t *testing.T) {
		ts := newTestServer(t)
		refreshToken := getTestToken()
		ts.tokenManager.EXPECT().DeleteRefreshToken(refreshToken.Raw)
		rr := sendRequest(ts, &refreshToken.Raw)
		assert.Equal(t, http.StatusOK, rr.Code)

		// check cookie
		cookie := getRefreshTokenSetCookie(rr.Result())
		require.NotNil(t, cookie, "Missing refresh token cookie in response")
		assert.Empty(t, cookie.Value)
		assert.Equal(t, -1, cookie.MaxAge)
	})

	t.Run("without cookie", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequest(ts, nil)
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}
