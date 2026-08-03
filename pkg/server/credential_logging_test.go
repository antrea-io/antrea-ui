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

package server

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonglil/buflogr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

// Storing users' Kubernetes credentials server-side is only acceptable if they never leak out of
// memory. This drives a full login-and-use cycle at maximum verbosity and asserts that none of the
// credential material ever reaches the logger - not in a message, not in a structured field, not
// in an error.
func TestNoCredentialMaterialInLogs(t *testing.T) {
	const (
		saToken       = "eyJhbGciOiJSUzI1NiJ9.c2VjcmV0LXNlcnZpY2VhY2NvdW50LXRva2Vu.sig" // #nosec G101: not a real credential
		kubeconfigTok = "kubeconfig-embedded-token-value"
		badToken      = "rejected-token-that-must-not-be-logged"
	)

	var buf bytes.Buffer
	// buflogr records every level, so V(4) traces are captured too.
	logger := buflogr.NewWithBuffer(&buf)
	ts := newTestServerWithLogger(t, logger, enableKubeconfigAuth())

	// A credential the API server rejects: the failure path is the one most likely to echo the
	// offending value back into a log line.
	ts.k8sAPIServer.rejectTokens[badToken] = true
	rr := postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: badToken})
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	// A successful ServiceAccount-token login, then an authenticated API request with the
	// resulting session.
	rr = postJSON(ts, "/auth/login/token", apisv1.LoginTokenRequest{Token: saToken})
	require.Equal(t, http.StatusOK, rr.Code)
	cookie := sessionCookie(rr.Result())
	require.NotNil(t, cookie)

	req := httptest.NewRequest("GET", "/auth/session", nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	// A kubeconfig upload: the uploaded document must not be logged either.
	kubeconfig := testKubeconfig(t, "    token: "+kubeconfigTok+"\n")
	rr = postJSON(ts, "/auth/login/kubeconfig", apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
	require.Equal(t, http.StatusOK, rr.Code)

	// Logout, which reads the credential out of the session before zeroing it.
	req = httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(cookie)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	logs := buf.String()
	require.NotEmpty(t, logs, "the test would pass vacuously if nothing was logged at all")
	for _, secret := range []string{
		saToken,
		kubeconfigTok,
		badToken,
		kubeconfig,
		// The session ID is not credential material, but it is bearer-equivalent: anyone
		// holding it can act as the user until the session ends.
		cookie.Value,
		// Also check a base64 form, in case something dumps raw bytes through a JSON
		// encoder (which renders []byte as base64).
		base64.StdEncoding.EncodeToString([]byte(saToken)),
	} {
		assert.NotContains(t, logs, secret, "credential material must never reach the logger")
	}
}

// The admin password must not be logged either, even though it is antrea-ui's own credential
// rather than a Kubernetes one.
func TestNoAdminPasswordInLogs(t *testing.T) {
	const password = "s3cr3t-admin-password"

	var buf bytes.Buffer
	ts := newTestServerWithLogger(t, buflogr.NewWithBuffer(&buf))
	ts.passwordStore.EXPECT().Compare(gomock.Any(), []byte(password))

	req := httptest.NewRequest("POST", "/auth/login", nil)
	req.SetBasicAuth("admin", password)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	cookie := sessionCookie(rr.Result())
	require.NotNil(t, cookie)
	assert.Equal(t, cookieutils.SessionCookieName, cookie.Name)
	assert.NotContains(t, buf.String(), password)
}
