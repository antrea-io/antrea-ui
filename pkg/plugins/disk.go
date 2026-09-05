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
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// dirWatchRetryDelay is how long waitForPluginDirectory waits between attempts to watch/list dir
// before it exists or becomes readable - e.g. a volume a slower-starting sidecar still has to
// provision, or a hostPath not yet mounted. Long enough not to spin/log on every failed attempt,
// short enough that a directory appearing shortly after antrea-ui starts is picked up without a
// restart. A var, not a const, so tests can shrink it instead of waiting out a real 5s retry.
var dirWatchRetryDelay = 5 * time.Second

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

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		r.logger.Error(err, "failed to create plugin directory watcher")
		return
	}
	defer watcher.Close()

	entries, ok := r.waitForPluginDirectory(dir, watcher, stopCh)
	if !ok {
		return // stopCh closed before dir ever became watchable
	}

	// A rate-limited, delaying queue of plugin (subdirectory) names rather than handling each
	// fsnotify event inline: it debounces bursts (AddAfter below) into one reload, and gives
	// transient failures (e.g. reading a file mid-write) a rate-limited retry via AddRateLimited
	// - see the invalid-plugin-directory branch in loadDiskPlugin.
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	// queue.ShutDown (deferred below) only makes the worker's next queue.Get return - it doesn't
	// wait for an in-flight processDiskPluginQueueItem, so without this WaitGroup the worker
	// goroutine could still be running (and logging through r.logger) after RunDirectoryWatch
	// itself has already returned. Deferred in this order (wg.Wait declared first, so it runs
	// last - defers unwind LIFO) so ShutDown unblocks queue.Get before Wait blocks for the
	// goroutine to actually exit.
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	defer queue.ShutDown()
	go func() {
		defer wg.Done()
		r.runDiskPluginWorker(dir, watcher, queue)
	}()

	for _, e := range entries {
		if isPluginSubdirectory(dir, e) {
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
			if event.Name == dir {
				// fsnotify drops the watch on the root itself (IN_DELETE_SELF/IN_MOVE_SELF on
				// inotify, from a rm -rf, a rename, or the volume being remounted) and never
				// re-adds it - watcher.Add(dir) is otherwise only ever called once, from
				// waitForPluginDirectory below main's single RunDirectoryWatch call, so without
				// this the select loop would keep running forever against a watcher that can
				// never deliver another event. Re-run the same wait/retry this function starts
				// with, then re-scan: a rm -rf reports each child individually and this only
				// races the recreation, but a rename/remount (fsnotify's inotify backend sends
				// only IN_MOVE_SELF, never IN_MOVED_FROM/TO) reports nothing for the children.
				//
				// So re-enqueue every name we're currently tracking, before waiting for dir to
				// come back: processDiskPluginQueueItem's os.Stat failure is what drops a plugin
				// that's no longer on disk, and dir may never reappear at all (a plain mv), in
				// which case anything still tracked would otherwise keep being served out of its
				// extraction cache for the life of the process.
				for _, name := range r.diskPluginNames() {
					queue.Add(name)
				}
				entries, ok := r.waitForPluginDirectory(dir, watcher, stopCh)
				if !ok {
					return // stopCh closed while waiting for dir to become watchable again
				}
				for _, e := range entries {
					if isPluginSubdirectory(dir, e) {
						queue.Add(e.Name())
					}
				}
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

// waitForPluginDirectory blocks until dir can both be watched and listed, retrying every
// dirWatchRetryDelay on either failing - most commonly dir not existing yet at startup (e.g. a
// slower-starting sidecar still provisioning the volume backing it), which would otherwise leave
// the directory source dead for the life of the process after a single startup error log:
// RunDirectoryWatch is launched exactly once, from cmd/server/main.go, with nothing else
// retrying it. Returns dir's initial entries and true once both succeed, or nil and false if
// stopCh closes first.
func (r *Registry) waitForPluginDirectory(dir string, watcher *fsnotify.Watcher, stopCh <-chan struct{}) ([]os.DirEntry, bool) {
	logged := false
	for {
		if err := watcher.Add(dir); err != nil {
			if !logged {
				r.logger.Error(err, "failed to watch plugin directory, will keep retrying", "directory", dir)
				logged = true
			}
		} else if entries, err := os.ReadDir(dir); err != nil {
			if !logged {
				r.logger.Error(err, "failed to read plugin directory, will keep retrying", "directory", dir)
				logged = true
			}
			_ = watcher.Remove(dir)
		} else {
			return entries, true
		}
		select {
		case <-stopCh:
			return nil, false
		case <-time.After(dirWatchRetryDelay):
		}
	}
}

// isPluginSubdirectory reports whether e (a direct child of dir) is a plugin subdirectory -
// either a real directory, or a symlink to one. os.ReadDir's DirEntry.IsDir() reflects an lstat,
// not a stat, so a symlink is never IsDir() regardless of what it points to; without the extra
// check here, a plugin directory delivered as a symlink (a common way to point
// plugins.directory's <name> subdirectory at another checkout during local development) would
// load once an fsnotify event happens to fire on it - processDiskPluginQueueItem's os.Stat does
// follow symlinks - but never at startup, since this is the only place that has to tell the two
// apart.
func isPluginSubdirectory(dir string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, e.Name()))
	return err == nil && info.IsDir()
}

// runDiskPluginWorker drains queue, (re)loading or dropping one plugin per item, until queue is
// shut down (RunDirectoryWatch returning closes it via its own defer).
//
// watcher.Add/Remove (called below, from this goroutine) are safe to call concurrently with
// RunDirectoryWatch's goroutine reading watcher.Events: both of fsnotify's inotify and kqueue
// backends guard their internal watch-list mutations with their own mutex (see fsnotify's
// backend_inotify.go/backend_kqueue.go) - this isn't a special case this code has to account for
// itself, it's how fsnotify.Watcher is meant to be used from multiple goroutines.
func (r *Registry) runDiskPluginWorker(dir string, watcher *fsnotify.Watcher, queue workqueue.TypedRateLimitingInterface[string]) {
	for {
		pluginName, shutdown := queue.Get()
		if shutdown {
			return
		}
		r.processDiskPluginQueueItem(dir, pluginName, watcher, queue)
		queue.Done(pluginName)
	}
}

func (r *Registry) processDiskPluginQueueItem(dir, pluginName string, watcher *fsnotify.Watcher, queue workqueue.TypedRateLimitingInterface[string]) {
	pluginDir := filepath.Join(dir, pluginName)
	if info, err := os.Stat(pluginDir); err != nil || !info.IsDir() {
		// The plugin's subdirectory itself is gone (removed or renamed away); drop it if we
		// were serving it. watcher.Remove errors if it was never watched (e.g. this event is
		// for a file, not the subdirectory) - harmless, nothing to clean up in that case.
		r.deleteDiskPlugin(pluginName)
		r.removeExtractedPluginDir(directorySourceName, pluginName)
		_ = watcher.Remove(pluginDir)
		queue.Forget(pluginName)
		return
	}
	if r.loadDiskPlugin(dir, pluginName, watcher) {
		queue.Forget(pluginName)
	} else if queue.NumRequeues(pluginName) >= maxPluginLoadRetries {
		// Every attempt has failed the same way loadDiskPlugin already logged - keep re-queuing a
		// permanently-invalid directory forever instead of only a transient one. Give up until
		// the next fsnotify event on this plugin (see maxPluginLoadRetries).
		r.logger.Error(fmt.Errorf("plugin directory failed to load after %d retries, giving up until it changes again", maxPluginLoadRetries), "giving up on plugin directory", "directory", pluginDir)
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
	if err != nil || rel == ".." || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return strings.SplitN(filepath.ToSlash(rel), "/", 2)[0], true
}

// loadDiskPlugin (re)loads the plugin bundle in rootDir/pluginName and watches that subdirectory
// for future file changes - fsnotify does not watch recursively, so a freshly-seen subdirectory
// needs its own explicit watch. Reports whether the load succeeded, so the caller can decide
// whether to retry.
func (r *Registry) loadDiskPlugin(rootDir, pluginName string, watcher *fsnotify.Watcher) bool {
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := watcher.Add(pluginDir); err != nil {
		r.logger.Error(err, "failed to watch plugin directory", "directory", pluginDir)
	}
	if !r.hasDiskCapacity(pluginName) {
		// Checked before parsing/extracting, not just before addDiskPlugin below: a cap
		// rejection is permanent for the current state of the world (nothing about the bundle
		// itself is wrong), so it shouldn't cost an extraction, and returning true here (instead
		// of false) skips processDiskPluginQueueItem's retry budget entirely rather than spending
		// it on a rejection that a retry can't fix.
		r.logger.Error(fmt.Errorf("plugins.maxDirectoryPlugins (%d) reached", r.maxDirectoryPlugins), "too many plugin directories, dropping", "directory", pluginDir)
		return true
	}
	dest, err := r.extractedPluginDir(directorySourceName, pluginName)
	if err != nil {
		r.logger.Error(err, "skipping plugin directory", "directory", pluginDir)
		return false
	}
	entry, err := parsePluginArchive(pluginDir, dest, r.maxBundleBytes)
	if err != nil {
		// Unlike the ConfigMap source (see handleUpsert), this deletes the tracked plugin (if
		// any) outright rather than keeping the last known-good version being served - a
		// pre-existing behavior difference between the two sources, not something introduced
		// here, and it's unconditional regardless of whether extractZip failed before or after
		// removing dest (see errDestGone in registry.go): either way, this plugin stops being
		// served until it loads successfully again.
		r.logger.Error(err, "skipping invalid plugin directory", "directory", pluginDir)
		r.deleteDiskPlugin(pluginName)
		r.removeExtractedPluginDir(directorySourceName, pluginName)
		return false
	}
	if !r.addDiskPlugin(pluginName, *entry) {
		r.logger.Error(fmt.Errorf("plugins.maxDirectoryPlugins (%d) reached", r.maxDirectoryPlugins), "too many plugin directories, dropping", "directory", pluginDir)
		r.removeExtractedPluginDir(directorySourceName, pluginName)
		return false
	}
	r.logger.Info("Loaded plugin from directory", "directory", pluginDir, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
	return true
}

// hasDiskCapacity reports whether name can be loaded without exceeding maxDirectoryPlugins - true
// if name is already tracked (an update is never blocked by the cap, only a new name) or the
// source has room for one more. Checked by loadDiskPlugin before parsing/extracting the plugin's
// bundle, so a plugin past the cap is rejected without spending a bundle extraction on it.
func (r *Registry) hasDiskCapacity(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, exists := r.diskPlugins[name]; exists {
		return true
	}
	return r.maxDirectoryPlugins <= 0 || len(r.diskPlugins) < r.maxDirectoryPlugins
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
	r.refreshResolvedEntriesLocked()
	return true
}

func (r *Registry) deleteDiskPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.diskPlugins, name)
	r.refreshResolvedEntriesLocked()
}

// diskPluginNames returns a snapshot of every plugin name currently tracked from the directory
// source, regardless of whether it's still on disk.
func (r *Registry) diskPluginNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.diskPlugins))
	for name := range r.diskPlugins {
		names = append(names, name)
	}
	return names
}

// parsePluginArchive reads pluginDir/manifest.json and pluginDir/bundle.zip, validates the
// manifest against the archive's file names (cheap: bundle.zip's central directory lists names
// without decompressing anything), and only then extracts bundle.zip into dest (see
// extractZip) - no reason to write a bundle to disk that's going to be rejected anyway.
func parsePluginArchive(pluginDir, dest string, maxBundleBytes int64) (*pluginEntry, error) {
	manifestData, err := os.ReadFile(filepath.Join(pluginDir, manifestFileName))
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", manifestFileName, err)
	}

	zr, err := zip.OpenReader(filepath.Join(pluginDir, bundleFileName))
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", bundleFileName, err)
	}
	defer zr.Close()

	manifest, err := validateManifest(manifestData, zipEntryNames(&zr.Reader))
	if err != nil {
		return nil, err
	}

	extractRoot, err := extractZip(&zr.Reader, dest, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, diskRoot: extractRoot}, nil
}
