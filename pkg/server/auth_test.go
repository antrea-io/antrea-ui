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
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func postJSON(ts *testServer, path string, body interface{}) *httptest.ResponseRecorder {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	return rr
}

// jwtWithExpiry builds an unsigned JWT-shaped token with the given "exp" claim. The signature is
// never checked by antrea-ui (only by the API server), so an unsigned token is enough here.
func jwtWithExpiry(expiry time.Time) string {
	enc := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	return enc(`{"alg":"RS256","typ":"JWT"}`) + "." + enc(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())) + ".signature"
}

func TestLogin(t *testing.T) {
	username := "admin"
	password := "xyz"
	wrongPassword := "abc"

	sendRequest := func(ts *testServer, mutators ...func(req *http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/login", nil)
		for _, m := range mutators {
			m(req)
		}
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("valid login", func(t *testing.T) {
		ts := newTestServer(t)
		ts.passwordStore.EXPECT().Compare(gomock.Any(), []byte(password))
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, password)
		})
		assert.Equal(t, http.StatusOK, rr.Code)
		// The response no longer carries any token: the credential stays server-side.
		assert.Empty(t, rr.Body.String())

		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie, "Missing session cookie in response")
		assert.NotEmpty(t, cookie.Value)
		assert.Equal(t, "/", cookie.Path)
		assert.Equal(t, "", cookie.Domain)
		assert.Equal(t, 0, cookie.MaxAge)
		assert.True(t, cookie.HttpOnly)
		assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)

		// The session impersonates antrea-ui-admin: the admin password grants no Kubernetes
		// identity of its own.
		sess, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err)
		assert.Equal(t, session.ModeAdmin, sess.Mode())
		assert.Equal(t, session.KindImpersonate, sess.Credential().Kind)
		assert.Equal(t, testAdminUserName, sess.Credential().UserName)
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
		assert.Nil(t, sessionCookie(rr.Result()))
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

	t.Run("basic auth disabled", func(t *testing.T) {
		ts := newTestServer(t, disableBasicAuth())
		rr := sendRequest(ts, func(req *http.Request) {
			req.SetBasicAuth(username, password)
		})
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

func TestLoginWithToken(t *testing.T) {
	t.Run("valid token", func(t *testing.T) {
		ts := newTestServer(t)
		expiry := time.Now().Add(1 * time.Hour).Truncate(time.Second)
		token := jwtWithExpiry(expiry)
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: token})
		require.Equal(t, http.StatusOK, rr.Code)

		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)
		sess, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err)
		assert.Equal(t, session.ModeToken, sess.Mode())
		assert.Equal(t, session.KindBearer, sess.Credential().Kind)
		assert.Equal(t, []byte(token), sess.Credential().Token)
		// The identity comes from the API server's SelfSubjectReview answer.
		assert.Equal(t, "alice", sess.Username())
		// A projected ServiceAccount token expires, and the session must not outlive it.
		assert.Equal(t, expiry, sess.ExpiresAt())
	})

	t.Run("opaque token has no expiry of its own", func(t *testing.T) {
		ts := newTestServer(t)
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "legacy-opaque-token"}) // #nosec G101: not a real credential
		require.Equal(t, http.StatusOK, rr.Code)
		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)
		sess, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err)
		// Only the absolute session cap applies.
		assert.WithinDuration(t, time.Now().Add(12*time.Hour), sess.ExpiresAt(), time.Minute)
	})

	// A bad paste must fail on the login form, not on the first page load.
	t.Run("token rejected by the API server", func(t *testing.T) {
		ts := newTestServer(t)
		ts.k8sAPIServer.rejectTokens["not-a-real-token"] = true
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "not-a-real-token"})
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.Nil(t, sessionCookie(rr.Result()))
	})

	t.Run("empty token", func(t *testing.T) {
		ts := newTestServer(t)
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "  "})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("disabled", func(t *testing.T) {
		ts := newTestServer(t, disableTokenAuth())
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "tok"})
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("rate limited", func(t *testing.T) {
		ts := newTestServer(t, setMaxLoginsPerSecond(0))
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "tok"})
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	})
}

