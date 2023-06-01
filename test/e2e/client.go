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
	"time"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
)

type AuthProvider struct {
	refreshCookie *http.Cookie
}

func (p *AuthProvider) getAccessToken(ctx context.Context, host string) (string, error) {
	login := func(ctx context.Context) (*http.Response, error) {
		return Request(ctx, host, "POST", "auth/login", nil, func(req *http.Request) {
			req.SetBasicAuth("admin", "admin") // default credentials
		})
	}

	refreshToken := func(ctx context.Context) (*http.Response, error) {
		return Request(ctx, host, "GET", "auth/refresh_token", nil, func(req *http.Request) {
			req.AddCookie(p.refreshCookie)
		})
	}

	parseToken := func(body []byte) (string, error) {
		var data apisv1alpha1.Token
		if err := json.Unmarshal(body, &data); err != nil {
			return "", fmt.Errorf("invalid response body format: %w", err)
		}
		return data.AccessToken, nil
	}

	token, err := func() (string, error) {
		if p.refreshCookie == nil {
			timer := time.NewTimer(0)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-timer.C:
					resp, err := login(ctx)
					if err != nil {
						return "", err
					}
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						return "", err
					}
					if resp.StatusCode == http.StatusOK {
						p.refreshCookie = resp.Cookies()[0]
						token, err := parseToken(body)
						if err != nil {
							return "", err
						}
						return token, nil
					} else if resp.StatusCode == http.StatusTooManyRequests {
						timer.Reset(100 * time.Millisecond)
						continue
					} else {
						return "", fmt.Errorf("failed to log in: %w", err)
					}
				}
			}
		} else {
			resp, err := refreshToken(ctx)
			if err != nil {
				return "", err
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return "", err
			}
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("failed to refresh token: %w", err)
			}
			token, err := parseToken(body)
			if err != nil {
				return "", err
			}
			return token, nil
		}
	}()

	return token, err
}

var authProvider = &AuthProvider{}

func Request(ctx context.Context, host string, method string, path string, body io.Reader, mutators ...func(req *http.Request)) (*http.Response, error) {
	url := url.URL{
		Scheme: "http",
		Host:   host,
		Path:   path,
	}
	req, err := http.NewRequestWithContext(ctx, method, url.String(), body)
	if err != nil {
		return nil, err
	}
	for _, m := range mutators {
		m(req)
	}
	return http.DefaultClient.Do(req)
}

func GetAccessToken(ctx context.Context, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return authProvider.getAccessToken(ctx, host)
}
