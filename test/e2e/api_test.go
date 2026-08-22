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

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func TestLoginRateLimiting(t *testing.T) {
	ctx := t.Context()
	badLogin := func() int {
		resp, err := Request(ctx, host, "POST", "auth/login", nil, func(req *http.Request) {
			req.SetBasicAuth("admin", "bad") // invalid password
		})
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// reset rate limiting for login API
	t.Cleanup(func() { time.Sleep(1 * time.Second) })

	require.Equal(t, http.StatusUnauthorized, badLogin())
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, http.StatusTooManyRequests, badLogin())
	time.Sleep(1 * time.Second)
	require.Equal(t, http.StatusUnauthorized, badLogin())
}

func setAccessTokenMutator(token string) func(req *http.Request) {
	return func(req *http.Request) {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}

func getResponseBody[T any](resp *http.Response, data *T) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, data)
}

func TestAPI(t *testing.T) {
	ctx := t.Context()
	token, err := GetAccessToken(ctx, host)
	require.NoError(t, err)
	t.Logf("Obtained access token to UI")

	t.Run("k8s", func(t *testing.T) {
		t.Run("antreaagentinfos", func(t *testing.T) {
			resp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos", nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			var data metav1.PartialObjectMetadataList
			require.NoError(t, getResponseBody(resp, &data))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.NotEmpty(t, data.Items)
		})
		t.Run("antreacontrollerinfos", func(t *testing.T) {
			resp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller", nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			var data metav1.PartialObjectMetadata
			require.NoError(t, getResponseBody(resp, &data))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "antrea-controller", data.Name)
		})
	})

	t.Run("featuregates", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "api/v1/featuregates", nil, setAccessTokenMutator(token))
		require.NoError(t, err)
		var featureGates []any
		require.NoError(t, getResponseBody(resp, &featureGates))
		assert.NotEmpty(t, featureGates)
	})

	t.Run("traceflow", func(t *testing.T) {
		// set-up: we create 2 Pods which we can use for a simple Traceflow request
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, ns)
		_, pods, err := createTestDeployment(ctx, ns, "tf", 2)
		require.NoError(t, err)
		require.Len(t, pods, 2)

		tf := map[string]interface{}{
			"spec": map[string]interface{}{
				"source": map[string]interface{}{
					"namespace": ns,
					"pod":       pods[0].Name,
				},
				"destination": map[string]interface{}{
					"namespace": ns,
					"pod":       pods[1].Name,
				},
			},
		}
		b, err := json.Marshal(tf)
		require.NoError(t, err)

		// we assume that rate-limiting won't be an issue for this test
		resp, err := Request(ctx, host, "POST", "api/v1/traceflow", bytes.NewBuffer(b), setAccessTokenMutator(token))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		url, err := resp.Location()
		require.NoError(t, err)
		reqURI := url.RequestURI()
		defer func() {
			resp, err := Request(ctx, host, "DELETE", reqURI, nil, setAccessTokenMutator(token))
			if !assert.NoError(t, err) {
				return
			}
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "Failed to delete Traceflow")
		}()

		statusURI := reqURI + "/status"

		require.Eventually(t, func() bool {
			resp, err := Request(ctx, host, "GET", statusURI, nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			// when the Traceflow completes, there will be an automatic redirect to the result
			return strings.HasSuffix(resp.Request.URL.Path, "/result")
		}, 30*time.Second, 1*time.Second)
	})
}

