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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
)

const testBundlePath = "/apis/ui.example.com/v1/uipluginbundles"

func TestValidateBundleAPIServerPath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{path: testBundlePath},
		{path: testBundlePath + "/"},
		{path: "/x"},
		{path: "", wantErr: true},
		{path: "apis/x", wantErr: true},
		{path: "//evil.example/apis/x", wantErr: true},
		{path: "https://evil.example/apis/x", wantErr: true},
		{path: "/apis/x?watch=true", wantErr: true},
		{path: "/apis/x#frag", wantErr: true},
		{path: "/apis/../api/v1/secrets", wantErr: true},
		{path: "/apis/./x", wantErr: true},
		{path: "/apis//x", wantErr: true},
		{path: "/apis/%2e%2e/x", wantErr: true},
		{path: `/apis\x`, wantErr: true},
		{path: "/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateBundleAPIServerPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestBundleRequestPath(t *testing.T) {
	digest := bundleDigest([]byte("x"))
	assert.Equal(t, testBundlePath+"/"+digest+"/download", bundleRequestPath(testBundlePath, digest))
	assert.Equal(t, testBundlePath+"/"+digest+"/download", bundleRequestPath(testBundlePath+"/", digest))
	// Resource names are lowercase.
	assert.Equal(t, testBundlePath+"/abc/download", bundleRequestPath(testBundlePath, "ABC"))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAPIServerFetcher(t *testing.T) {
	respond := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(bytes.NewReader([]byte(body))), Header: http.Header{}}
	}
	newFetcher := func(t *testing.T, rt roundTripFunc) BundleFetcher {
		t.Helper()
		f, err := NewAPIServerBundleFetcher(&rest.Config{Host: "https://apiserver.example:6443", BearerToken: "sa-token", Transport: rt})
		require.NoError(t, err)
		return f
	}

	t.Run("success", func(t *testing.T) {
		var got *http.Request
		f := newFetcher(t, func(r *http.Request) (*http.Response, error) {
			got = r
			return respond(http.StatusOK, "zip-bytes"), nil
		})
		body, err := f.Fetch(t.Context(), testBundlePath+"/abc/download")
		require.NoError(t, err)
		assert.Equal(t, "zip-bytes", readAll(t, body))
		assert.Equal(t, http.MethodGet, got.Method)
		assert.Equal(t, "https://apiserver.example:6443"+testBundlePath+"/abc/download", got.URL.String())
		assert.Equal(t, "Bearer sa-token", got.Header.Get("Authorization"))
	})

	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError, http.StatusFound} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			f := newFetcher(t, func(*http.Request) (*http.Response, error) { return respond(status, "nope"), nil })
			_, err := f.Fetch(t.Context(), testBundlePath+"/abc/download")
			assert.Error(t, err)
		})
	}

	t.Run("base path prefix", func(t *testing.T) {
		var got *http.Request
		f, err := NewAPIServerBundleFetcher(&rest.Config{Host: "https://proxy.example/k8s/clusters/c-x", Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			return respond(http.StatusOK, "zip-bytes"), nil
		})})
		require.NoError(t, err)
		_, err = f.Fetch(t.Context(), testBundlePath+"/abc/download")
		require.NoError(t, err)
		assert.Equal(t, "https://proxy.example/k8s/clusters/c-x"+testBundlePath+"/abc/download", got.URL.String())
	})

	t.Run("transport error", func(t *testing.T) {
		f := newFetcher(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })
		_, err := f.Fetch(t.Context(), testBundlePath+"/abc/download")
		assert.ErrorContains(t, err, "connection refused")
	})
}

// fakeFetcher serves a canned response and records every request path, standing in for the API
// server. The response can be swapped between calls.
type fakeFetcher struct {
	mu       sync.Mutex
	requests []string
	body     []byte
	err      error
}

func (f *fakeFetcher) Fetch(_ context.Context, requestPath string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, requestPath)
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

