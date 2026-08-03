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

package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonglil/buflogr"

	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

// HTTP client that does not follow redirects
var clientWithNoRedirect = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func doOAuth2Login(t *testing.T, ts *testServer, userPage string) (*url.URL, []*http.Cookie) {
	loginURL := &url.URL{
		Path: "/auth/oauth2/login",
	}

	if userPage != "" {
		loginURL.RawQuery = url.Values{
			"redirect_url": []string{userPage},
		}.Encode()
	}

	req := httptest.NewRequest("GET", loginURL.String(), nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusSeeOther, rr.Code)
	resp := rr.Result()
	location, err := resp.Location()
	require.NoError(t, err, "Location header is missing")
	return location, resp.Cookies()
}

func doOIDCAuthorize(t *testing.T, url *url.URL) *url.URL {
	// for this one, we need to make a real HTTP request, given that mockOIDC is an actual HTTP
	// server running locally.
	req, err := http.NewRequest("GET", url.String(), nil)
	require.NoError(t, err)
	client := clientWithNoRedirect
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	location, err := resp.Location()
	require.NoError(t, err, "Location header is missing")
	return location
}

func doOAuth2Callback(t *testing.T, ts *testServer, url *url.URL, cookies []*http.Cookie) *url.URL {
	req := httptest.NewRequest("GET", url.String(), nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	resp := rr.Result()
	location, err := resp.Location()
	require.NoError(t, err, "Location header is missing")
	return location
}

func TestOAuth2(t *testing.T) {
	ts := newTestServer(t, enableOIDCAuth())

	userPage, err := url.JoinPath(testServerAddr, "somepage")
	require.NoError(t, err)

	authorizeLocation, cookies := doOAuth2Login(t, ts, userPage)
	for _, cookie := range cookies {
		assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	}

	callbackLocation := doOIDCAuthorize(t, authorizeLocation)

	userRedirectLocation := doOAuth2Callback(t, ts, callbackLocation, cookies)

	expectedLocation, _ := url.Parse(userPage)
	expectedLocation.RawQuery = url.Values{
		"auth_method": []string{"oidc"},
	}.Encode()
	assert.Equal(t, expectedLocation, userRedirectLocation)
}

// Verifying the id_token only proves the OIDC provider issued it. The kube-apiserver is a separate
// trust decision, and it is the one that matters, since the id_token is the credential Antrea UI
// presents upstream. A deployment whose apiserver does not trust the issuer must fail visibly at
// login: without this check the session is created, the first K8s call 401s, that 401 destroys the
// session, and the user is bounced back to the login page with no explanation, every single time.
func TestOAuth2CallbackRejectedByKubernetes(t *testing.T) {
	ts := newTestServer(t, enableOIDCAuth())
	ts.k8sAPIServer.rejectAll = true

	userPage, err := url.JoinPath(testServerAddr, "somepage")
	require.NoError(t, err)
	authorizeLocation, cookies := doOAuth2Login(t, ts, userPage)
	callbackLocation := doOIDCAuthorize(t, authorizeLocation)

	req := httptest.NewRequest("GET", callbackLocation.String(), nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)

	// The login fails outright instead of redirecting the user into the app.
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "--oidc-issuer-url")

	// No session cookie, and nothing left in the store.
	for _, cookie := range rr.Result().Cookies() {
		assert.NotEqual(t, cookieutils.SessionCookieName, cookie.Name,
			"a rejected OIDC login must not set a session cookie")
	}
}

