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

package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// bundleURLKey is the ConfigMap data key holding the URL of a bundleSource.http plugin's bundle.
// It is install-specific, so it is not in the signed manifest: changing it needs no signing key,
// and cannot change which bundle bytes are accepted, as the digest is.
const bundleURLKey = "bundleURL"

// bundleDownloadTimeout bounds a single bundle download, connection to last byte. Longer than
// kube-apiserver's own default request timeout (60s), which is what actually bounds a proxied
// download; this only stops a stalled connection from holding the worker forever.
const bundleDownloadTimeout = 2 * time.Minute

// BundleFetcher downloads a plugin's bundle.zip from the location a manifest's bundleSource
// names. Fetch takes the target the registry has already built from a validated manifest - a
// server-relative request path for bundleSource.apiServer, an absolute URL for bundleSource.http -
// and returns the response body of a successful (2xx) response; any other outcome is an error.
// The registry retries every error the same way, so the implementation doesn't need to classify
// them. An error never carries the response body, which comes from a server the backend does not
// control.
type BundleFetcher interface {
	Fetch(ctx context.Context, target string) (io.ReadCloser, error)
}

// apiServerFetcher is the BundleFetcher for bundleSource.apiServer: authenticated GETs to the
// Kubernetes API server the backend is configured with, with the backend's own credentials.
type apiServerFetcher struct {
	client *http.Client
	base   *url.URL
}

// NewAPIServerBundleFetcher returns a BundleFetcher that requests bundles from the API server
// config points at, authenticating with config's credentials (the antrea-ui ServiceAccount's,
// not those of any logged-in user). The host is fixed here: a manifest can only choose the path.
func NewAPIServerBundleFetcher(config *rest.Config) (BundleFetcher, error) {
	client, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	// A redirect would let the API server (or something proxied behind it) send the
	// ServiceAccount-authenticated request elsewhere, and none is expected.
	client.CheckRedirect = noRedirect
	hasCA := len(config.CAData) > 0 || len(config.CAFile) > 0
	base, _, err := rest.DefaultServerURL(config.Host, config.APIPath, schema.GroupVersion{}, hasCA || config.Insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to parse API server address %q: %w", config.Host, err)
	}
	return &apiServerFetcher{client: client, base: base}, nil
}

func (f *apiServerFetcher) Fetch(ctx context.Context, requestPath string) (io.ReadCloser, error) {
	u := *f.base
	// Behind the base path, which an API server reached through a proxy can have.
	u.Path = strings.TrimSuffix(f.base.Path, "/") + requestPath
	return fetchBundle(ctx, f.client, u.String())
}

// httpFetcher is the BundleFetcher for bundleSource.http: unauthenticated GETs to the URL the
// plugin's ConfigMap names.
type httpFetcher struct {
	client *http.Client
}

// NewHTTPBundleFetcher returns a BundleFetcher that requests bundles from absolute HTTP(S) URLs.
// Its client carries no credentials of any kind - nothing the backend holds is sent along - and
// follows no redirects.
func NewHTTPBundleFetcher() BundleFetcher {
	return &httpFetcher{client: &http.Client{CheckRedirect: noRedirect}}
}

func (f *httpFetcher) Fetch(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	return fetchBundle(ctx, f.client, rawURL)
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// fetchBundle GETs rawURL with client. A response other than 2xx is an error carrying only its
// status: the body is not read, since a server the backend doesn't control could put anything in
// it, and the registry logs errors. Nor does an error carry the URL, which may hold a secret.
func fetchBundle(ctx context.Context, client *http.Client, rawURL string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, bundleDownloadTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, errors.New("failed to build the request")
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("unexpected response status %q", resp.Status)
	}
	return &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}, nil
}

// cancelOnClose releases a request's timeout context once its response body is closed, rather
// than as soon as Fetch returns, which would abort the body read.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

// validateBundleSource checks a manifest's bundleSource, which must name exactly one transport.
// An apiServer source must have a path that can only address the API server's own resources; an
// http source has nothing to check here, as its URL is not part of the manifest (see
// validateBundleURL).
func validateBundleSource(source *apisv1.PluginBundleSource) error {
	switch {
	case source.APIServer != nil && source.HTTP != nil:
		return errors.New("manifest's 'bundleSource' must set only one of 'apiServer' and 'http'")
	case source.APIServer != nil:
		return validateBundleAPIServerPath(source.APIServer.Path)
	case source.HTTP != nil:
		return nil
	default:
		return errors.New("manifest's 'bundleSource' must set 'apiServer' or 'http'")
	}
}

// validateBundleURL checks the URL a bundleSource.http plugin's ConfigMap names: absolute, HTTP or
// HTTPS, with a host and no credentials or fragment. The error never repeats the URL, which
// might hold a secret.
func validateBundleURL(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("a manifest with 'bundleSource.http' needs the ConfigMap key %q", bundleURLKey)
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", fmt.Errorf("ConfigMap key %q is not a valid URL", bundleURLKey)
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("ConfigMap key %q must be an absolute http or https URL", bundleURLKey)
	case u.Host == "" || u.Hostname() == "":
		return "", fmt.Errorf("ConfigMap key %q must name a host", bundleURLKey)
	case u.User != nil:
		return "", fmt.Errorf("ConfigMap key %q must not carry credentials", bundleURLKey)
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "", fmt.Errorf("ConfigMap key %q must not carry a fragment", bundleURLKey)
	}
	return raw, nil
}

func validateBundleAPIServerPath(p string) error {
	if p == "" {
		return errors.New("manifest's 'bundleSource.apiServer.path' is missing")
	}
	// "//host/x" is a scheme-relative URL: left to URL parsing it would name another host.
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return fmt.Errorf("manifest's 'bundleSource.apiServer.path' %q must be a server-relative path starting with a single '/'", p)
	}
	if strings.ContainsAny(p, "?#%\\") {
		return fmt.Errorf("manifest's 'bundleSource.apiServer.path' %q must not contain '?', '#', '%%' or '\\'", p)
	}
	for seg := range strings.SplitSeq(strings.Trim(p, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("manifest's 'bundleSource.apiServer.path' %q must not contain empty, '.' or '..' segments", p)
		}
	}
	return nil
}

// bundleRequestPath builds the path a bundle is requested from: the manifest's path, then the
// digest as the resource name (lowercase, as a Kubernetes resource name must be), then the
// "download" subresource. The digest is written only once - by the backend, never the manifest.
func bundleRequestPath(apiServerPath, bundleSha256 string) string {
	return strings.TrimSuffix(apiServerPath, "/") + "/" + strings.ToLower(bundleSha256) + "/download"
}
