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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

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

func TestParsePluginArchive(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	entry, err := parsePluginArchive(filepath.Join(dir, "pod-counter"), t.TempDir(), "pod-counter")
	require.NoError(t, err)
	assert.Equal(t, apisv1.PluginManifest{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"}, entry.manifest)
	rc, size, ok := entry.open("index.js")
	require.True(t, ok)
	assert.Equal(t, int64(len("console.log('hi')")), size)
	assert.Equal(t, "console.log('hi')", readAll(t, rc))
}

func TestParsePluginArchiveIncludesNestedPaths(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), map[string]string{
		"index.js":        "console.log('hi')",
		"assets/logo.png": "fake-png-bytes",
	})

	entry, err := parsePluginArchive(filepath.Join(dir, "pod-counter"), t.TempDir(), "pod-counter")
	require.NoError(t, err)
	rc, _, ok := entry.open("assets/logo.png")
	require.True(t, ok, "a bundle.zip entry under a subdirectory must be extracted and servable")
	assert.Equal(t, "fake-png-bytes", readAll(t, rc))
}

func TestExtractZipNeutralizesPathTraversal(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "plugin")
	require.NoError(t, os.MkdirAll(pluginDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, manifestFileName),
		[]byte(`{"name":"plugin","version":"0.1.0","entry":"index.js"}`), 0o600))

	// buildZip can't produce a traversal-shaped entry name via its map[string]string files
	// argument in a way that's clearer than just writing the zip directly here.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"index.js":          "console.log('hi')",
		"../../../evil.txt": "should not escape",
	} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, bundleFileName), buf.Bytes(), 0o600))

	cacheParent := t.TempDir()
	cacheRoot := filepath.Join(cacheParent, "cache")
	require.NoError(t, os.MkdirAll(cacheRoot, 0o755))

	entry, err := parsePluginArchive(pluginDir, cacheRoot, "plugin")
	require.NoError(t, err)

	// The malicious entry lands safely inside the plugin's own extraction directory...
	rc, _, ok := entry.open("evil.txt")
	require.True(t, ok)
	assert.Equal(t, "should not escape", readAll(t, rc))

	// ...and does not actually escape onto the filesystem outside cacheRoot ("zip slip").
	_, err = os.Stat(filepath.Join(cacheParent, "evil.txt"))
	assert.True(t, os.IsNotExist(err), "path traversal entry must not escape the cache root")
}

func TestExtractZipRejectsSingleEntryPastTheDecompressedSizeLimit(t *testing.T) {
	original := maxDiskPluginBundleBytes
	maxDiskPluginBundleBytes = 100
	t.Cleanup(func() { maxDiskPluginBundleBytes = original })

	dir := t.TempDir()
	writePluginDir(t, dir, "plugin", `{"name":"plugin","version":"0.1.0","entry":"index.js"}`, map[string]string{
		"index.js": strings.Repeat("x", 200),
	})

	_, err := parsePluginArchive(filepath.Join(dir, "plugin"), t.TempDir(), "plugin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zip bomb")
}

// TestExtractZipRejectsCombinedEntriesPastTheDecompressedSizeLimit exercises the case a single
// oversized entry can't: maxDiskPluginBundleBytes bounds the bundle's total decompressed size, not
// any one entry's, so several individually-small entries that together exceed it must also be
// rejected.
func TestExtractZipRejectsCombinedEntriesPastTheDecompressedSizeLimit(t *testing.T) {
	original := maxDiskPluginBundleBytes
	maxDiskPluginBundleBytes = 100
	t.Cleanup(func() { maxDiskPluginBundleBytes = original })

	dir := t.TempDir()
	writePluginDir(t, dir, "plugin", `{"name":"plugin","version":"0.1.0","entry":"index.js"}`, map[string]string{
		"index.js": strings.Repeat("x", 60),
		"other.js": strings.Repeat("y", 60),
	})

	_, err := parsePluginArchive(filepath.Join(dir, "plugin"), t.TempDir(), "plugin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zip bomb")
}

