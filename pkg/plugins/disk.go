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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/client-go/util/workqueue"
)

// requeueDelay debounces a burst of fsnotify events for the same plugin (an editor's
// temp-file-swap on save, or a multi-file build writing several files in quick succession) into
// one reload instead of one per individual event. See TestDebounce* in disk_test.go for the
// timing behavior this produces, exercised via testing/synctest's fake clock rather than by
// overriding this value.
const requeueDelay = time.Second

// RunDirectoryWatch watches dir for plugin bundle subdirectories - one directory per plugin, each
// holding a manifest.json plus a bundle.zip with everything else it references, the on-disk
// mirror of a plugin ConfigMap's Data/BinaryData. Meant for local development (no cluster
// round-trip to iterate on a plugin), and as a fallback if a deployment's plugins outgrow a
// ConfigMap's 1MiB cap and it mounts a shared volume here instead. A plugin subdirectory can be
// created, changed, or removed at any time; the registry reflects the change shortly after (see
// requeueDelay), with no antrea-ui restart required. Blocks and should be called from a
// goroutine. A no-op if dir is empty.
func (r *Registry) RunDirectoryWatch(dir string, stopCh <-chan struct{}) {
	if dir == "" {
		return
	}

	// bundle.zip is extracted here, never back into dir itself: dir is what's being watched
	// below, so writing extracted output into it would make this function see its own writes as
	// new fsnotify events. Removed on return - this is a cache, not a source of truth, and
	// nothing else needs it to survive a restart.
	cacheRoot, err := os.MkdirTemp("", "antrea-ui-plugins-*")
	if err != nil {
		r.logger.Error(err, "failed to create plugin extraction cache directory")
		return
	}
	defer os.RemoveAll(cacheRoot)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		r.logger.Error(err, "failed to create plugin directory watcher")
		return
	}
	defer watcher.Close()

	if err := watcher.Add(dir); err != nil {
		r.logger.Error(err, "failed to watch plugin directory", "directory", dir)
		return
	}

	// A rate-limited, delaying queue of plugin (subdirectory) names rather than handling each
	// fsnotify event inline: it debounces bursts (AddAfter below) into one reload, and gives
	// transient failures (e.g. reading a file mid-write) a rate-limited retry via AddRateLimited
	// - see the invalid-plugin-directory branch in loadDiskPlugin.
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	go r.runDiskPluginWorker(dir, cacheRoot, watcher, queue)

	entries, err := os.ReadDir(dir)
	if err != nil {
		r.logger.Error(err, "failed to read plugin directory", "directory", dir)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			queue.Add(e.Name())
		}
	}

	r.logger.Info("Starting plugin directory watch", "directory", dir)
	for {
		select {
		case <-stopCh:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only these four event kinds can possibly change what a plugin looks like; some
			// platforms also report chmod/access-time events fsnotify passes through, which
			// never warrant a reload.
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) &&
				!event.Has(fsnotify.Remove) && !event.Has(fsnotify.Rename) {
				continue
			}
			if pluginName, ok := pluginNameFromEventPath(dir, event.Name); ok {
				queue.AddAfter(pluginName, requeueDelay)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error(err, "plugin directory watch error", "directory", dir)
		}
	}
}

// runDiskPluginWorker drains queue, (re)loading or dropping one plugin per item, until queue is
// shut down (RunDirectoryWatch returning closes it via its own defer).
//
// watcher.Add/Remove (called below, from this goroutine) are safe to call concurrently with
// RunDirectoryWatch's goroutine reading watcher.Events: both of fsnotify's inotify and kqueue
// backends guard their internal watch-list mutations with their own mutex (see fsnotify's
// backend_inotify.go/backend_kqueue.go) - this isn't a special case this code has to account for
// itself, it's how fsnotify.Watcher is meant to be used from multiple goroutines.
func (r *Registry) runDiskPluginWorker(dir, cacheRoot string, watcher *fsnotify.Watcher, queue workqueue.TypedRateLimitingInterface[string]) {
	for {
		pluginName, shutdown := queue.Get()
		if shutdown {
			return
		}
		r.processDiskPluginQueueItem(dir, cacheRoot, pluginName, watcher, queue)
		queue.Done(pluginName)
	}
}

func (r *Registry) processDiskPluginQueueItem(dir, cacheRoot, pluginName string, watcher *fsnotify.Watcher, queue workqueue.TypedRateLimitingInterface[string]) {
	pluginDir := filepath.Join(dir, pluginName)
	if info, err := os.Stat(pluginDir); err != nil || !info.IsDir() {
		// The plugin's subdirectory itself is gone (removed or renamed away); drop it if we
		// were serving it. watcher.Remove errors if it was never watched (e.g. this event is
		// for a file, not the subdirectory) - harmless, nothing to clean up in that case.
		r.deleteDiskPlugin(pluginName)
		_ = watcher.Remove(pluginDir)
		_ = os.RemoveAll(filepath.Join(cacheRoot, pluginName))
		queue.Forget(pluginName)
		return
	}
	if r.loadDiskPlugin(dir, cacheRoot, pluginName, watcher) {
		queue.Forget(pluginName)
	} else {
		// A transient failure (e.g. this fired mid-write, before every referenced file exists
		// yet) gets a rate-limited retry instead of waiting for the next unrelated fsnotify
		// event on this plugin to paper over it.
		queue.AddRateLimited(pluginName)
	}
}

