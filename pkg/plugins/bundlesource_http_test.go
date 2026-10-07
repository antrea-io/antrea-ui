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
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const testBundleURL = "http://bundles.plugins.svc:8080/bundles/abc"

func TestValidateBundleURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "http", url: testBundleURL},
		{name: "https with a query", url: "https://bundles.example.com/b?token=1"},
		{name: "empty", url: "", wantErr: true},
		{name: "relative", url: "/bundles/abc", wantErr: true},
		{name: "scheme-relative", url: "//bundles.example.com/b", wantErr: true},
		{name: "no host", url: "http:///bundles", wantErr: true},
		{name: "other scheme", url: "ftp://bundles.example.com/b", wantErr: true},
		{name: "file", url: "file:///etc/passwd", wantErr: true},
		{name: "credentials", url: "http://user:pass@bundles.example.com/b", wantErr: true},
		{name: "fragment", url: "http://bundles.example.com/b#x", wantErr: true},
		{name: "not a URL", url: "http://bundles.example.com/%zz", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateBundleURL(tt.url)
			if tt.wantErr {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "pass", "an error must not repeat the URL")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.url, got)
		})
	}
}

func TestValidateBundleSourceTransports(t *testing.T) {
	assert.Error(t, validateBundleSource(&apisv1.PluginBundleSource{}))
	assert.NoError(t, validateBundleSource(&apisv1.PluginBundleSource{HTTP: &apisv1.PluginBundleHTTP{}}))
	assert.Error(t, validateBundleSource(&apisv1.PluginBundleSource{
		HTTP:      &apisv1.PluginBundleHTTP{},
		APIServer: &apisv1.PluginBundleAPIServer{Path: testBundlePath},
	}))
}

func TestHTTPFetcher(t *testing.T) {
	respond := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: http.Header{}}
	}
	newFetcher := func(rt roundTripFunc) BundleFetcher {
		f := NewHTTPBundleFetcher()
		f.(*httpFetcher).client.Transport = rt
		return f
	}

	t.Run("success", func(t *testing.T) {
		var got *http.Request
		f := newFetcher(func(r *http.Request) (*http.Response, error) {
			got = r
			return respond(http.StatusOK, "zip-bytes"), nil
		})
		body, err := f.Fetch(t.Context(), testBundleURL)
		require.NoError(t, err)
		assert.Equal(t, "zip-bytes", readAll(t, body))
		assert.Equal(t, http.MethodGet, got.Method)
		assert.Equal(t, testBundleURL, got.URL.String())
		assert.Empty(t, got.Header.Get("Authorization"), "no credential may reach an arbitrary destination")
		assert.Empty(t, got.Header.Get("Cookie"))
	})

	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			f := newFetcher(func(*http.Request) (*http.Response, error) {
				resp := respond(status, "secret-from-the-server")
				resp.Header.Set("Location", "http://elsewhere.example/b")
				return resp, nil
			})
			_, err := f.Fetch(t.Context(), testBundleURL)
			require.Error(t, err)
			assert.ErrorContains(t, err, fmt.Sprint(status))
			assert.NotContains(t, err.Error(), "secret-from-the-server", "an error must not carry the response body")
		})
	}

	t.Run("error body is not read", func(t *testing.T) {
		body := &readTracker{}
		f := newFetcher(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: body, Header: http.Header{}}, nil
		})
		_, err := f.Fetch(t.Context(), testBundleURL)
		require.Error(t, err)
		assert.False(t, body.read)
		assert.True(t, body.closed)
	})

	t.Run("transport error does not repeat the URL", func(t *testing.T) {
		f := newFetcher(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
		_, err := f.Fetch(t.Context(), "https://bundles.example.com/b?token=hunter2")
		require.Error(t, err)
		assert.ErrorContains(t, err, "connection refused")
		assert.NotContains(t, err.Error(), "hunter2")
	})
}

// readTracker is a response body that records whether it was read or closed.
type readTracker struct{ read, closed bool }

func (b *readTracker) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *readTracker) Close() error             { b.closed = true; return nil }

// httpManifest is remoteManifest for a plugin whose bundleSource is "http".
func httpManifest(version string, bundle []byte) string {
	return fmt.Sprintf(`{"name":"pod-counter","version":%q,"entry":"index.js","bundleSha256":%q,"bundleSource":{"http":{}}}`,
		version, bundleDigest(bundle))
}

