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
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/publicsuffix"
)

func skipIfOIDCDisabled(t *testing.T) {
	if !settings.Auth.OIDCEnabled {
		t.Skip("Skipping test as OIDC is disabled")
	}
}

func TestOIDC(t *testing.T) {
	ctx := context.Background()
	skipIfOIDCDisabled(t)

	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	require.NoError(t, err, "failed to create cookie jar")
	client := &http.Client{
		Jar: jar,
	}

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

	resp, err := RequestURLWithClient(ctx, client, "GET", loginURL, nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// after a successful login, user should have been redirected to the correct page
	expectedURL := currentURL
	expectedURL.RawQuery = url.Values{
		"auth_method": []string{"oidc"},
	}.Encode()
	assert.Equal(t, expectedURL.String(), resp.Request.URL.String())

	resp, err = RequestWithClient(ctx, client, host, "GET", "auth/refresh_token", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_, err = parseAccessToken(body)
	require.NoError(t, err)

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
	// after a successful logout, user should have been redirected to the correct page
	assert.Equal(t, currentURL.String(), resp.Request.URL.String())
}