// pluginNameFromEventPath returns the immediate child of dir that path falls under - the plugin
// subdirectory an fsnotify event belongs to, whether the event fired on that subdirectory itself
// or on a file inside it.
func pluginNameFromEventPath(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return strings.SplitN(filepath.ToSlash(rel), "/", 2)[0], true
}

// loadDiskPlugin (re)loads the plugin bundle in rootDir/pluginName and watches that subdirectory
// for future file changes - fsnotify does not watch recursively, so a freshly-seen subdirectory
// needs its own explicit watch. Reports whether the load succeeded, so the caller can decide
// whether to retry.
func (r *Registry) loadDiskPlugin(rootDir, cacheRoot, pluginName string, watcher *fsnotify.Watcher) bool {
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := watcher.Add(pluginDir); err != nil {
		r.logger.Error(err, "failed to watch plugin directory", "directory", pluginDir)
	}
	entry, err := parsePluginArchive(pluginDir, cacheRoot, pluginName)
	if err != nil {
		r.logger.Error(err, "skipping invalid plugin directory", "directory", pluginDir)
		r.deleteDiskPlugin(pluginName)
		return false
	}
	if !r.addDiskPlugin(pluginName, *entry) {
		r.logger.Error(fmt.Errorf("plugins.maxDirectoryPlugins (%d) reached", r.maxDirectoryPlugins), "too many plugin directories, dropping", "directory", pluginDir)
		return false
	}
	r.logger.Info("Loaded plugin from directory", "directory", pluginDir, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
	return true
}

// addDiskPlugin records entry under name, unless name is new and the source is already at
// maxDirectoryPlugins - reports whether it was recorded.
func (r *Registry) addDiskPlugin(name string, entry pluginEntry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.diskPlugins[name]; !exists && r.maxDirectoryPlugins > 0 && len(r.diskPlugins) >= r.maxDirectoryPlugins {
		return false
	}
	r.diskPlugins[name] = entry
	return true
}

func (r *Registry) deleteDiskPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.diskPlugins, name)
}

// parsePluginArchive reads pluginDir/manifest.json and pluginDir/bundle.zip, validates the
// manifest against the archive's file names (cheap: bundle.zip's central directory lists names
// and sizes without decompressing anything), and only then extracts bundle.zip into
// cacheRoot/pluginName - no reason to write a bundle to disk that's going to be rejected anyway.
func parsePluginArchive(pluginDir, cacheRoot, pluginName string) (*pluginEntry, error) {
	manifestData, err := os.ReadFile(filepath.Join(pluginDir, manifestFileName))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", manifestFileName, err)
	}

	zr, err := zip.OpenReader(filepath.Join(pluginDir, bundleFileName))
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", bundleFileName, err)
	}
	defer zr.Close()

	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			names[f.Name] = true
		}
	}
	manifest, err := validateManifest(manifestData, names)
	if err != nil {
		return nil, err
	}

	extractRoot, err := extractZip(&zr.Reader, cacheRoot, pluginName)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, diskRoot: extractRoot}, nil
}

// maxDiskPluginBundleBytes bounds how much a single plugin's bundle.zip may decompress to in
// total, across every entry combined. Checked while extracting (extractZipFile), one entry at a
// time against however much of the budget earlier entries in the same bundle already spent - a
// backstop against a "zip bomb" (a small compressed bundle with an extreme compression ratio),
// which would otherwise let io.CopyN below write an unbounded amount to local disk. A var, not a
// const, so tests can shrink it instead of constructing a real 10MiB fixture.
var maxDiskPluginBundleBytes int64 = 10 * 1024 * 1024 // 10MiB

// extractZip extracts every regular file entry of zr into a fresh cacheRoot/pluginName,
// replacing any previous extraction under that name - a clean, all-or-nothing copy per reload
// rather than layering a new bundle's files over a previous one's leftovers (which could leave a
// file the new bundle no longer references still being served).
func extractZip(zr *zip.Reader, cacheRoot, pluginName string) (string, error) {
	dest := filepath.Join(cacheRoot, pluginName)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("failed to clear previous extraction of %q: %w", pluginName, err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return "", fmt.Errorf("failed to create extraction directory for %q: %w", pluginName, err)
	}
	var written int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// safeJoin neutralizes a malicious entry name (e.g. "../../etc/cron.d/evil") the same
		// way it neutralizes a malicious URL filename in pluginEntry.open - this is exactly the
		// "zip slip" vulnerability class, on the write side instead of the read side.
		target := safeJoin(dest, f.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("failed to create directory for %q: %w", f.Name, err)
		}
		n, err := extractZipFile(f, target, maxDiskPluginBundleBytes-written)
		if err != nil {
			return "", err
		}
		written += n
	}
	return dest, nil
}

// extractZipFile writes f's decompressed content to target, stopping and returning an error if
// it would write more than budget bytes - budget is whatever's left of maxDiskPluginBundleBytes
// after every earlier entry in the same bundle (see extractZip), so the limit applies to the
// bundle as a whole rather than resetting per file. Returns the number of bytes written.
func extractZipFile(f *zip.File, target string, budget int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("failed to open %q in %s: %w", f.Name, bundleFileName, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("failed to create %q: %w", target, err)
	}
	defer out.Close()
	written, err := io.CopyN(out, rc, budget+1)
	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("failed to write %q: %w", target, err)
	}
	if written > budget {
		return 0, fmt.Errorf("plugin bundle exceeds the %d byte limit while extracting %q, refusing to extract further (possible zip bomb)", maxDiskPluginBundleBytes, f.Name)
	}
	return written, nil
}
