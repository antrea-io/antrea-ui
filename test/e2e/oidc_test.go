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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/publicsuffix"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	// dexAuthority is the host:port in the issuer URL, and therefore the name Dex's certificate is
	// issued for. It must match ci/e2e-oidc.sh and ci/kind-config.yml exactly - it is the "iss"
	// claim that Dex, the kube-apiserver and the Antrea UI backend all compare.
	dexAuthority = "dex.e2e:5556"
	// dexLocalAddr is where Dex is actually reachable from this process: ci/e2e-oidc.sh publishes
	// the container port on loopback. The host does not resolve "dex.e2e" and, on macOS, could not
	// route to the container's IP even if it did - hence the dialer rewrite below.
	dexLocalAddr = "127.0.0.1:5556"
	// dexCAFile is written by ci/e2e-oidc.sh (DEX_DIR).
	dexCAFile = "/tmp/antrea-ui-e2e-dex/ca.crt"

	// oidcTestUser is the identity Dex's mockCallback connector always returns. With
	// --oidc-username-claim=email the apiserver sees exactly this, with no prefix.
	oidcTestUser = "kilgore@kilgore.trout"
)

// skipIfOIDCDisabled skips when the deployment under test has no OIDC provider configured. In CI
// that provider is the Dex container from ci/e2e-oidc.sh.
func skipIfOIDCDisabled(t *testing.T) {
	if !settings.Auth.OIDCEnabled {
		t.Skip("Skipping test as OIDC is disabled")
	}
}

// oidcClient builds the HTTP client that stands in for the user's browser.
//
// It has to talk to two origins: Antrea UI on localhost (through the port forwarding set up in
// TestMain) and Dex on https://dex.e2e:5556. The dialer rewrites the latter to loopback, so the
// certificate is still verified under the name "dex.e2e" while the connection goes to the
// published container port. That indirection is what keeps this working on macOS, where the host
// cannot reach Docker container IPs directly.
//
// Redirects are followed, and the cookie jar keeps the session cookie the callback sets.
func oidcClient(t *testing.T) *http.Client {
	t.Helper()

	caPEM, err := os.ReadFile(dexCAFile)
	require.NoError(t, err, "cannot read the Dex CA at %s; did ci/e2e-oidc.sh start-dex run?", dexCAFile)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM), "no certificate found in %s", dexCAFile)

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	require.NoError(t, err, "failed to create cookie jar")

	dialer := &net.Dialer{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == dexAuthority {
			addr = dexLocalAddr
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return &http.Client{Jar: jar, Transport: transport}
}

// TestOIDC drives a full OIDC login against a real identity provider and then makes an
// authenticated Kubernetes API call with the resulting session.
//
// The API call is the point of the test. Antrea UI presents the id_token to the kube-apiserver as
// the user's own credential, so a login that "succeeds" proves very little on its own: what has to
// work is the whole chain, from Dex issuing the token, through the apiserver accepting it as
// kilgore@kilgore.trout, to RBAC authorizing that identity's group.
func TestOIDC(t *testing.T) {
	ctx := t.Context()
	skipIfOIDCDisabled(t)

	client := oidcClient(t)

	currentURL := url.URL{
		Scheme: "http",
		Host:   host,
		Path:   "summary",
	}
	loginURL := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   "auth/oauth2/login",
	}
	loginURL.RawQuery = url.Values{
		"redirect_url": []string{currentURL.String()},
	}.Encode()

	// One call, many redirects: Antrea UI -> Dex authorize -> the mock connector -> Dex callback ->
	// back to Antrea UI's /auth/oauth2/callback, which exchanges the code and sets the session
	// cookie. skipApprovalScreen in the Dex config is what keeps this free of any HTML to parse.
	resp, err := RequestURLWithClient(ctx, client, "GET", loginURL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// After a successful login the user lands back on the page they started from.
	expectedURL := currentURL
	expectedURL.RawQuery = url.Values{
		"auth_method": []string{"oidc"},
	}.Encode()
	assert.Equal(t, expectedURL.String(), resp.Request.URL.String())

	// The callback created a server-side session and set the cookie; the jar now carries it.
	// /auth/session is what the frontend probes at app start.
	resp, err = RequestWithClient(ctx, client, host, "GET", "auth/session", nil, setSameOriginMutator)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var sessionInfo apisv1.SessionInfo
	require.NoError(t, json.Unmarshal(body, &sessionInfo))
	assert.True(t, sessionInfo.Authenticated)
	assert.Equal(t, "oidc", sessionInfo.Mode)
	// The username is the one the apiserver resolved from the id_token at login, not a claim the
	// backend read for itself.
	assert.Equal(t, oidcTestUser, sessionInfo.Username)

	// The real test: a Kubernetes API call made as this user. It only succeeds if the apiserver
	// accepted the id_token and RBAC authorized the "authors" group that ci/e2e-oidc.sh bound.
	t.Run("authenticated K8s API call", func(t *testing.T) {
		resp, err := RequestWithClient(ctx, client, host, "GET",
			"api/v1/k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller", nil,
			setSameOriginMutator)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "response body: %s", string(b))
		var data map[string]any
		require.NoError(t, json.Unmarshal(b, &data))
		assert.Equal(t, "antrea-controller", data["metadata"].(map[string]any)["name"])
	})

	// A path the user's RBAC does NOT cover must come back 403, not 401: the credential is fine,
	// the permission is missing, and conflating the two would log the user out on every ordinary
	// permissions error.
	t.Run("forbidden K8s API call", func(t *testing.T) {
		resp, err := RequestWithClient(ctx, client, host, "GET",
			"api/v1/k8s/api/v1/namespaces/kube-system/secrets", nil, setSameOriginMutator)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	logoutURL := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   "auth/logout",
	}
	logoutURL.RawQuery = url.Values{
		"redirect_url": []string{currentURL.String()},
	}.Encode()

	resp, err = RequestURLWithClient(ctx, client, "GET", logoutURL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// After a successful logout, the user should have been redirected to the correct page.
	assert.Equal(t, currentURL.String(), resp.Request.URL.String())

	// And the session is genuinely gone, not just the redirect done.
	resp, err = RequestWithClient(ctx, client, host, "GET", "auth/session", nil, setSameOriginMutator)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
