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

func TestPluginNameFromEventPath(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "plugins")

	name, ok := pluginNameFromEventPath(dir, filepath.Join(dir, "pod-counter"))
	require.True(t, ok)
	assert.Equal(t, "pod-counter", name)

	name, ok = pluginNameFromEventPath(dir, filepath.Join(dir, "pod-counter", "manifest.json"))
	require.True(t, ok)
	assert.Equal(t, "pod-counter", name)

	// A plugin subdirectory literally named ".." would collapse to dir's own parent under
	// filepath.Rel, i.e. rel == "..": os.ReadDir would never surface it as a child of dir in the
	// first place, but a strings.HasPrefix(rel, "..") check would still (wrongly) accept it.
	_, ok = pluginNameFromEventPath(dir, filepath.Dir(dir))
	assert.False(t, ok, "dir's own parent must not resolve to a plugin name")

	// A path outside dir entirely.
	_, ok = pluginNameFromEventPath(dir, filepath.Join(filepath.Dir(dir), "other"))
	assert.False(t, ok, "a path outside dir must not resolve to a plugin name")

	// A plugin subdirectory whose name happens to start with ".." must still resolve normally -
	// this is exactly what strings.HasPrefix(rel, "..") got wrong.
	name, ok = pluginNameFromEventPath(dir, filepath.Join(dir, "..foo"))
	require.True(t, ok)
	assert.Equal(t, "..foo", name)
}

func TestParsePluginArchive(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	entry, err := parsePluginArchive(filepath.Join(dir, "pod-counter"), filepath.Join(t.TempDir(), "pod-counter"), 0, nil)
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

	entry, err := parsePluginArchive(filepath.Join(dir, "pod-counter"), filepath.Join(t.TempDir(), "pod-counter"), 0, nil)
	require.NoError(t, err)
	rc, _, ok := entry.open("assets/logo.png")
	require.True(t, ok, "a bundle.zip entry under a subdirectory must be extracted and servable")
	assert.Equal(t, "fake-png-bytes", readAll(t, rc))
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

	entry, err := parsePluginArchive(filepath.Join(dir, "policy-management"), filepath.Join(t.TempDir(), "policy-management"), 0, nil)
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
			_, err := parsePluginArchive(filepath.Join(dir, "plugin"), filepath.Join(t.TempDir(), "plugin"), 0, nil)
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

	// No keyring is passed below, so nothing requires a manifest.json.asc - but a bundleSha256
	// that is present is still verified, and a mismatch is a rejection.
	writePluginDir(t, dir, "digest-mismatch",
		manifestWithDigest("plugin", "0.1.0", "index.js", bundleDigest([]byte("some other bundle"))),
		map[string]string{"index.js": "x"})

	for _, name := range []string{
		"missing-name", "missing-entry", "entry-not-present", "malformed-manifest",
		"missing-manifest", "missing-bundle", "malformed-bundle", "digest-mismatch",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parsePluginArchive(filepath.Join(dir, name), filepath.Join(t.TempDir(), name), 0, nil)
			assert.Error(t, err)
		})
	}
}

func TestRunDirectoryWatchLoadsExistingPlugins(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
	rc, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "console.log('hi')", readAll(t, rc))
}

// TestRunDirectoryWatchRetriesUntilDirectoryAppears exercises waitForPluginDirectory: dir doesn't
// exist yet when RunDirectoryWatch starts (e.g. a slower-starting sidecar still provisioning the
// volume backing plugins.directory), so the first several watch attempts must fail and retry
// rather than giving up for the life of the process - only after dir is created does the plugin
// inside it ever get picked up.
func TestRunDirectoryWatchRetriesUntilDirectoryAppears(t *testing.T) {
	original := dirWatchRetryDelay
	dirWatchRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { dirWatchRetryDelay = original })

	parent := t.TempDir()
	dir := filepath.Join(parent, "plugins")

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

	// Give waitForPluginDirectory a few failed attempts against the not-yet-existing dir before
	// creating it - the point being that it keeps retrying rather than exiting after the first.
	time.Sleep(5 * dirWatchRetryDelay)
	assert.Empty(t, r.Index(), "must not have loaded anything before the directory even existed")

	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())
	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
}

// TestRunDirectoryWatchLoadsPluginBehindSymlink exercises isPluginSubdirectory: a plugin
// subdirectory delivered as a symlink to another directory (a common way to point
// plugins.directory's <name> entry at a separate checkout during local development) must be
// picked up by the startup os.ReadDir scan the same way a real directory would be -
// DirEntry.IsDir() alone would miss it, since it reflects an lstat of the symlink itself.
func TestRunDirectoryWatchLoadsPluginBehindSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := t.TempDir()
	writePluginDir(t, realDir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())
	require.NoError(t, os.Symlink(filepath.Join(realDir, "pod-counter"), filepath.Join(dir, "pod-counter")))

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })
	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
}

