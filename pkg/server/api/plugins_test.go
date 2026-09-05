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

package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// buildZip builds an in-memory bundle.zip from files (path -> content).
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// createPluginConfigMap creates a plugin ConfigMap in the current data["manifest.json"] +
// binaryData["bundle.zip"] shape (see pkg/plugins/registry.go's package doc).
func createPluginConfigMap(t *testing.T, ts *testServer, name, pluginName, version, entry string, bundleFiles map[string][]byte) {
	t.Helper()
	_, err := ts.pluginsClientset.CoreV1().ConfigMaps("antrea-ui").Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"ui.antrea.io/plugin": "true"},
		},
		Data: map[string]string{
			"manifest.json": `{"name":"` + pluginName + `","version":"` + version + `","entry":"` + entry + `"}`,
		},
		BinaryData: map[string][]byte{"bundle.zip": buildZip(t, bundleFiles)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// waitForPluginIndex polls GET /api/v1/plugins/index.json until it matches want, since the
// ConfigMap watch that backs the registry is asynchronous.
func waitForPluginIndex(t *testing.T, ts *testServer, want []apisv1.PluginManifest) {
	t.Helper()
	require.Eventually(t, func() bool {
		req := httptest.NewRequest("GET", "/api/v1/plugins/index.json", nil)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			return false
		}
		var got []apisv1.PluginManifest
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			return false
		}
		return assert.ObjectsAreEqual(want, got)
	}, 5*time.Second, 10*time.Millisecond)
}

func TestGetPluginsIndex(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/plugins/index.json", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	assert.JSONEq(t, "[]", rr.Body.String())

	createPluginConfigMap(t, ts, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string][]byte{
		"index.js": []byte("console.log('hi')"),
	})
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	})
}

// TestGetPluginsIndexReflectsConfigMapDeletion exercises Run's queue-driven delete path
// end-to-end: a deleted ConfigMap is detected by its absence from the informer's indexer (see
// processConfigMapQueueItem), not by the object a Delete event happens to carry.
func TestGetPluginsIndexReflectsConfigMapDeletion(t *testing.T) {
	ts := newTestServer(t)

	createPluginConfigMap(t, ts, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string][]byte{
		"index.js": []byte("console.log('hi')"),
	})
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	})

	require.NoError(t, ts.pluginsClientset.CoreV1().ConfigMaps("antrea-ui").Delete(context.Background(), "pod-counter-plugin", metav1.DeleteOptions{}))
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{})

	req := httptest.NewRequest("GET", "/api/v1/plugins/pod-counter/index.js", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code, "a deleted plugin's files must stop being served")
}

func TestGetPluginFile(t *testing.T) {
	ts := newTestServer(t)
	createPluginConfigMap(t, ts, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string][]byte{
		"index.js": []byte("console.log('hi')"),
	})
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	})

	req := httptest.NewRequest("GET", "/api/v1/plugins/pod-counter/index.js", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/javascript", rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", rr.Header().Get("Content-Security-Policy"))
	assert.Empty(t, rr.Header().Get("Content-Encoding"))
	b, err := io.ReadAll(rr.Result().Body)
	require.NoError(t, err)
	assert.Equal(t, "console.log('hi')", string(b))
}

func TestGetPluginFileNestedPath(t *testing.T) {
	ts := newTestServer(t)
	createPluginConfigMap(t, ts, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string][]byte{
		"index.js":        []byte("console.log('hi')"),
		"assets/logo.png": []byte("fake-png-bytes"),
	})
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	})

	req := httptest.NewRequest("GET", "/api/v1/plugins/pod-counter/assets/logo.png", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "image/png", rr.Header().Get("Content-Type"))
	b, err := io.ReadAll(rr.Result().Body)
	require.NoError(t, err)
	assert.Equal(t, "fake-png-bytes", string(b))
}

func TestPluginFileContentType(t *testing.T) {
	cases := map[string]string{
		"index.js":          "application/javascript",
		"module.mjs":        "application/javascript",
		"manifest.json":     "application/json",
		"styles.css":        "text/css",
		"assets/logo.svg":   "image/svg+xml",
		"assets/logo.png":   "image/png",
		"assets/photo.jpg":  "image/jpeg",
		"assets/photo.jpeg": "image/jpeg",
		"assets/anim.gif":   "image/gif",
		"assets/photo.webp": "image/webp",
		"favicon.ico":       "image/x-icon",
		"font.woff":         "font/woff",
		"font.woff2":        "font/woff2",
		"data.bin":          "application/octet-stream",
		"no-extension":      "application/octet-stream",
	}
	for filename, want := range cases {
		t.Run(filename, func(t *testing.T) {
			assert.Equal(t, want, pluginFileContentType(filename))
		})
	}
}

func TestGetPluginFileGzipped(t *testing.T) {
	ts := newTestServer(t)
	var gzipped bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	_, err := gw.Write([]byte("console.log('hi')"))
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	createPluginConfigMap(t, ts, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string][]byte{
		"index.js": gzipped.Bytes(),
	})
	waitForPluginIndex(t, ts, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	})

	req := httptest.NewRequest("GET", "/api/v1/plugins/pod-counter/index.js", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"))
	assert.Equal(t, gzipped.Bytes(), rr.Body.Bytes())
}

func TestGetPluginFileNotFound(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/plugins/does-not-exist/index.js", nil)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}