func TestParsePluginArchiveIncludesRoutesAndFederation(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "policy-management", `{
			"name": "policy-management",
			"version": "0.2.0",
			"entry": "index.js",
			"federation": {
				"remoteEntry": "remoteEntry.json",
				"routes": [
					{"path": "/policies", "sidebarLabel": "Policy Management", "icon": "M0 0h16v16H0z", "exposedModule": "./PolicyManagementPage"},
					{"path": "/policies/audit", "sidebarLabel": "Policy Audit Log", "exposedModule": "./PolicyAuditPage"}
				]
			}
		}`, map[string]string{
		"index.js":         "x",
		"remoteEntry.json": "{}",
	})

	entry, err := parsePluginArchive(filepath.Join(dir, "policy-management"), t.TempDir(), "policy-management")
	require.NoError(t, err)
	assert.Equal(t, apisv1.PluginManifest{
		Name:    "policy-management",
		Version: "0.2.0",
		Entry:   "index.js",
		Federation: &apisv1.PluginFederation{
			RemoteEntry: "remoteEntry.json",
			Routes: []apisv1.PluginRoute{
				{Path: "/policies", SidebarLabel: "Policy Management", Icon: "M0 0h16v16H0z", ExposedModule: "./PolicyManagementPage"},
				{Path: "/policies/audit", SidebarLabel: "Policy Audit Log", ExposedModule: "./PolicyAuditPage"},
			},
		},
	}, entry.manifest)
}

func TestParsePluginArchiveRejectsInvalidFederationRoutes(t *testing.T) {
	cases := map[string]struct {
		manifest string
		bundle   map[string]string
	}{
		"route missing path": {
			`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
			map[string]string{"index.js": "x", "remoteEntry.json": "x"},
		},
		"route path under reserved api/ prefix": {
			`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/api/v1/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
			map[string]string{"index.js": "x", "remoteEntry.json": "x"},
		},
		"federation remoteEntry file not present": {
			`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json"}}`,
			map[string]string{"index.js": "x"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writePluginDir(t, dir, "plugin", c.manifest, c.bundle)
			_, err := parsePluginArchive(filepath.Join(dir, "plugin"), t.TempDir(), "plugin")
			assert.Error(t, err)
		})
	}
}