func (f *fakeFetcher) set(body []byte, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.err = body, err
}

func (f *fakeFetcher) requestPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func remoteManifest(version string, bundle []byte) string {
	return fmt.Sprintf(`{"name":"pod-counter","version":%q,"entry":"index.js","bundleSha256":%q,"bundleSource":{"apiServer":{"path":%q}}}`,
		version, bundleDigest(bundle), testBundlePath)
}

// remoteConfigMap is a manifest-only plugin ConfigMap whose manifest is signed by signer.
func remoteConfigMap(t *testing.T, signer *openpgp.Entity, manifestJSON string) *corev1.ConfigMap {
	t.Helper()
	cm := signedConfigMap(t, "pod-counter-cm", manifestJSON, asc(sign(t, signer, []byte(manifestJSON))), nil)
	cm.BinaryData = nil
	cm.ResourceVersion = manifestJSON
	return cm
}

// newRemoteTestRegistry is a Registry that downloads bundleSource "apiServer" bundles through
// fetcher. A maxBundleBytes of 0 means testMaxBundleBytes, and a negative one no limit at all.
func newRemoteTestRegistry(t *testing.T, signer *openpgp.Entity, fetcher BundleFetcher, maxBundleBytes int64) *Registry {
	t.Helper()
	switch {
	case maxBundleBytes == 0:
		maxBundleBytes = testMaxBundleBytes
	case maxBundleBytes < 0:
		maxBundleBytes = 0
	}
	r := NewRegistry(Options{
		Logger:             testr.New(t),
		Namespace:          "antrea-ui",
		LabelSelector:      "ui.antrea.io/plugin=true",
		MaxBundleBytes:     maxBundleBytes,
		SignatureVerifiers: []SignatureVerifier{trustedOpenPGPKey(t, "trusted", signer)},
		BundleFetcher:      fetcher,
	})
	t.Cleanup(r.Close)
	return r
}

func TestRemoteBundleLoads(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	fetcher := &fakeFetcher{body: bundle}
	r := newRemoteTestRegistry(t, testSigner(), fetcher, 0)

	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.0", bundle))))

	require.Len(t, r.Index(), 1)
	assert.Equal(t, "1.0.0", r.Index()[0].Version)
	assert.Equal(t, []string{testBundlePath + "/" + bundleDigest(bundle) + "/download"}, fetcher.requestPaths())
	file, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "console.log('hi')", readAll(t, file))
	entry, _ := r.getConfigMapPlugin("pod-counter-cm")
	assert.Equal(t, "trusted", entry.verifiedBy)
}