func testKubeconfig(t *testing.T, userBlock string) string {
	t.Helper()
	return `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: test
  context:
    cluster: test
    user: test-user
current-context: test
users:
- name: test-user
  user:
` + userBlock
}

func clientCertPEM(t *testing.T, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alice"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return base64.StdEncoding.EncodeToString(certPEM), base64.StdEncoding.EncodeToString(keyPEM)
}

func TestLoginWithKubeconfig(t *testing.T) {
	t.Run("token credential", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		kubeconfig := testKubeconfig(t, "    token: my-token\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		require.Equal(t, http.StatusOK, rr.Code)

		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)
		sess, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err)
		assert.Equal(t, session.ModeKubeconfig, sess.Mode())
		assert.Equal(t, session.KindBearer, sess.Credential().Kind)
		assert.Equal(t, []byte("my-token"), sess.Credential().Token)
	})

	t.Run("client certificate credential", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		notAfter := time.Now().Add(6 * time.Hour).Truncate(time.Second)
		certB64, keyB64 := clientCertPEM(t, notAfter)
		kubeconfig := testKubeconfig(t, "    client-certificate-data: "+certB64+"\n    client-key-data: "+keyB64+"\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		require.Equal(t, http.StatusOK, rr.Code)

		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)
		sess, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err)
		assert.Equal(t, session.KindCert, sess.Credential().Kind)
		// The session cannot outlive the certificate.
		assert.Equal(t, notAfter.UTC(), sess.ExpiresAt().UTC())
	})

	t.Run("expired client certificate is rejected at login", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		certB64, keyB64 := clientCertPEM(t, time.Now().Add(-time.Minute))
		kubeconfig := testKubeconfig(t, "    client-certificate-data: "+certB64+"\n    client-key-data: "+keyB64+"\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "expired")
	})

	// An exec plugin describes a program to run on the *user's* machine. Running it inside the
	// antrea-ui Pod would at best fail and at worst execute a user-supplied command there.
	t.Run("exec credential plugin is rejected", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		kubeconfig := testKubeconfig(t, "    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: aws\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "exec credential plugin")
	})

	t.Run("auth-provider is rejected", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		kubeconfig := testKubeconfig(t, "    auth-provider:\n      name: gcp\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "auth-provider")
	})

	t.Run("file references are rejected", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		kubeconfig := testKubeconfig(t, "    client-certificate: /home/alice/.kube/client.crt\n    client-key: /home/alice/.kube/client.key\n")
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "only exist on your machine")
	})

	t.Run("garbage input", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth())
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: "\x00not yaml at all"})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("disabled by default", func(t *testing.T) {
		ts := newTestServer(t)
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: "irrelevant"})
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("rate limited", func(t *testing.T) {
		ts := newTestServer(t, enableKubeconfigAuth(), setMaxLoginsPerSecond(0))
		rr := postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: "irrelevant"})
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	})
}

func TestSession(t *testing.T) {
	getSession := func(ts *testServer, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/auth/session", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("anonymous", func(t *testing.T) {
		ts := newTestServer(t)
		// 401 is the "not logged in" answer the login page expects on a fresh visit.
		assert.Equal(t, http.StatusUnauthorized, getSession(ts, nil).Code)
	})

	t.Run("authenticated", func(t *testing.T) {
		ts := newTestServer(t)
		rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "tok"})
		require.Equal(t, http.StatusOK, rr.Code)
		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)

		rr = getSession(ts, cookie)
		require.Equal(t, http.StatusOK, rr.Code)
		var info apisv1.SessionInfo
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
		assert.True(t, info.Authenticated)
		assert.Equal(t, "token", info.Mode)
		assert.Equal(t, "alice", info.Username)
		require.NotNil(t, info.ExpiresAt)
	})

	t.Run("stale cookie", func(t *testing.T) {
		ts := newTestServer(t)
		rr := getSession(ts, &http.Cookie{Name: cookieutils.SessionCookieName, Value: "deadbeef"})
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		// The stale cookie is cleared so the browser stops sending it.
		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie)
		assert.Equal(t, -1, cookie.MaxAge)
	})
}