// TestRunDirectoryWatchDropsPluginsWhenDirectoryIsRenamedAway exercises the root-watch branch of
// RunDirectoryWatch's event loop. Renaming the watched root itself arrives as a single event on
// dir with nothing for the plugin subdirectories under it (fsnotify's inotify backend sends only
// IN_MOVE_SELF, never IN_MOVED_FROM/TO), and the watch is dropped, so every plugin loaded before
// the rename has to be reconciled off Index() from that one event - dir may never come back, and
// File() would otherwise keep serving each one out of its extraction cache for the life of the
// process.
func TestRunDirectoryWatchDropsPluginsWhenDirectoryIsRenamedAway(t *testing.T) {
	original := dirWatchRetryDelay
	dirWatchRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { dirWatchRetryDelay = original })

	parent := t.TempDir()
	dir := filepath.Join(parent, "plugins")
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 1 })

	require.NoError(t, os.Rename(dir, filepath.Join(parent, "plugins-moved")))
	waitFor(t, 5*time.Second, func() bool { return len(r.Index()) == 0 })
	_, _, ok := r.File("pod-counter", "index.js")
	assert.False(t, ok, "must stop serving a plugin whose watched root was renamed away")
}

func TestRunDirectoryWatchPicksUpNewAndUpdatedAndRemovedPlugins(t *testing.T) {
	// A real filesystem/fsnotify/registry integration test, so it pays requeueDelay's real 1s
	// debounce on every live change below (unlike TestDebounce*, which exercises that timing
	// precisely and instantly via testing/synctest - fsnotify's own event-reading goroutine
	// blocks on a real OS syscall, which synctest never considers "durably blocked", so its fake
	// clock can't be used here without hanging).
	const liveChangeTimeout = 3 * requeueDelay

	dir := t.TempDir()

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

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

	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 1, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
	startDirectoryWatch(t, r, dir)

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

	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	defer watcher.Close()

	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
		t.Cleanup(r.Close)
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
	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)
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

// TestLoadDiskPluginReportsFailedWatch pins what happens when the plugin subdirectory's own
// watch can't be established - in practice the inotify watch/instance limit being exhausted,
// stood in for here by a closed watcher, since there is no portable way to reach the real limit
// from a test and fsnotify's Add fails the same way either way. The plugin still has to load and
// be served, but loadDiskPlugin must report failure: the root's watch reports the subdirectory
// entry itself, never the files inside it, so without a retry this one extraction is what gets
// served for the life of the process.
func TestLoadDiskPluginReportsFailedWatch(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "pod-counter", podCounterManifest("pod-counter", "0.1.0"), podCounterBundle())

	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	require.NoError(t, watcher.Close())

	r := newTestRegistry(t)
	assert.False(t, r.loadDiskPlugin(dir, "pod-counter", watcher), "a failed watcher.Add must be reported, so the caller retries it")
	index := r.Index()
	require.Len(t, index, 1, "the plugin must still be loaded and served despite the failed watch")
	assert.Equal(t, "pod-counter", index[0].Name)
}

// TestParsePluginArchiveRejectsBundleFileLargerThanTheBudget covers the compressed-size half of
// maxBundleBytes, which only the directory source has (see copyBundleForVerification): the
// backend copies bundle.zip before verifying and extracting it, and that copy has to be bounded.
// A single one-byte entry keeps the decompressed total (1 byte) well inside the budget, so a
// rejection here can only come from the file's own size, not from extractZip's existing check.
func TestParsePluginArchiveRejectsBundleFileLargerThanTheBudget(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "plugin", podCounterManifest("plugin", "0.1.0"), map[string]string{"index.js": "x"})

	bundleSize := fileSize(t, filepath.Join(dir, "plugin", bundleFileName))
	require.Greater(t, bundleSize, int64(1), "a zip of a one-byte entry should still carry more than a byte of framing")

	_, err := parsePluginArchive(filepath.Join(dir, "plugin"), filepath.Join(t.TempDir(), "plugin"), bundleSize-1, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "larger than this plugin's bundle size budget")

	// Exactly at the budget is fine - the same boundary extractZipFile draws.
	_, err = parsePluginArchive(filepath.Join(dir, "plugin"), filepath.Join(t.TempDir(), "plugin"), bundleSize, nil)
	assert.NoError(t, err)
}

// TestParsePluginArchiveLeavesNoBundleCopyBehind pins that the private copy of bundle.zip
// copyBundleForVerification makes is removed on the way out, including when the load fails after
// it was made - it lives in the same directory as the extractions themselves, so a leak here
// would accumulate a full copy of every bundle the backend ever reloaded.
func TestParsePluginArchiveLeavesNoBundleCopyBehind(t *testing.T) {
	src := t.TempDir()
	writePluginDir(t, src, "good", podCounterManifest("good", "0.1.0"), podCounterBundle())
	// Rejected by the digest check, which runs after the copy is made.
	writePluginDir(t, src, "bad",
		manifestWithDigest("bad", "0.1.0", "index.js", bundleDigest([]byte("some other bundle"))),
		podCounterBundle())

	cacheRoot := t.TempDir()
	_, err := parsePluginArchive(filepath.Join(src, "good"), filepath.Join(cacheRoot, "good"), 0, nil)
	require.NoError(t, err)
	_, err = parsePluginArchive(filepath.Join(src, "bad"), filepath.Join(cacheRoot, "bad"), 0, nil)
	require.Error(t, err)

	entries, err := os.ReadDir(cacheRoot)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Equal(t, []string{"good"}, names, "only the successful plugin's extraction directory should remain")
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}