func TestAPIUnauthorized(t *testing.T) {
	ctx := t.Context()
	testCases := []struct {
		path   string
		method string
	}{
		{
			path:   "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos",
			method: "GET",
		},
		{
			path:   "api/v1/traceflow",
			method: "POST",
		},
		{
			path:   "api/v1/account/password",
			method: "PUT",
		},
		{
			path:   "api/v1/access-summary",
			method: "GET",
		},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			resp, err := Request(ctx, host, tc.method, tc.path, nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

// The Authorization: Bearer fallback is how a non-browser client (a script, this test suite)
// authenticates: it presents a Kubernetes token directly, with no session and no cookie.
func TestSessionEndpoint(t *testing.T) {
	ctx := t.Context()

	t.Run("anonymous", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "auth/session", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		// 401 is the "not logged in" answer the login page expects on a fresh visit.
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("bearer token", func(t *testing.T) {
		token, err := GetAccessToken(ctx, host)
		require.NoError(t, err)
		resp, err := Request(ctx, host, "GET", "auth/session", nil, setAccessTokenMutator(token))
		require.NoError(t, err)
		var info apisv1.SessionInfo
		require.NoError(t, getResponseBody(resp, &info))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, info.Authenticated)
		assert.Equal(t, "token", info.Mode)
	})
}

// Logging in with a pasted Kubernetes token creates a real session, and a bad token is rejected
// at login rather than on the first page load.
func TestLoginWithToken(t *testing.T) {
	ctx := t.Context()

	login := func(token string) (*http.Response, error) {
		body, err := json.Marshal(apisv1.LoginTokenRequest{Token: token})
		if err != nil {
			return nil, err
		}
		return Request(ctx, host, "POST", "auth/login/token", bytes.NewReader(body), func(req *http.Request) {
			req.Header.Set("Content-Type", "application/json")
		})
	}

	// Both login endpoints are rate-limited, so back off until we get a real answer.
	loginWithRetry := func(t *testing.T, token string) *http.Response {
		t.Helper()
		var resp *http.Response
		require.Eventually(t, func() bool {
			var err error
			resp, err = login(token)
			if err != nil {
				return false
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				resp.Body.Close()
				return false
			}
			return true
		}, 10*time.Second, 200*time.Millisecond)
		return resp
	}

	t.Run("valid token", func(t *testing.T) {
		token, err := GetAccessToken(ctx, host)
		require.NoError(t, err)
		resp := loginWithRetry(t, token)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var sessionCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "antrea-ui-session" {
				sessionCookie = c
			}
		}
		require.NotNil(t, sessionCookie, "expected a session cookie")
		assert.Equal(t, "/", sessionCookie.Path)
		assert.True(t, sessionCookie.HttpOnly)

		// The cookie should authenticate an API request, with the same-origin signal a browser
		// would send.
		apiResp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos", nil,
			func(req *http.Request) {
				req.AddCookie(sessionCookie)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
			})
		require.NoError(t, err)
		defer apiResp.Body.Close()
		assert.Equal(t, http.StatusOK, apiResp.StatusCode)
	})

	t.Run("garbage token is rejected at login", func(t *testing.T) {
		resp := loginWithRetry(t, "not-a-real-kubernetes-token")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// A cookie-authenticated request from another origin must be rejected: SameSite=Strict is the
// primary CSRF defence, and this origin check is the second layer behind it.
func TestCSRFRejectsForeignOrigin(t *testing.T) {
	ctx := t.Context()
	token, err := GetAccessToken(ctx, host)
	require.NoError(t, err)

	body, err := json.Marshal(apisv1.LoginTokenRequest{Token: token})
	require.NoError(t, err)
	var loginResp *http.Response
	require.Eventually(t, func() bool {
		loginResp, err = Request(ctx, host, "POST", "auth/login/token", bytes.NewReader(body), func(req *http.Request) {
			req.Header.Set("Content-Type", "application/json")
		})
		if err != nil {
			return false
		}
		if loginResp.StatusCode == http.StatusTooManyRequests {
			loginResp.Body.Close()
			return false
		}
		return true
	}, 10*time.Second, 200*time.Millisecond)
	defer loginResp.Body.Close()
	require.Equal(t, http.StatusOK, loginResp.StatusCode)
	cookies := loginResp.Cookies()

	resp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos", nil,
		func(req *http.Request) {
			for _, c := range cookies {
				req.AddCookie(c)
			}
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("Origin", "https://evil.example.com")
		})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func skipIfKubeconfigDisabled(t *testing.T) {
	if !settings.Auth.KubeconfigEnabled {
		t.Skip("Skipping test as kubeconfig authentication is disabled")
	}
}

// buildKubeconfig produces a self-contained kubeconfig carrying token as the current context's
// credential, the way `kubectl config view --raw --minify` would.
func buildKubeconfig(t *testing.T, token string) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["test"] = &clientcmdapi.Cluster{
		Server:                   k8sRESTConfig.Host,
		CertificateAuthorityData: k8sRESTConfig.CAData,
		InsecureSkipTLSVerify:    k8sRESTConfig.CAData == nil,
	}
	cfg.AuthInfos["test-user"] = &clientcmdapi.AuthInfo{Token: token}
	cfg.Contexts["test"] = &clientcmdapi.Context{Cluster: "test", AuthInfo: "test-user"}
	cfg.CurrentContext = "test"
	b, err := clientcmd.Write(*cfg)
	require.NoError(t, err)
	return string(b)
}

// TestLoginWithKubeconfig covers login mode 3, where the user uploads their own kubeconfig and the
// backend extracts the current context's credential from it.
//
// The rejection cases matter as much as the happy path: a kubeconfig can ask for a program to be
// run (exec plugins, auth-provider) or point at files that only exist on the user's machine.
// Neither can work server-side, and the exec case must never be attempted at all, since it would
// mean running a user-supplied command inside the antrea-ui Pod.
func TestLoginWithKubeconfig(t *testing.T) {
	ctx := t.Context()
	skipIfKubeconfigDisabled(t)

	login := func(kubeconfig string) (*http.Response, error) {
		body, err := json.Marshal(apisv1.LoginKubeconfigRequest{Kubeconfig: kubeconfig})
		if err != nil {
			return nil, err
		}
		return Request(ctx, host, "POST", "auth/login/kubeconfig", bytes.NewReader(body), func(req *http.Request) {
			req.Header.Set("Content-Type", "application/json")
		})
	}

	// The login endpoints are rate-limited, so back off until we get a real answer.
	loginWithRetry := func(t *testing.T, kubeconfig string) *http.Response {
		t.Helper()
		var resp *http.Response
		require.Eventually(t, func() bool {
			var err error
			resp, err = login(kubeconfig)
			if err != nil {
				return false
			}
			if resp.StatusCode == http.StatusTooManyRequests {
				resp.Body.Close()
				return false
			}
			return true
		}, 10*time.Second, 200*time.Millisecond)
		return resp
	}

	t.Run("valid kubeconfig", func(t *testing.T) {
		token, err := GetAccessToken(ctx, host)
		require.NoError(t, err)
		resp := loginWithRetry(t, buildKubeconfig(t, token))
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var sessionCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == "antrea-ui-session" {
				sessionCookie = c
			}
		}
		require.NotNil(t, sessionCookie, "expected a session cookie")
		assert.True(t, sessionCookie.HttpOnly)

		// The session reports the mode it was created with, and the identity the API server
		// resolved from the credential - not anything read out of the kubeconfig.
		sessionResp, err := Request(ctx, host, "GET", "auth/session", nil, func(req *http.Request) {
			req.AddCookie(sessionCookie)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		})
		require.NoError(t, err)
		defer sessionResp.Body.Close()
		require.Equal(t, http.StatusOK, sessionResp.StatusCode)
		var sessionInfo apisv1.SessionInfo
		require.NoError(t, getResponseBody(sessionResp, &sessionInfo))
		assert.True(t, sessionInfo.Authenticated)
		assert.Equal(t, "kubeconfig", sessionInfo.Mode)
		assert.Equal(t, "system:serviceaccount:kube-system:antrea-ui-admin", sessionInfo.Username)

		// And the cookie authenticates a real Kubernetes call.
		apiResp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos", nil,
			func(req *http.Request) {
				req.AddCookie(sessionCookie)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
			})
		require.NoError(t, err)
		defer apiResp.Body.Close()
		assert.Equal(t, http.StatusOK, apiResp.StatusCode)
	})

	t.Run("exec credential plugin is rejected", func(t *testing.T) {
		cfg := clientcmdapi.NewConfig()
		cfg.AuthInfos["test-user"] = &clientcmdapi.AuthInfo{
			Exec: &clientcmdapi.ExecConfig{
				APIVersion: "client.authentication.k8s.io/v1",
				Command:    "touch",
				Args:       []string{"/tmp/antrea-ui-should-never-exist"},
			},
		}
		cfg.Contexts["test"] = &clientcmdapi.Context{Cluster: "test", AuthInfo: "test-user"}
		cfg.CurrentContext = "test"
		b, err := clientcmd.Write(*cfg)
		require.NoError(t, err)

		resp := loginWithRetry(t, string(b))
		defer resp.Body.Close()
		// 400, not 401: the kubeconfig is not something the backend can use at all, which is a
		// different problem from a credential Kubernetes rejected.
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "exec credential plugin")
	})

	t.Run("garbage is rejected", func(t *testing.T) {
		resp := loginWithRetry(t, "this is not a kubeconfig")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}
