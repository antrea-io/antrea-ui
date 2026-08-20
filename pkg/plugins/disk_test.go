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
	"os"
	"path/filepath"
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

func writePluginDir(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for filename, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o600))
	}
}

func podCounterFiles(pluginName, version string) map[string]string {
	return map[string]string{
		"manifest.json": `{"name":"` + pluginName + `","version":"` + version + `","entry":"index.js"}`,
		"index.js":      "console.log('hi')",
	}
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

func TestParsePluginDirectory(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.1.0"))

	entry, err := parsePluginDirectory(filepath.Join(dir, "pod-counter"))
	require.NoError(t, err)
	assert.Equal(t, apisv1.PluginManifest{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"}, entry.manifest)
	assert.Equal(t, "console.log('hi')", string(entry.files["index.js"]))
}

func TestParsePluginDirectorySkipsNestedSubdirectories(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "pod-counter")
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.1.0"))
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "nested", "extra.js"), []byte("x"), 0o600))

	entry, err := parsePluginDirectory(pluginDir)
	require.NoError(t, err)
	_, ok := entry.files["nested/extra.js"]
	assert.False(t, ok)
	_, ok = entry.files["nested"]
	assert.False(t, ok)
}

func TestParsePluginDirectoryIncludesRoutesAndFederation(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "policy-management", map[string]string{
		"manifest.json": `{
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
		}`,
		"index.js":         "x",
		"remoteEntry.json": "{}",
	})

	entry, err := parsePluginDirectory(filepath.Join(dir, "policy-management"))
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

func TestParsePluginDirectoryRejectsInvalidFederationRoutes(t *testing.T) {
	cases := map[string]map[string]string{
		"route missing path": {
			"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
			"index.js":         "x",
			"remoteEntry.json": "x",
		},
		"route path under reserved api/ prefix": {
			"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/api/v1/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
			"index.js":         "x",
			"remoteEntry.json": "x",
		},
		"federation remoteEntry file not present": {
			"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json"}}`,
			"index.js":      "x",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writePluginDir(t, dir, "plugin", files)
			_, err := parsePluginDirectory(filepath.Join(dir, "plugin"))
			assert.Error(t, err)
		})
	}
}

func TestParsePluginDirectoryInvalid(t *testing.T) {
	cases := map[string]map[string]string{
		"missing manifest.json": {"index.js": "x"},
		"malformed manifest.json": {
			"manifest.json": "not json",
		},
		"missing name":  {"manifest.json": `{"version":"0.1.0","entry":"index.js"}`, "index.js": "x"},
		"missing entry": {"manifest.json": `{"name":"plugin","version":"0.1.0"}`, "index.js": "x"},
		"entry file not present": {
			"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js"}`,
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writePluginDir(t, dir, "plugin", files)
			_, err := parsePluginDirectory(filepath.Join(dir, "plugin"))
			assert.Error(t, err)
		})
	}
}

func TestParsePluginDirectoryRejectsOversizedBundle(t *testing.T) {
	originalMax := maxDiskPluginBundleBytes
	maxDiskPluginBundleBytes = 20
	t.Cleanup(func() { maxDiskPluginBundleBytes = originalMax })

	dir := t.TempDir()
	writePluginDir(t, dir, "plugin", map[string]string{
		"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js"}`,
		"index.js":      "this file alone is already well over twenty bytes",
	})

	_, err := parsePluginDirectory(filepath.Join(dir, "plugin"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestParsePluginDirectoryAcceptsBundleUnderTheLimit(t *testing.T) {
	originalMax := maxDiskPluginBundleBytes
	maxDiskPluginBundleBytes = 1024
	t.Cleanup(func() { maxDiskPluginBundleBytes = originalMax })

	dir := t.TempDir()
	writePluginDir(t, dir, "plugin", podCounterFiles("plugin", "0.1.0"))

	_, err := parsePluginDirectory(filepath.Join(dir, "plugin"))
	require.NoError(t, err)
}

func TestRunDirectoryWatchLoadsExistingPlugins(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.1.0"))

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
	data, ok := r.File("pod-counter", "index.js")
	assert.True(t, ok)
	assert.Equal(t, "console.log('hi')", string(data))
}

func TestRunDirectoryWatchPicksUpNewAndUpdatedAndRemovedPlugins(t *testing.T) {
	// A real filesystem/fsnotify/registry integration test, so it pays requeueDelay's real 1s
	// debounce on every live change below (unlike TestDebounce*, which exercises that timing
	// precisely and instantly via testing/synctest - fsnotify's own event-reading goroutine
	// blocks on a real OS syscall, which synctest never considers "durably blocked", so its fake
	// clock can't be used here without hanging).
	const liveChangeTimeout = 3 * requeueDelay

	dir := t.TempDir()

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 0 })

	// A plugin subdirectory created after the watch started is picked up.
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.1.0"))
	waitFor(t, liveChangeTimeout, func() bool { return len(r.Index()) == 1 })

	// A file changed within an already-known plugin subdirectory reloads that plugin.
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.2.0"))
	waitFor(t, liveChangeTimeout, func() bool {
		data, ok := r.File("pod-counter", "index.js")
		return ok && string(data) == "console.log('hi')" && len(r.Index()) == 1 && r.Index()[0].Version == "0.2.0"
	})

	// Removing the plugin subdirectory drops it.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "pod-counter")))
	waitFor(t, liveChangeTimeout, func() bool { return len(r.Index()) == 0 })
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
	writePluginDir(t, dir, "pod-counter", podCounterFiles("pod-counter", "0.1.0"))

	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	defer watcher.Close()

	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer func() {
			queue.ShutDown()
			synctest.Wait()
		}()
		go r.runDiskPluginWorker(dir, watcher, queue)

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
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
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
	writePluginDir(t, dir, "disk-plugin", podCounterFiles("disk-plugin", "0.1.0"))

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
	r.handleUpsert(configMap("cm-plugin", "cm-plugin", "0.1.0", "index.js", map[string]string{"index.js": "x"}))

	stopCh := make(chan struct{})
	defer close(stopCh)
	go r.RunDirectoryWatch(dir, stopCh)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 2 })
}

func TestDirectoryPluginLosesNameCollisionToConfigMap(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "shared-name", podCounterFiles("shared", "from-disk"))

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
	r.handleUpsert(configMap("shared-name", "shared", "from-configmap", "index.js", map[string]string{"index.js": "x"}))

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