func TestRemoteBundleRejections(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	otherBundle := buildZip(t, map[string]string{"index.js": "other"})
	signedCM := func(t *testing.T, manifest string) *corev1.ConfigMap {
		return remoteConfigMap(t, testSigner(), manifest)
	}

	tests := []struct {
		name string
		// cm builds the ConfigMap; nil uses a correctly signed manifest for bundle.
		cm        func(t *testing.T) *corev1.ConfigMap
		noVerify  bool
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
			name: "no signature",
			cm: func(t *testing.T) *corev1.ConfigMap {
				cm := signedCM(t, remoteManifest("1.0.0", bundle))
				delete(cm.Data, openPGPSignatureFileName)
				return cm
			},
			want: upsertRejected,
		},
		{
			name: "signature over different bytes",
			cm: func(t *testing.T) *corev1.ConfigMap {
				cm := signedCM(t, remoteManifest("1.0.0", bundle))
				cm.Data[openPGPSignatureFileName] = string(sign(t, testSigner(), []byte(remoteManifest("2.0.0", bundle))))
				return cm
			},
			want: upsertRejected,
		},
		{name: "signature verification disabled", noVerify: true, want: upsertRejected},
		{
			name: "no digest",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return signedCM(t, fmt.Sprintf(`{"name":"pod-counter","entry":"index.js","bundleSource":{"apiServer":{"path":%q}}}`, testBundlePath))
			},
			want: upsertRejected,
		},
		{
			name: "absolute URL as path",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return signedCM(t, fmt.Sprintf(`{"name":"pod-counter","entry":"index.js","bundleSha256":%q,"bundleSource":{"apiServer":{"path":"https://evil.example/x"}}}`, bundleDigest(bundle)))
			},
			want: upsertRejected,
		},
		{
			name: "no transport",
			cm: func(t *testing.T) *corev1.ConfigMap {
				return signedCM(t, fmt.Sprintf(`{"name":"pod-counter","entry":"index.js","bundleSha256":%q,"bundleSource":{}}`, bundleDigest(bundle)))
			},
			want: upsertRejected,
		},
		{
			name: "bundle.zip as well as a bundleSource",
			cm: func(t *testing.T) *corev1.ConfigMap {
				cm := signedCM(t, remoteManifest("1.0.0", bundle))
				cm.BinaryData = map[string][]byte{bundleFileName: bundle}
				return cm
			},
			want: upsertRetry,
		},
		{
			name: "entry missing from the bundle",
			cm: func(t *testing.T) *corev1.ConfigMap {
				m := fmt.Sprintf(`{"name":"pod-counter","entry":"missing.js","bundleSha256":%q,"bundleSource":{"apiServer":{"path":%q}}}`, bundleDigest(bundle), testBundlePath)
				return signedCM(t, m)
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
			fetcher := &fakeFetcher{body: served, err: tt.fetchErr}
			r := newRemoteTestRegistry(t, testSigner(), fetcher, tt.maxBytes)
			if tt.noVerify {
				r.signatureVerifiers = nil
			}
			cm := remoteConfigMap(t, testSigner(), remoteManifest("1.0.0", bundle))
			if tt.cm != nil {
				cm = tt.cm(t)
			}

			assert.Equal(t, tt.want, r.handleUpsert(t.Context(), cm))

			assert.Empty(t, r.Index())
			assert.Equal(t, tt.wantFetch, len(fetcher.requestPaths()) > 0, "requests: %v", fetcher.requestPaths())
		})
	}
}

// A failed download of a new version leaves the previous one being served, and a later success
// replaces it.
func TestRemoteBundleKeepsPreviousVersionWhileDownloadFails(t *testing.T) {
	v1 := buildZip(t, map[string]string{"index.js": "v1"})
	v2 := buildZip(t, map[string]string{"index.js": "v2"})
	fetcher := &fakeFetcher{body: v1}
	r := newRemoteTestRegistry(t, testSigner(), fetcher, 0)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.0", v1))))

	fetcher.set(nil, errors.New("404"))
	v2CM := remoteConfigMap(t, testSigner(), remoteManifest("2.0.0", v2))
	require.Equal(t, upsertRetryAlways, r.handleUpsert(t.Context(), v2CM))
	require.Len(t, r.Index(), 1)
	assert.Equal(t, "1.0.0", r.Index()[0].Version)
	file, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "v1", readAll(t, file))

	fetcher.set(v2, nil)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), v2CM))
	require.Len(t, r.Index(), 1)
	assert.Equal(t, "2.0.0", r.Index()[0].Version)
}

// An edit that leaves bundleSha256 alone (a new version string, say) is picked up without
// downloading the bundle again; a new digest, or an extracted bundle gone missing, downloads.
func TestRemoteBundleReusesExtractedBundleForSameDigest(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	fetcher := &fakeFetcher{body: bundle}
	r := newRemoteTestRegistry(t, testSigner(), fetcher, 0)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.0", bundle))))
	require.Len(t, fetcher.requestPaths(), 1)

	fetcher.set(nil, errors.New("503"))
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.1", bundle))))
	require.Len(t, r.Index(), 1)
	assert.Equal(t, "1.0.1", r.Index()[0].Version)
	assert.Len(t, fetcher.requestPaths(), 1, "the unchanged digest must not be downloaded again")
	file, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "console.log('hi')", readAll(t, file))

	entry, _ := r.getConfigMapPlugin("pod-counter-cm")
	require.NoError(t, os.RemoveAll(entry.diskRoot))
	fetcher.set(bundle, nil)
	require.Equal(t, upsertDone, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.2", bundle))))
	assert.Len(t, fetcher.requestPaths(), 2)
}

