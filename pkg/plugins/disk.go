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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/client-go/util/workqueue"
)

// requeueDelay debounces a burst of fsnotify events for the same plugin (an editor's
// temp-file-swap on save, or a multi-file build writing several files in quick succession) into
// one reload instead of one per individual event. A var, not a const, so tests can shrink it.
var requeueDelay = time.Second

// maxDiskPluginBundleBytes bounds how much of a single plugin's directory parsePluginDirectory
// will load into memory. A ConfigMap-backed plugin is bounded for free by etcd's own ~1MiB
// object size limit; a plugin directory has no such limit built into its delivery mechanism, so
// without this check a misbehaving build (or anyone with write access to the shared volume)
// could make the backend read an arbitrarily large amount of content onto the Go heap on every
// reload. Checked via os.Stat in parsePluginDirectory - before any file content is read - so an
// oversized plugin costs a few stat syscalls, not a multi-gigabyte allocation. A var, not a
// const, so tests can shrink it instead of writing a real 20MiB fixture.
var maxDiskPluginBundleBytes int64 = 20 * 1024 * 1024 // 20MiB

// RunDirectoryWatch watches dir for plugin bundle subdirectories - one directory per plugin, each
// holding a manifest.json plus the files it references, the on-disk mirror of a plugin
// ConfigMap's Data. Meant for local development (no cluster round-trip to iterate on a plugin),
// and as a fallback if a deployment's plugins outgrow a ConfigMap's 1MiB cap and it mounts a
// shared volume here instead. A plugin subdirectory can be created, changed, or removed at any
// time; the registry reflects the change shortly after (see requeueDelay), with no antrea-ui
// restart required. Blocks and should be called from a goroutine. A no-op if dir is empty.
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
	go r.runDiskPluginWorker(dir, watcher, queue)

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
		_ = watcher.Remove(pluginDir)
		queue.Forget(pluginName)
		return
	}
	if r.loadDiskPlugin(dir, pluginName, watcher) {
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
func (r *Registry) loadDiskPlugin(rootDir, pluginName string, watcher *fsnotify.Watcher) bool {
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := watcher.Add(pluginDir); err != nil {
		r.logger.Error(err, "failed to watch plugin directory", "directory", pluginDir)
	}
	entry, err := parsePluginDirectory(pluginDir)
	if err != nil {
		r.logger.Error(err, "skipping invalid plugin directory", "directory", pluginDir)
		r.deleteDiskPlugin(pluginName)
		return false
	}
	r.addDiskPlugin(pluginName, *entry)
	r.logger.Info("Loaded plugin from directory", "directory", pluginDir, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
	return true
}

func (r *Registry) addDiskPlugin(name string, entry pluginEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.diskPlugins[name] = entry
}

func (r *Registry) deleteDiskPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.diskPlugins, name)
}

// parsePluginDirectory reads one plugin bundle directory - flat files only, no nested
// subdirectories, mirroring a ConfigMap's flat Data/BinaryData namespace - into a pluginEntry.
// Stats every file before reading any of them, rejecting the whole bundle up front if their
// combined size would exceed maxDiskPluginBundleBytes (see that constant's comment for why a
// directory needs this check where a ConfigMap doesn't).
func parsePluginDirectory(dir string) (*pluginEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin directory: %w", err)
	}

	type statted struct {
		name string
		size int64
	}
	toRead := make([]statted, 0, len(entries))
	var totalSize int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("failed to stat %q: %w", e.Name(), err)
		}
		totalSize += info.Size()
		if totalSize > maxDiskPluginBundleBytes {
			return nil, fmt.Errorf("plugin bundle exceeds the %d byte limit", maxDiskPluginBundleBytes)
		}
		toRead = append(toRead, statted{name: e.Name(), size: info.Size()})
	}

	files := make(map[string][]byte, len(toRead))
	for _, f := range toRead {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return nil, fmt.Errorf("failed to read %q: %w", f.name, err)
		}
		// A file that grew between the stat above and this read would otherwise let a bundle
		// slip past the size check it just passed; fail loudly rather than silently accept a
		// bundle whose actual size was never checked. The next reload (this one gets retried,
		// see processDiskPluginQueueItem) re-stats from scratch.
		if int64(len(data)) != f.size {
			return nil, fmt.Errorf("%q changed size while being read", f.name)
		}
		files[f.name] = data
	}
	return parseManifestAndFiles(files)
}
