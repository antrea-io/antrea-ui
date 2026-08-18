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

	"github.com/fsnotify/fsnotify"
)

// RunDirectoryWatch watches dir for plugin bundle subdirectories - one directory per plugin, each
// holding a manifest.json plus the files it references, the on-disk mirror of a plugin
// ConfigMap's Data. Meant for local development (no cluster round-trip to iterate on a plugin),
// and as a fallback if a deployment's plugins outgrow a ConfigMap's 1MiB cap and it mounts a
// shared volume here instead. A plugin subdirectory can be created, changed, or removed at any
// time; the registry reflects the change immediately, with no antrea-ui restart required. Blocks
// and should be called from a goroutine. A no-op if dir is empty.
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		r.logger.Error(err, "failed to read plugin directory", "directory", dir)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			r.loadDiskPlugin(dir, e.Name(), watcher)
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
			r.handleDirectoryEvent(dir, event, watcher)
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			r.logger.Error(err, "plugin directory watch error", "directory", dir)
		}
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

func (r *Registry) handleDirectoryEvent(dir string, event fsnotify.Event, watcher *fsnotify.Watcher) {
	pluginName, ok := pluginNameFromEventPath(dir, event.Name)
	if !ok {
		return
	}
	pluginDir := filepath.Join(dir, pluginName)
	if info, err := os.Stat(pluginDir); err != nil || !info.IsDir() {
		// The plugin's subdirectory itself is gone (removed or renamed away); drop it if we
		// were serving it. watcher.Remove errors if it was never watched (e.g. this event is
		// for a file, not the subdirectory) - harmless, nothing to clean up in that case.
		r.mu.Lock()
		delete(r.diskPlugins, pluginName)
		r.mu.Unlock()
		_ = watcher.Remove(pluginDir)
		return
	}
	r.loadDiskPlugin(dir, pluginName, watcher)
}

// loadDiskPlugin (re)loads the plugin bundle in rootDir/pluginName and watches that subdirectory
// for future file changes - fsnotify does not watch recursively, so a freshly-seen subdirectory
// needs its own explicit watch.
func (r *Registry) loadDiskPlugin(rootDir, pluginName string, watcher *fsnotify.Watcher) {
	pluginDir := filepath.Join(rootDir, pluginName)
	if err := watcher.Add(pluginDir); err != nil {
		r.logger.Error(err, "failed to watch plugin directory", "directory", pluginDir)
	}
	entry, err := parsePluginDirectory(pluginDir)
	if err != nil {
		r.logger.Error(err, "skipping invalid plugin directory", "directory", pluginDir)
		r.mu.Lock()
		delete(r.diskPlugins, pluginName)
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.diskPlugins[pluginName] = *entry
	r.mu.Unlock()
	r.logger.Info("Loaded plugin from directory", "directory", pluginDir, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
}

// parsePluginDirectory reads one plugin bundle directory - flat files only, no nested
// subdirectories, mirroring a ConfigMap's flat Data/BinaryData namespace - into a pluginEntry.
func parsePluginDirectory(dir string) (*pluginEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin directory: %w", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read %q: %w", e.Name(), err)
		}
		files[e.Name()] = data
	}
	return parseManifestAndFiles(files)
}
