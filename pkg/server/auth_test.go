package server

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
		req, err := http.NewRequest("GET", "/api/v1/auth/login", nil)
		require.NoError(t, err)
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
		assert.Equal(t, int(testTokenValidity/time.Second), cookie.MaxAge)
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
}

func TestRefreshToken(t *testing.T) {
	sendRequest := func(ts *testServer, refreshToken *string) *httptest.ResponseRecorder {
		req, err := http.NewRequest("GET", "/api/v1/auth/refresh_token", nil)
		require.NoError(t, err)
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

	t.Run("valid refresh", func(t *testing.T) {
		ts := newTestServer(t)
		refreshToken := getTestToken()
		token := getTestToken()
		gomock.InOrder(
			ts.tokenManager.EXPECT().VerifyRefreshToken(refreshToken.Raw),
			ts.tokenManager.EXPECT().GetToken().Return(token, nil),
		)
		rr := sendRequest(ts, &refreshToken.Raw)
		assert.Equal(t, http.StatusOK, rr.Code)

		// check body of response
		var data apisv1alpha1.Token
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &data))
		assert.Equal(t, token.Raw, data.AccessToken)
		assert.Equal(t, "Bearer", data.TokenType)
		assert.Equal(t, int64(testTokenValidity/time.Second), data.ExpiresIn)
	})

	t.Run("missing cookie", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequest(ts, nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("wrong refresh token", func(t *testing.T) {
		ts := newTestServer(t)
		badToken := "bad"
		ts.tokenManager.EXPECT().VerifyRefreshToken(badToken).Return(fmt.Errorf("bad token"))
		rr := sendRequest(ts, &badToken)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}

func TestLogout(t *testing.T) {
	sendRequest := func(ts *testServer, refreshToken *string) *httptest.ResponseRecorder {
		req, err := http.NewRequest("GET", "/api/v1/auth/logout", nil)
		require.NoError(t, err)
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