type countingRefresher struct {
	calls atomic.Int32
}

func (r *countingRefresher) Refresh(_ context.Context, _ []byte) (session.Credential, []byte, error) {
	r.calls.Add(1)
	return session.Credential{}, nil, fmt.Errorf("countingRefresher: not implemented")
}

func TestLogout(t *testing.T) {
	sendRequest := func(ts *testServer, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/logout", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("with session", func(t *testing.T) {
		ts := newTestServer(t)
		token := []byte("tok-to-be-zeroed")
		sess, err := ts.sessionStore.Create(&session.Spec{
			Mode:       session.ModeToken,
			Credential: session.Credential{Kind: session.KindBearer, Token: token},
		})
		require.NoError(t, err)

		rr := sendRequest(ts, &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()})
		assert.Equal(t, http.StatusOK, rr.Code)

		cookie := sessionCookie(rr.Result())
		require.NotNil(t, cookie, "Missing session cookie in response")
		assert.Empty(t, cookie.Value)
		assert.Equal(t, -1, cookie.MaxAge)

		_, err = ts.sessionStore.Get(t.Context(), sess.ID())
		assert.ErrorIs(t, err, session.ErrNotFound)
		// Logout must wipe the credential, not just drop the reference to it.
		assert.Equal(t, make([]byte, len(token)), token)
	})

	t.Run("without cookie", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequest(ts)
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	// Logout must not wait on, or fail because of, a credential refresh whose result is about
	// to be thrown away. This holds even for a credential that has already expired: refresh
	// would otherwise be due.
	t.Run("does not refresh an expiring OIDC credential", func(t *testing.T) {
		ts := newTestServer(t)
		refresher := &countingRefresher{}
		sess, err := ts.sessionStore.Create(&session.Spec{
			Mode: session.ModeOIDC,
			Credential: session.Credential{
				Kind:      session.KindBearer,
				Token:     []byte("id-token"),
				ExpiresAt: time.Now().Add(-1 * time.Hour),
			},
			RefreshToken: []byte("refresh-token"),
			Refresher:    refresher,
		})
		require.NoError(t, err)

		rr := sendRequest(ts, &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()})
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, int32(0), refresher.calls.Load())

		_, err = ts.sessionStore.Get(t.Context(), sess.ID())
		assert.ErrorIs(t, err, session.ErrNotFound)
	})

	// A browser that was logged in before the session rework still holds the old Path=/auth
	// cookies, which the new Path=/ cookie does not overwrite.
	t.Run("clears legacy cookies", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendRequest(ts, &http.Cookie{Name: "antrea-ui-refresh-token", Value: "old"})
		assert.Equal(t, http.StatusOK, rr.Code)
		var cleared *http.Cookie
		for _, c := range rr.Result().Cookies() {
			if c.Name == "antrea-ui-refresh-token" {
				cleared = c
			}
		}
		require.NotNil(t, cleared)
		assert.Equal(t, -1, cleared.MaxAge)
		assert.Equal(t, "/auth", cleared.Path)
	})
}

// The cross-origin gate inside authn.Resolve only covers requests that already carry a session
// cookie, which the request that *creates* one does not. Without an explicit gate on the login
// routes, a page on another origin can POST a credential of its choosing and have the victim's
// browser keep the resulting session: the victim then browses as the attacker's Kubernetes
// identity, and whatever they do lands under the attacker's account.
func TestLoginRejectsCrossOrigin(t *testing.T) {
	paths := []string{"/auth/login", "/auth/login/token", "/auth/login/kubeconfig"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			ts := newTestServer(t, enableKubeconfigAuth())
			req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.SetBasicAuth("admin", "password")
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusForbidden, rr.Code)
			// Rejected before the handler runs, so no session and no cookie.
			assert.Nil(t, sessionCookie(rr.Result()))
		})
	}
}

