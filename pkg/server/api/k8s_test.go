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
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// There is no longer a path allowlist in front of the proxy: RBAC is the guard, and it is the end
// user's own RBAC in every mode but the static admin password. These paths are all forwarded; what
// comes back is the API server's decision, not antrea-ui's.
func TestK8sProxyRequest(t *testing.T) {
	paths := []string{
		"/apis/crd.antrea.io/v1beta1/antreaagentinfos/node=A",
		"/apis/crd.antrea.io/v1beta1/antreacontrollerinfos",
		"/api/v1/pods",
		"/apis/networking.k8s.io/v1/networkpolicies",
		"/apis/crd.antrea.io/v1beta1/clusternetworkpolicies",
		// Previously blocked by the allowlist.
		"/apis/apps/v1/deployments",
	}

	for _, tcPath := range paths {
		t.Run(tcPath, func(t *testing.T) {
			ts := newTestServer(t)
			path, err := url.JoinPath("/api/v1/k8s", tcPath)
			require.NoError(t, err)
			req := httptest.NewRequest("GET", path, nil)
			ts.authorizeRequest(req)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, tcPath, ts.k8sProxyHandler.request.URL.Path)
		})
	}
}

// Whatever the client used to authenticate to antrea-ui - the Bearer fallback header, or the
// session cookie - must not reach the API server: the proxy authenticates the request itself. The
// session cookie matters most, being credential-equivalent for the whole UI: forwarding it would
// deposit it in the API server's audit log.
func TestK8sProxyStripsInboundCredentials(t *testing.T) {
	ts := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/k8s/api/v1/pods", nil)
	ts.authorizeRequest(req)
	req.Header.Set("Authorization", "Bearer client-supplied-token")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Empty(t, ts.k8sProxyHandler.request.Header.Get("Authorization"))
	assert.Empty(t, ts.k8sProxyHandler.request.Header.Get("Cookie"))
}
