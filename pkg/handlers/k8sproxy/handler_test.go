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
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"antrea.io/antrea-ui/pkg/auth/session"
)

// staticTransport stands in for k8s.ClientFactory.TransportForRequest.
func staticTransport(rt http.RoundTripper) func(*http.Request) (http.RoundTripper, error) {
	return func(*http.Request) (http.RoundTripper, error) { return rt, nil }
}

func TestK8sProxyHandler(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	logger := testr.New(t)
	serverURL, err := url.Parse(ts.URL)
	require.NoError(t, err)
	h := NewK8sProxyHandler(logger, serverURL, staticTransport(http.DefaultTransport))

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
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.upstreamStatus)
			}))
			defer ts.Close()

			serverURL, err := url.Parse(ts.URL)
			require.NoError(t, err)
			h := NewK8sProxyHandler(testr.New(t), serverURL, staticTransport(http.DefaultTransport))

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