func TestRemoteBundleWithoutFetcher(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	r := newRemoteTestRegistry(t, testSigner(), nil, 0)
	assert.Equal(t, upsertRejected, r.handleUpsert(t.Context(), remoteConfigMap(t, testSigner(), remoteManifest("1.0.0", bundle))))
	assert.Empty(t, r.Index())
}

func TestPluginDirectoryRejectsBundleSource(t *testing.T) {
	bundle := buildZip(t, podCounterBundle())
	r := newRemoteTestRegistry(t, testSigner(), &fakeFetcher{body: bundle}, 0)
	root := t.TempDir()
	manifest := remoteManifest("1.0.0", bundle)
	signedPluginDir(t, root, "pod-counter", manifest, asc(sign(t, testSigner(), []byte(manifest))), bundle)

	assert.False(t, r.loadDiskPlugin(root, "pod-counter", newTestWatcher(t)))
	assert.Empty(t, r.Index())
}

// Unlike a ConfigMap that is invalid, a failing download is retried for as long as it takes, and
// the plugin loads on the first attempt after the bundle server recovers.
func TestRemoteBundleQueueRetriesWithoutLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Made inside the bubble: its fake clock starts in 2000, before testSigner() existed.
		signer := newTestEntity(ed25519Config(), 0)
		bundle := buildZip(t, podCounterBundle())
		fetcher := &fakeFetcher{err: errors.New("503")}
		r := newRemoteTestRegistry(t, signer, fetcher, 0)

		cm := remoteConfigMap(t, signer, remoteManifest("1.0.0", bundle))
		indexer := newTestIndexer(t, cm)
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer queue.ShutDown()
		go r.runConfigMapWorker(t.Context(), indexer, queue)

		key := configMapKey(t, cm)
		queue.Add(key)
		// Far more attempts than maxPluginLoadRetries, which would have given up by now.
		synctest.Sleep(time.Hour)
		attempts := len(fetcher.requestPaths())
		assert.Greater(t, attempts, 2*maxPluginLoadRetries)
		assert.Empty(t, r.Index())

		// The backoff is capped at about a minute, so recovery is noticed within that.
		fetcher.set(bundle, nil)
		synctest.Sleep(2 * time.Minute)
		require.Len(t, r.Index(), 1)
		assert.Equal(t, 0, queue.NumRequeues(key))
		after := len(fetcher.requestPaths())
		synctest.Sleep(time.Hour)
		assert.Equal(t, after, len(fetcher.requestPaths()), "a loaded plugin must not be downloaded again")
	})
}

// A rejected ConfigMap is not retried: the download would only fail again until it changes.
func TestRemoteBundleQueueDoesNotRetryRejection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Made inside the bubble: its fake clock starts in 2000, before testSigner() existed.
		signer := newTestEntity(ed25519Config(), 0)
		bundle := buildZip(t, podCounterBundle())
		fetcher := &fakeFetcher{body: bundle}
		r := newRemoteTestRegistry(t, signer, fetcher, 0)

		cm := remoteConfigMap(t, signer, remoteManifest("1.0.0", bundle))
		delete(cm.Data, openPGPSignatureFileName)
		indexer := newTestIndexer(t, cm)
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer queue.ShutDown()
		go r.runConfigMapWorker(t.Context(), indexer, queue)

		queue.Add(configMapKey(t, cm))
		synctest.Sleep(time.Hour)
		assert.EqualValues(t, 1, indexer.gets.Load())
		assert.Empty(t, fetcher.requestPaths())
	})
}