// httpConfigMap is a manifest-only plugin ConfigMap for a bundleSource "http" manifest, naming url
// as its bundle's location. It is signed by signer, unless that is nil.
func httpConfigMap(t *testing.T, signer *openpgp.Entity, manifestJSON, url string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-counter-cm", Namespace: "antrea-ui", ResourceVersion: manifestJSON + url},
		Data:       map[string]string{manifestFileName: manifestJSON},
	}
	if url != "" {
		cm.Data[bundleURLKey] = url
	}
	if signer != nil {
		cm.Data[openPGPSignatureFileName] = string(asc(sign(t, signer, []byte(manifestJSON)))[openPGPSignatureFileName])
	}
	return cm
}

// newHTTPTestRegistry is a Registry that downloads bundleSource "http" bundles through fetcher. A
// nil signer leaves signature verification disabled.
func newHTTPTestRegistry(t *testing.T, signer *openpgp.Entity, fetcher BundleFetcher, maxBundleBytes int64) *Registry {
	t.Helper()
	opts := Options{
		Logger:            testr.New(t),
		Namespace:         "antrea-ui",
		LabelSelector:     "ui.antrea.io/plugin=true",
		MaxBundleBytes:    maxBundleBytes,
		HTTPBundleFetcher: fetcher,
	}
	if signer != nil {
		opts.SignatureVerifiers = []SignatureVerifier{trustedOpenPGPKey(t, "trusted", signer)}
	}
	r := NewRegistry(opts)
	t.Cleanup(r.Close)
	return r
}

const testMaxBundleBytes = 10 << 20

func TestHTTPBundleLoads(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())

	for _, tt := range []struct {
		name       string
		signer     *openpgp.Entity
		verifiedBy string
	}{
		{name: "signed", signer: testSigner(), verifiedBy: "trusted"},
		// The request carries no credentials and the digest decides what is accepted, so no
		// signature verification being configured is not an obstacle, unlike for an API server.
		{name: "signature verification disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &fakeFetcher{body: bundle}
			r := newHTTPTestRegistry(t, tt.signer, fetcher, testMaxBundleBytes)

			require.Equal(t, upsertDone, r.handleUpsert(t.Context(), httpConfigMap(t, tt.signer, httpManifest("1.0.0", bundle), testBundleURL)))

			require.Len(t, r.Index(), 1)
			assert.Equal(t, []string{testBundleURL}, fetcher.requestPaths())
			file, _, ok := r.File("pod-counter", "index.js")
			require.True(t, ok)
			assert.Equal(t, "console.log('hi')", readAll(t, file))
			entry, _ := r.getConfigMapPlugin("pod-counter-cm")
			assert.Equal(t, tt.verifiedBy, entry.verifiedBy)
		})
	}
}

// The URL is outside the signed manifest, so changing it is a ConfigMap edit that needs no key. A
// bundle already extracted for the digest is not downloaded again for it.
func TestHTTPBundleURLChange(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	fetcher := &fakeFetcher{body: bundle}
	r := newHTTPTestRegistry(t, testSigner(), fetcher, testMaxBundleBytes)
	manifest := httpManifest("1.0.0", bundle)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), httpConfigMap(t, testSigner(), manifest, testBundleURL)))

	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), httpConfigMap(t, testSigner(), manifest, "http://other.plugins.svc/bundles/abc")))
	assert.Equal(t, []string{testBundleURL}, fetcher.requestPaths())

	// A different digest, though, is fetched from the new URL.
	other := buildZip(t, map[string]string{"index.js": "other"})
	fetcher.set(other, nil)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), httpConfigMap(t, testSigner(), httpManifest("2.0.0", other), "http://other.plugins.svc/bundles/def")))
	assert.Equal(t, []string{testBundleURL, "http://other.plugins.svc/bundles/def"}, fetcher.requestPaths())
}

