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

	"antrea.io/antrea-ui/pkg/auth/session"
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

// Write requests are forwarded too, with their method: there is no verb allowlist either, RBAC
// decides whether the user may create, update, patch or delete the resource.
func TestK8sProxyMethods(t *testing.T) {
	const path = "/apis/crd.antrea.io/v1beta1/clusternetworkpolicies/test-acnp"
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			ts := newTestServer(t)
			req := httptest.NewRequest(method, "/api/v1/k8s"+path, nil)
			ts.authorizeRequest(req)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, http.StatusOK, rr.Code)
			require.NotNil(t, ts.k8sProxyHandler.request)
			assert.Equal(t, method, ts.k8sProxyHandler.request.Method)
			assert.Equal(t, path, ts.k8sProxyHandler.request.URL.Path)
		})
	}
}

// The proxy subresources of Services, Pods and Nodes relay a workload's own responses onto the UI's
// origin, so they are refused, for every method, and however the path is spelled. Paths that only
// look similar must still go through.
func TestK8sProxyProxySubresources(t *testing.T) {
	testCases := []struct {
		name         string
		path         string
		expectedPath string // the path forwarded to the API server, when the request is forwarded
	}{
		{name: "service proxy", path: "/api/v1/namespaces/ns/services/svc/proxy"},
		{name: "service proxy with port", path: "/api/v1/namespaces/ns/services/https:svc:443/proxy/"},
		{name: "service proxy sub-path", path: "/api/v1/namespaces/ns/services/svc/proxy/index.html"},
		{name: "pod proxy", path: "/api/v1/namespaces/ns/pods/pod/proxy/"},
		{name: "node proxy", path: "/api/v1/nodes/node/proxy/metrics"},
		{name: "doubled slashes", path: "/api//v1/namespaces/ns/services/svc//proxy/"},
		{name: "dot segments", path: "/api/v1/namespaces/ns/services/svc/x/../proxy/"},
		{name: "percent-encoded", path: "/api/v1/namespaces/ns/services/svc/%70roxy/"},
		{name: "pod named proxy", path: "/api/v1/namespaces/ns/pods/proxy", expectedPath: "/api/v1/namespaces/ns/pods/proxy"},
		{name: "pod log", path: "/api/v1/namespaces/ns/pods/pod/log", expectedPath: "/api/v1/namespaces/ns/pods/pod/log"},
		{name: "node named proxy", path: "/api/v1/nodes/proxy", expectedPath: "/api/v1/nodes/proxy"},
		{name: "dot segments forwarded clean", path: "/api/v1/x/../pods", expectedPath: "/api/v1/pods"},
	}
	for _, tc := range testCases {
		for _, method := range []string{"GET", "POST"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				ts := newTestServer(t)
				req := httptest.NewRequest(method, "/api/v1/k8s"+tc.path, nil)
				ts.authorizeRequest(req)
				rr := httptest.NewRecorder()
				ts.router.ServeHTTP(rr, req)
				if tc.expectedPath == "" {
					assert.Equal(t, http.StatusForbidden, rr.Code)
					assert.Nil(t, ts.k8sProxyHandler.request, "request should not have been proxied")
					return
				}
				assert.Equal(t, http.StatusOK, rr.Code)
				require.NotNil(t, ts.k8sProxyHandler.request)
				assert.Equal(t, tc.expectedPath, ts.k8sProxyHandler.request.URL.EscapedPath())
			})
		}
	}
}

// A write is where a forged cross-origin request does damage, so check that the CSRF gate of the
// authentication middleware stops one before it reaches the proxy.
func TestK8sProxyRejectsCrossSiteWrite(t *testing.T) {
	ts := newTestServer(t)
	req := httptest.NewRequest("DELETE", "/api/v1/k8s/api/v1/namespaces/default/pods/test-pod", nil)
	req.AddCookie(ts.newSession(session.ModeAdmin))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Nil(t, ts.k8sProxyHandler.request, "request should not have been proxied")
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
