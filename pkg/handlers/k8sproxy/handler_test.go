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

package k8sproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"antrea.io/antrea-ui/pkg/auth/session"
	k8stesting "antrea.io/antrea-ui/pkg/k8s/testing"
)

// upstreamURL is the URL of the fake API server behind the proxy, which uses the in-memory network
// of httptest.NewTestServer: only the transport of its own client can reach it. It is not
// example.com, the URL of such a server, as that is also the default host of httptest.NewRequest,
// which TestK8sProxyHandler must tell apart from the host of the upstream.
const upstreamURL = k8stesting.InMemoryServerURL

// staticTransport stands in for k8s.ClientFactory.TransportForRequest.
func staticTransport(rt http.RoundTripper) func(*http.Request) (http.RoundTripper, error) {
	return func(*http.Request) (http.RoundTripper, error) { return rt, nil }
}

func TestK8sProxyHandler(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusOK)
	}))

	logger := testr.New(t)
	serverURL, err := url.Parse(upstreamURL)
	require.NoError(t, err)
	h := NewK8sProxyHandler(logger, serverURL, staticTransport(ts.Client().Transport))

	req := httptest.NewRequest("GET", "/api/v1/pods", nil)
	req.RemoteAddr = "127.0.0.1:32167"
	req.Header.Add("X-Forwarded-For", "10.0.0.1")
	// a malicious UI user should not be able to set their own impersonation identity
	req.Header.Add("Impersonate-User", "system:admin")
	req.Header.Add("Impersonate-Uid", "0")
	req.Header.Add("Impersonate-Group", "system:masters")
	req.Header.Add("Impersonate-Extra-foo", "bar")
	// The credentials the client used to authenticate to antrea-ui must not reach the API
	// server: the proxy authenticates the request itself. The session cookie in particular is
	// credential-equivalent for the whole UI and would end up in the API server's audit log.
	req.Header.Add("Cookie", "antrea-ui-session=0123456789abcdef")
	req.Header.Add("Authorization", "Bearer some-user-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedReq)
	assert.Equal(t, "GET", capturedReq.Method)
	assert.Equal(t, "/api/v1/pods", capturedReq.URL.String())
	header := capturedReq.Header
	// original X-Forwarded-For value should have been preserved
	assert.Equal(t, "10.0.0.1, 127.0.0.1", header.Get("X-Forwarded-For"))
	// example.com is default for httptest.NewRequest
	assert.Equal(t, "example.com", header.Get("X-Forwarded-Host"))
	assert.Equal(t, "http", header.Get("X-Forwarded-Proto"))
	assert.Equal(t, serverURL.Host, capturedReq.Host)
	assert.Empty(t, header.Get("Impersonate-User"))
	assert.Empty(t, header.Get("Impersonate-Uid"))
	assert.Empty(t, header.Get("Impersonate-Group"))
	assert.Empty(t, header.Get("Impersonate-Extra-foo"))
	assert.Empty(t, header.Get("Cookie"))
	assert.Empty(t, header.Get("Authorization"))
}

// A write request must reach the API server as sent: method, query (e.g. dryRun, fieldManager),
// body, and the Content-Type which selects the patch strategy.
func TestK8sProxyHandlerWriteRequest(t *testing.T) {
	var capturedReq *http.Request
	var capturedBody []byte
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))

	serverURL, err := url.Parse(upstreamURL)
	require.NoError(t, err)
	h := NewK8sProxyHandler(testr.New(t), serverURL, staticTransport(ts.Client().Transport))

	const path = "/api/v1/namespaces/default/pods/test-pod?dryRun=All&fieldManager=antrea-ui"
	const body = `{"metadata":{"labels":{"app":"test"}}}`
	req := httptest.NewRequest("PATCH", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedReq)
	assert.Equal(t, "PATCH", capturedReq.Method)
	assert.Equal(t, path, capturedReq.URL.String())
	assert.Equal(t, "application/merge-patch+json", capturedReq.Header.Get("Content-Type"))
	assert.Equal(t, body, string(capturedBody))
}

// What the proxy relays reaches the browser on the UI's origin, and not all of it is the API
// server's own (e.g. an aggregated API server's responses), so it must not be able to run script or
// set cookies there, whatever headers it comes with.
func TestK8sProxyHandlerSandboxesResponses(t *testing.T) {
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "script-src *")
		w.Header().Add("Set-Cookie", "antrea-ui-session=attacker-session; Path=/")
		w.Header().Add("Set-Cookie", "other=value; Path=/")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<script>alert(1)</script>"))
	}))

	serverURL, err := url.Parse(upstreamURL)
	require.NoError(t, err)
	h := NewK8sProxyHandler(testr.New(t), serverURL, staticTransport(ts.Client().Transport))

	req := httptest.NewRequest("GET", "/apis/example.com/v1/widgets", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, []string{"sandbox"}, rr.Header().Values("Content-Security-Policy"))
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Empty(t, rr.Header().Values("Set-Cookie"))
}

// The proxy must tell "the API server rejected the credential" (401) apart from "this user is not
// allowed to do that" (403): only the former means the session is over.
func TestK8sProxyHandlerInvalidatesSessionOn401(t *testing.T) {
	testCases := []struct {
		name              string
		upstreamStatus    int
		expectInvalidated bool
	}{
		{name: "unauthorized", upstreamStatus: http.StatusUnauthorized, expectInvalidated: true},
		{name: "forbidden", upstreamStatus: http.StatusForbidden, expectInvalidated: false},
		{name: "ok", upstreamStatus: http.StatusOK, expectInvalidated: false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.upstreamStatus)
			}))

			serverURL, err := url.Parse(upstreamURL)
			require.NoError(t, err)
			h := NewK8sProxyHandler(testr.New(t), serverURL, staticTransport(ts.Client().Transport))

			store := session.NewStore(testr.New(t), session.Options{})
			sess, err := store.Create(&session.Spec{
				Mode:       session.ModeToken,
				Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
			})
			require.NoError(t, err)
			ra := session.NewSessionAuth(store, sess)

			req := httptest.NewRequest("GET", "/api/v1/pods", nil)
			req = req.WithContext(session.WithRequestAuth(req.Context(), ra))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, tc.upstreamStatus, rr.Code)

			_, err = store.Get(req.Context(), sess.ID())
			if tc.expectInvalidated {
				assert.ErrorIs(t, err, session.ErrNotFound, "session should have been invalidated")
			} else {
				assert.NoError(t, err, "session should have survived")
			}
		})
	}
}
