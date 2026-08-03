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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// AuthProvider obtains a Kubernetes token for the antrea-ui-admin ServiceAccount and presents it
// to the UI backend with the Authorization: Bearer fallback.
//
// This is deliberately not the browser flow: it skips the session cookie entirely, so the tests do
// not have to maintain a cookie jar or track session expiry, and it exercises the same code path a
// script or a controller would use.
type AuthProvider struct {
	mutex  sync.Mutex
	token  string
	expiry time.Time
}

const (
	authProviderSAName   = "antrea-ui-admin"
	authProviderTokenTTL = 1 * time.Hour
)

func (p *AuthProvider) getAccessToken(ctx context.Context) (string, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	// Renew well before expiry, so a long test run never trips over it mid-request.
	if p.token != "" && time.Now().Add(5*time.Minute).Before(p.expiry) {
		return p.token, nil
	}
	tr, err := k8sClient.CoreV1().ServiceAccounts(antreaNamespace).CreateToken(ctx, authProviderSAName, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To(int64(authProviderTokenTTL / time.Second)),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to request a token for ServiceAccount %s/%s: %w", antreaNamespace, authProviderSAName, err)
	}
	p.token = tr.Status.Token
	p.expiry = tr.Status.ExpirationTimestamp.Time
	return p.token, nil
}

var authProvider = &AuthProvider{}

func RequestURLWithClient(
	ctx context.Context,
	client *http.Client,
	method string,
	url *url.URL,
	body io.Reader,
	mutators ...func(req *http.Request),
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url.String(), body)
	if err != nil {
		return nil, err
	}
	for _, m := range mutators {
		m(req)
	}
	return client.Do(req)
}

func RequestWithClient(
	ctx context.Context,
	client *http.Client,
	host string,
	method string,
	path string,
	body io.Reader,
	mutators ...func(req *http.Request),
) (*http.Response, error) {
	url := &url.URL{
		Scheme: "http",
		Host:   host,
		Path:   path,
	}
	return RequestURLWithClient(ctx, client, method, url, body, mutators...)
}

func Request(
	ctx context.Context,
	host string,
	method string,
	path string,
	body io.Reader,
	mutators ...func(req *http.Request),
) (*http.Response, error) {
	return RequestWithClient(ctx, http.DefaultClient, host, method, path, body, mutators...)
}

// GetAccessToken returns a Kubernetes bearer token that the UI backend accepts on the
// Authorization header.
func GetAccessToken(ctx context.Context, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return authProvider.getAccessToken(ctx)
}

func GetFrontendSettings(ctx context.Context) (*apisv1.FrontendSettings, error) {
	resp, err := Request(ctx, host, "GET", "api/v1/settings", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status when retrieving settings: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var settings apisv1.FrontendSettings
	if err := json.Unmarshal(body, &settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// setSameOriginMutator marks a request as same-origin, the way a browser would.
//
// Cookie-authenticated requests go through the backend's CSRF gate, which requires either
// Sec-Fetch-Site: same-origin or a matching Origin header. Requests that authenticate with the
// Authorization header instead are exempt (a browser cannot attach one cross-origin without CORS),
// which is why the token-based tests do not need this.
func setSameOriginMutator(req *http.Request) {
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}