func TestOAuth2InvalidCallback(t *testing.T) {
	testCases := []struct {
		name         string
		skipCookies  bool
		urlMutator   func(*url.URL)
		expectedCode int
	}{
		{
			name:         "missing cookie",
			skipCookies:  true,
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "wrong code",
			urlMutator: func(url *url.URL) {
				values := url.Query()
				values.Set("code", "foobar")
				url.RawQuery = values.Encode()
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name: "missing code",
			urlMutator: func(url *url.URL) {
				values := url.Query()
				values.Del("code")
				url.RawQuery = values.Encode()
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "wrong state",
			urlMutator: func(url *url.URL) {
				values := url.Query()
				values.Set("state", "foobar")
				url.RawQuery = values.Encode()
			},
			expectedCode: http.StatusBadRequest,
		},
		{
			name: "missing state",
			urlMutator: func(url *url.URL) {
				values := url.Query()
				values.Del("state")
				url.RawQuery = values.Encode()
			},
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, enableOIDCAuth())
			authorizeLocation, cookies := doOAuth2Login(t, ts, "")
			callbackLocation := doOIDCAuthorize(t, authorizeLocation)

			if tc.urlMutator != nil {
				tc.urlMutator(callbackLocation)
			}
			req := httptest.NewRequest("GET", callbackLocation.String(), nil)
			if !tc.skipCookies {
				for _, cookie := range cookies {
					req.AddCookie(cookie)
				}
			}
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			require.Equal(t, tc.expectedCode, rr.Code)
		})
	}
}

func TestOAuth2LogoutURL(t *testing.T) {
	const issuerURL = "http://example.com"
	const clientID = "antrea-ui"
	const clientSecret = "abcd"
	const token = "token-xyz"

	newProvider := func(template string) (*OIDCProvider, error) {
		logger := testr.New(t)
		return NewOIDCProvider(
			logger,
			testServerAddr,
			issuerURL,
			"", // discovery URL
			clientID,
			clientSecret,
			template,
			nil,
		)
	}

	t.Run("malformed template - unknown variable", func(t *testing.T) {
		_, err := newProvider("http://example.com/logout/{{Foo}}")
		assert.ErrorContains(t, err, "logout URL is not a valid template")
	})

	t.Run("malformed template - not URL", func(t *testing.T) {
		p, err := newProvider(":foo")
		// URL validation happens when building the URL, not when validating the template.
		require.NoError(t, err)
		_, err = p.BuildLogoutURL(token)
		assert.ErrorContains(t, err, "invalid logout URL")
	})

	t.Run("success", func(t *testing.T) {
		p, err := newProvider("http://example.com/logout?returnTo={{URL}}&client_id={{ClientID}}&id_token={{Token}}")
		require.NoError(t, err)
		logoutURL, err := p.BuildLogoutURL(token)
		require.NoError(t, err)
		expectedURL, err := url.Parse("http://example.com/logout")
		require.NoError(t, err)
		// cannot use ur.Values as it will change the order of query parameters
		expectedURL.RawQuery = fmt.Sprintf("returnTo=%s&client_id=%s&id_token=%s", url.QueryEscape(testServerAddr), url.QueryEscape(clientID), url.QueryEscape(token))
		assert.Equal(t, expectedURL.String(), logoutURL)
	})
}

func TestOAuth2DiscoveryURL(t *testing.T) {
	t.Logf("Starting mock OIDC server")
	mockOIDC, err := mockoidc.Run()
	require.NoError(t, err, "failed to start mock OIDC server")
	defer mockOIDC.Shutdown()
	oidcConfig := mockOIDC.Config()
	issuerURL, err := url.Parse(oidcConfig.Issuer)
	require.NoError(t, err)
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(issuerURL))
	defer proxy.Close()

	initProvider := func(logger logr.Logger, issuerURL, discoveryURL string) error {
		provider, err := NewOIDCProvider(
			logger,
			testServerAddr,
			issuerURL,
			discoveryURL,
			oidcConfig.ClientID,
			oidcConfig.ClientSecret,
			"", // logoutURL
			nil,
		)
		require.NoError(t, err)
		// provider.Init will not spawn any goroutine and will attempt OIDC discover right
		// away, so 1s should be more than enough.
		ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
		defer cancel()
		return provider.Init(ctx)
	}

	t.Run("issuer mismatch error", func(t *testing.T) {
		var buf bytes.Buffer
		logger := buflogr.NewWithBuffer(&buf)
		require.Error(t, initProvider(logger, proxy.URL, ""))
		assert.Contains(t, buf.String(), "did not match the issuer URL returned by provider")
	})

	t.Run("with discovery URL", func(t *testing.T) {
		logger := testr.New(t)
		require.NoError(t, initProvider(logger, oidcConfig.Issuer, proxy.URL))
	})
}
