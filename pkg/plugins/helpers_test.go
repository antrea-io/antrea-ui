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
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestRegistry(t *testing.T) *Registry {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	t.Cleanup(r.Close)
	return r
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(data)
}

// configMap builds a plugin ConfigMap in the current data["manifest.json"] +
// binaryData["bundle.zip"] shape: manifest.json separate (small, human-readable), everything
// else (bundleFiles) zipped into one binaryData key - see registry.go's package doc for why. A
// nil bundleFiles omits binaryData entirely (for cases exercising a missing bundle.zip). Reuses
// version as the ConfigMap's ResourceVersion, standing in for the apiserver bumping it on every
// real write - every test upserting the same name with a new version this way still exercises
// handleUpsert's ResourceVersion-unchanged skip correctly.
func configMap(t *testing.T, name, pluginName, version, entry string, bundleFiles map[string]string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui", ResourceVersion: version},
		Data: map[string]string{
			"manifest.json": fmt.Sprintf(`{"name":%q,"version":%q,"entry":%q}`, pluginName, version, entry),
		},
	}
	if bundleFiles != nil {
		cm.BinaryData = map[string][]byte{"bundle.zip": buildZip(t, bundleFiles)}
	}
	return cm
}

// buildZip builds an in-memory bundle.zip from files (path -> content), for any test that needs
// one - both sources' fixtures are built on top of it, as is extractZip's own suite.
func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// writePluginDir writes a plugin's on-disk bundle in the current manifest.json + bundle.zip
// shape (see registry.go's package doc): manifestJSON goes to disk as-is, bundleFiles get zipped
// into bundle.zip.
func writePluginDir(t *testing.T, root, name, manifestJSON string, bundleFiles map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, manifestFileName), []byte(manifestJSON), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, bundleFileName), buildZip(t, bundleFiles), 0o600))
}

func podCounterManifest(pluginName, version string) string {
	return fmt.Sprintf(`{"name":%q,"version":%q,"entry":"index.js"}`, pluginName, version)
}

func podCounterBundle() map[string]string {
	return map[string]string{"index.js": "console.log('hi')"}
}

// waitFor polls cond until it returns true or the timeout elapses, failing the test otherwise -
// needed because RunDirectoryWatch's fsnotify-driven updates happen asynchronously.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, cond(), "condition not met within %s", timeout)
}

// startDirectoryWatch runs r.RunDirectoryWatch(dir, ...) in a goroutine and registers a
// t.Cleanup that stops it and waits for the goroutine to actually exit before the test returns -
// r.RunDirectoryWatch logs through r.logger (testr, which writes via t.Log), so a goroutine still
// running after the test function itself returns would otherwise race with the test harness's
// own teardown of t.
func startDirectoryWatch(t *testing.T, r *Registry, dir string) {
	t.Helper()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.RunDirectoryWatch(dir, stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-done
	})
}