func TestParsePluginArchiveInvalid(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "missing-name", `{"version":"0.1.0","entry":"index.js"}`, map[string]string{"index.js": "x"})
	writePluginDir(t, dir, "missing-entry", `{"name":"plugin","version":"0.1.0"}`, map[string]string{"index.js": "x"})
	writePluginDir(t, dir, "entry-not-present", `{"name":"plugin","version":"0.1.0","entry":"index.js"}`, map[string]string{"other.js": "x"})
	writePluginDir(t, dir, "malformed-manifest", "not json", map[string]string{"index.js": "x"})

	// missing manifest.json entirely
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "missing-manifest"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "missing-manifest", bundleFileName), buildZip(t, map[string]string{"index.js": "x"}), 0o600))

	// missing bundle.zip entirely
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "missing-bundle"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "missing-bundle", manifestFileName),
		[]byte(`{"name":"plugin","version":"0.1.0","entry":"index.js"}`), 0o600))

	// malformed bundle.zip (not a zip at all)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "malformed-bundle"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "malformed-bundle", manifestFileName),
		[]byte(`{"name":"plugin","version":"0.1.0","entry":"index.js"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "malformed-bundle", bundleFileName), []byte("not a zip"), 0o600))

	for _, name := range []string{
		"missing-name", "missing-entry", "entry-not-present", "malformed-manifest",
		"missing-manifest", "missing-bundle", "malformed-bundle",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parsePluginArchive(filepath.Join(dir, name), t.TempDir(), name)
			assert.Error(t, err)
		})
	}
}

func TestRunDirectoryWatchLoadsExistingPlugins(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
	rc, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "console.log('hi')", readAll(t, rc))
}

func TestRunDirectoryWatchPicksUpNewAndUpdatedAndRemovedPlugins(t *testing.T) {
	// A real filesystem/fsnotify/registry integration test, so it pays requeueDelay's real 1s
	// debounce on every live change below (unlike TestDebounce*, which exercises that timing
	// precisely and instantly via testing/synctest - fsnotify's own event-reading goroutine
	// blocks on a real OS syscall, which synctest never considers "durably blocked", so its fake
	// clock can't be used here without hanging).
	const liveChangeTimeout = 3 * requeueDelay

	dir := t.TempDir()

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 0 })

	// A plugin subdirectory created after the watch started is picked up.
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())
	waitFor(t, liveChangeTimeout, func() bool { return len(r.Index()) == 1 })

	// A file changed within an already-known plugin subdirectory reloads that plugin.
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.2.0"), podCounterBundle())
	waitFor(t, liveChangeTimeout, func() bool {
		rc, _, ok := r.File("pod-counter", "index.js")
		if !ok {
			return false
		}
		return readAll(t, rc) == "console.log('hi')" && len(r.Index()) == 1 && r.Index()[0].Version == "0.2.0"
	})

	// Removing the plugin subdirectory drops it.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "pod-counter")))
	waitFor(t, liveChangeTimeout, func() bool { return len(r.Index()) == 0 })
}

func TestRunDirectoryWatchRejectsNewPluginPastLimit(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "first", podCounterManifest("first", "0.1.0"), podCounterBundle())
	writePluginDir(t, dir, "second", podCounterManifest("second", "0.1.0"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 1, 0)
	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	// Give a would-be second load a moment to (not) land, then confirm it's still just one.
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, r.Index(), 1)
}

// TestDebounceCollapsesBurstIntoOneDelayedReload exercises requeueDelay's exact timing via
// testing/synctest's fake clock, deterministically and without a real 1s wait - unlike
// TestRunDirectoryWatchPicksUpNewAndUpdatedAndRemovedPlugins above, this drives
// runDiskPluginWorker's queue directly instead of going through RunDirectoryWatch/fsnotify:
// fsnotify's own event-reading goroutine blocks on a real OS syscall, which synctest never
// considers "durably blocked", so its fake clock can't advance while that goroutine exists
// inside the bubble. The *fsnotify.Watcher below is therefore constructed outside
// synctest.Test on purpose - this test never reads its Events channel, it only needs a live
// *Watcher for loadDiskPlugin's watcher.Add call.
func TestDebounceCollapsesBurstIntoOneDelayedReload(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())
	cacheRoot := t.TempDir()

	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	defer watcher.Close()

	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer func() {
			queue.ShutDown()
			synctest.Wait()
		}()
		go r.runDiskPluginWorker(dir, cacheRoot, watcher, queue)

		// Five events for the same plugin in a tight burst - as if a build wrote five files in
		// a row - collapse into the one delayed entry the workqueue already knows about: a
		// later AddAfter for a known key only wins if it would fire sooner (see
		// delaying_queue.go's insert), and under the fake clock these all share the same
		// virtual "now" anyway, since no goroutine yields between them.
		for range 5 {
			queue.AddAfter("pod-counter", requeueDelay)
		}

		time.Sleep(requeueDelay - time.Nanosecond)
		synctest.Wait()
		assert.Empty(t, r.Index(), "must not reload before requeueDelay elapses")

		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Len(t, r.Index(), 1)
		assert.Equal(t, "pod-counter", r.Index()[0].Name)
	})
}

func TestRunDirectoryWatchIsNoopWhenDirectoryEmpty(t *testing.T) {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		r.RunDirectoryWatch("", stopCh)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunDirectoryWatch(\"\", ...) did not return promptly")
	}
	assert.Empty(t, r.Index())
}

func TestDirectoryAndConfigMapPluginsMerge(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "disk-plugin", podCounterManifest("disk-plugin", "0.1.0"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	r.handleUpsert(configMap(t, "cm-plugin", "cm-plugin", "0.1.0", "index.js", map[string]string{"index.js": "x"}))

	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 2 })
}

func TestDirectoryPluginLosesNameCollisionToConfigMap(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "shared-name", podCounterManifest("shared", "from-disk"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	r.handleUpsert(configMap(t, "shared-name", "shared", "from-configmap", "index.js", map[string]string{"index.js": "x"}))

	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	// "configmap/shared-name" sorts before "directory/shared-name", so the ConfigMap always
	// wins this collision regardless of load order.
	waitFor(t, time.Second, func() bool {
		manifests := r.Index()
		return len(manifests) == 1 && manifests[0].Version == "from-configmap"
	})
}