func TestLogoutRejectsCrossOriginSubresource(t *testing.T) {
	// An <img src="https://antrea-ui/auth/logout"> is how a forced logout would be delivered:
	// no user interaction, nothing visible.
	ts := newTestServer(t)
	req := httptest.NewRequest("GET", "/auth/logout", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Dest", "image")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// redirect_url comes from the query string, so an unvalidated one makes /auth/logout an open
// redirect: a link that starts on a URL the user recognizes and ends up wherever the attacker
// chose.
func TestLogoutRedirectURL(t *testing.T) {
	testCases := []struct {
		name             string
		redirectURL      string
		expectedCode     int
		expectedLocation string
	}{
		{
			name:             "own origin",
			redirectURL:      testServerAddr + "/?msg=bye",
			expectedCode:     http.StatusFound,
			expectedLocation: testServerAddr + "/?msg=bye",
		},
		{
			name:             "absolute path",
			redirectURL:      "/?msg=bye",
			expectedCode:     http.StatusFound,
			expectedLocation: "/?msg=bye",
		},
		{
			name:         "foreign origin",
			redirectURL:  "https://evil.example.com/",
			expectedCode: http.StatusOK,
		},
		{
			name:         "protocol-relative",
			redirectURL:  "//evil.example.com/",
			expectedCode: http.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// config.URL is what the redirect target is compared against; OIDC is the only
			// mode that requires it, so use an OIDC-enabled server to have one set.
			ts := newTestServer(t, enableOIDCAuth())
			target := "/auth/logout?" + url.Values{"redirect_url": []string{tc.redirectURL}}.Encode()
			req := httptest.NewRequest("GET", target, nil)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, tc.expectedCode, rr.Code)
			assert.Equal(t, tc.expectedLocation, rr.Header().Get("Location"))
		})
	}
}

// Logging in again replaces the caller's session. The old one is unreachable either way, since
// the cookie is overwritten, but until it expires it holds a slot against MaxSessions - so
// repeated logins by one user could fill the store and turn every other login into a 503.
func TestLoginReplacesExistingSession(t *testing.T) {
	ts := newTestServer(t)

	rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: "tok"})
	require.Equal(t, http.StatusOK, rr.Code)
	first := sessionCookie(rr.Result())
	require.NotNil(t, first)

	req := httptest.NewRequest("POST", "/auth/login/token", strings.NewReader(`{"token":"tok2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(first)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	second := sessionCookie(rr.Result())
	require.NotNil(t, second)
	require.NotEqual(t, first.Value, second.Value)

	_, err := ts.sessionStore.Get(t.Context(), first.Value)
	assert.ErrorIs(t, err, session.ErrNotFound)
	_, err = ts.sessionStore.Get(t.Context(), second.Value)
	assert.NoError(t, err)
}

// GET /auth/session resolves an identity and then never calls Kubernetes, so nothing downstream
// can catch a bad bearer token on its behalf. Answering "yes, you are authenticated" to a token
// nothing validated is both a lie and a free oracle for whether a token is worth trying elsewhere.
func TestSessionRejectsUnvalidatedBearerToken(t *testing.T) {
	ts := newTestServer(t)
	ts.k8sAPIServer.rejectTokens["bogus"] = true

	req := httptest.NewRequest("GET", "/auth/session", nil)
	req.Header.Set("Authorization", "Bearer bogus")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// A token the API server accepts works, and reports the identity the API server resolved.
	req = httptest.NewRequest("GET", "/auth/session", nil)
	req.Header.Set("Authorization", "Bearer good")
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var info apisv1.SessionInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &info))
	assert.True(t, info.Authenticated)
	assert.Equal(t, "alice", info.Username)
	// There is no session behind a bearer request, so nothing to report an expiry for.
	assert.Nil(t, info.ExpiresAt)
}