func TestHTTPBundleRejections(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	otherBundle := buildZip(t, map[string]string{"index.js": "other"})
	manifest := httpManifest("1.0.0", bundle)

	tests := []struct {
		name string
		// cm builds the ConfigMap; nil uses manifest, signed, with testBundleURL.
		cm        func(t *testing.T) *corev1.ConfigMap
		unsigned  bool
		maxBytes  int64
		served    []byte
		fetchErr  error
		want      upsertResult
		wantFetch bool
	}{
		{name: "download fails", fetchErr: errors.New("503"), want: upsertRetryAlways, wantFetch: true},
		{name: "served bundle does not match the digest", served: otherBundle, want: upsertRetryAlways, wantFetch: true},
		{name: "bundle over the size limit", maxBytes: 10, want: upsertRejected, wantFetch: true},
		{name: "no size limit configured", maxBytes: -1, want: upsertRejected},
		{
			name: "no URL",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return httpConfigMap(t, testSigner(), manifest, "")
			},
			want: upsertRejected,
		},
		{
			name: "URL that is not http(s)",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return httpConfigMap(t, testSigner(), manifest, "file:///etc/passwd")
			},
			want: upsertRejected,
		},
		{
			name: "no signature",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return httpConfigMap(t, nil, manifest, testBundleURL)
			},
			want: upsertRejected,
		},
		{
			name: "signature over different bytes",
			cm: func(t *testing.T) *corev1.ConfigMap {
				cm := httpConfigMap(t, testSigner(), manifest, testBundleURL)
				cm.Data[openPGPSignatureFileName] = string(asc(sign(t, testSigner(), []byte(httpManifest("2.0.0", bundle))))[openPGPSignatureFileName])
				return cm
			},
			want: upsertRejected,
		},
		{
			name: "no digest",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return httpConfigMap(t, testSigner(), `{"name":"pod-counter","entry":"index.js","bundleSource":{"http":{}}}`, testBundleURL)
			},
			want: upsertRejected,
		},
		{
			name: "both transports",
			cm: func(t *testing.T) *corev1.ConfigMap {
				m := fmt.Sprintf(`{"name":"pod-counter","entry":"index.js","bundleSha256":%q,"bundleSource":{"http":{},"apiServer":{"path":%q}}}`, bundleDigest(bundle), testBundlePath)
				return httpConfigMap(t, testSigner(), m, testBundleURL)
			},
			want: upsertRejected,
		},
		{
			name: "entry missing from the bundle",
			cm: func(t *testing.T) *corev1.ConfigMap {
				m := fmt.Sprintf(`{"name":"pod-counter","entry":"missing.js","bundleSha256":%q,"bundleSource":{"http":{}}}`, bundleDigest(bundle))
				return httpConfigMap(t, testSigner(), m, testBundleURL)
			},
			want:      upsertRejected,
			wantFetch: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			served := tt.served
			if served == nil {
				served = bundle
			}
			maxBytes := tt.maxBytes
			switch maxBytes {
			case 0:
				maxBytes = testMaxBundleBytes
			case -1:
				maxBytes = 0
			}
			fetcher := &fakeFetcher{body: served, err: tt.fetchErr}
			r := newHTTPTestRegistry(t, testSigner(), fetcher, maxBytes)
			cm := httpConfigMap(t, testSigner(), manifest, testBundleURL)
			if tt.cm != nil {
				cm = tt.cm(t)
			}

			assert.Equal(t, tt.want, r.handleUpsert(t.Context(), cm))

			assert.Empty(t, r.Index())
			assert.Equal(t, tt.wantFetch, len(fetcher.requestPaths()) > 0, "requests: %v", fetcher.requestPaths())
		})
	}
}

// Both transports can be in use at once, each through its own fetcher: the apiServer one is never
// handed a URL, nor the http one a path.
func TestRemoteBundleTransportsUseTheirOwnFetcher(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	apiServer := &fakeFetcher{body: bundle}
	httpFetcher := &fakeFetcher{body: bundle}
	r := newRemoteTestRegistry(t, testSigner(), apiServer, testMaxBundleBytes)
	r.httpBundleFetcher = httpFetcher

	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), httpConfigMap(t, testSigner(), httpManifest("1.0.0", bundle), testBundleURL)))
	assert.Equal(t, []string{testBundleURL}, httpFetcher.requestPaths())
	assert.Empty(t, apiServer.requestPaths())

	other := buildZip(t, map[string]string{"index.js": "other"})
	apiServer.set(other, nil)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("2.0.0", other))))
	assert.Equal(t, []string{testBundlePath + "/" + bundleDigest(other) + "/download"}, apiServer.requestPaths())
	assert.Len(t, httpFetcher.requestPaths(), 1)
}
