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

// Package plugins discovers frontend plugin bundles (a manifest.json plus a bundle.zip holding
// the plugin's JS entry file and everything else it references) from two sources - labeled
// ConfigMaps, and optionally a filesystem directory - and keeps an in-memory index that the
// backend's /api/v1/plugins routes serve. Either source can change at any time; the registry
// reflects the change on the next request, with no antrea-ui restart required. See registry.go
// for the ConfigMap source and disk.go for the directory source.
//
// Both sources ship a single bundle.zip rather than one file per key/directory entry: a plugin
// with subdirectory-nested assets (e.g. Angular's assets/ convention - images, i18n locale
// files, anything referenced by a relative runtime URL rather than pulled into the JS module
// graph) can't be represented as flat ConfigMap keys (the apiserver rejects "/" in a key name)
// or flat directory entries (this package used to skip subdirectories outright, mirroring that
// same flat-namespace convention) - a zip's own internal paths sidestep both restrictions
// without this package needing to special-case either "/" character.
package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	manifestFileName = "manifest.json"
	bundleFileName   = "bundle.zip"
)

// pluginEntry is one loaded plugin bundle. Exactly one of files/diskRoot is set:
//   - files backs ConfigMap-sourced entries, decoded fully into memory. A ConfigMap object is
//     bounded for free by etcd's own ~1MiB size limit, but that only bounds the compressed
//     bundle.zip bytes, not what they decompress to (see readZipFiles's maxConfigMapBundleBytes) -
//     with that budget enforced, holding the result in memory costs little, and there is no
//     on-disk location to serve it from anyway - it never existed as a file, only as bytes in a
//     Kubernetes API object.
//   - diskRoot backs directory-sourced entries: bundle.zip is extracted once, into this
//     directory (never the plugin's own source directory - see disk.go), and served straight
//     from there. A plugin directory has no equivalent size limit, so unlike ConfigMap, decoding
//     its entire bundle into memory on every load is a real, unbounded-in-practice cost this
//     avoids entirely.
type pluginEntry struct {
	manifest apisv1.PluginManifest
	files    map[string][]byte
	diskRoot string
}

// open returns a reader (and its size, for Content-Length) for filename within this plugin's
// bundle. Callers must Close the returned ReadCloser. ok is false if filename isn't part of the
// bundle.
func (e *pluginEntry) open(filename string) (io.ReadCloser, int64, bool) {
	if e.files != nil {
		data, ok := e.files[filename]
		if !ok {
			return nil, 0, false
		}
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), true
	}
	f, err := os.Open(safeJoin(e.diskRoot, filename))
	if err != nil {
		return nil, 0, false
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return nil, 0, false
	}
	return f, info.Size(), true
}

// safeJoin joins root and name the way net/http.Dir does: name is treated as rooted (as if it
// had a leading "/") before being cleaned, so any number of ".." segments collapse instead of
// escaping root - the standard technique for turning a URL path segment (pluginEntry.open, from
// an unauthenticated HTTP request) or an untrusted archive entry name (disk.go's extraction,
// "zip slip") into a safe local filesystem path.
func safeJoin(root, name string) string {
	cleaned := path.Clean("/" + filepath.ToSlash(name))
	return filepath.Join(root, filepath.FromSlash(cleaned))
}

type Registry struct {
	logger        logr.Logger
	clientset     kubernetes.Interface
	namespace     string
	labelSelector string
	// maxConfigMapPlugins/maxDirectoryPlugins cap how many plugins each source may register at
	// once. A new (not already-tracked) plugin past the cap is rejected and logged; updates to
	// an already-tracked plugin are never blocked by it. Zero means unbounded.
	maxConfigMapPlugins int
	maxDirectoryPlugins int
	// maxConfigMapBundleBytes bounds how much a ConfigMap-sourced bundle.zip may decompress to in
	// total, checked while decompressing (readZipFiles) rather than after the fact - a backstop
	// against a "zip bomb" now that this source's content is compressed, not raw bytes handed
	// straight from the ConfigMap the way it used to be. Zero means unbounded.
	maxConfigMapBundleBytes int64

	mu          sync.RWMutex
	plugins     map[string]pluginEntry // keyed by the backing ConfigMap's name
	diskPlugins map[string]pluginEntry // keyed by the backing directory's name
}

func NewRegistry(logger logr.Logger, clientset kubernetes.Interface, namespace, labelSelector string, maxConfigMapPlugins, maxDirectoryPlugins int, maxConfigMapBundleBytes int64) *Registry {
	return &Registry{
		logger:                  logger,
		clientset:               clientset,
		namespace:               namespace,
		labelSelector:           labelSelector,
		maxConfigMapPlugins:     maxConfigMapPlugins,
		maxDirectoryPlugins:     maxDirectoryPlugins,
		maxConfigMapBundleBytes: maxConfigMapBundleBytes,
		plugins:                 make(map[string]pluginEntry),
		diskPlugins:             make(map[string]pluginEntry),
	}
}

// Run watches ConfigMaps matching the registry's namespace and label
// selector until stopCh is closed. It blocks and should be called from a
// goroutine.
func (r *Registry) Run(stopCh <-chan struct{}) {
	factory := informers.NewSharedInformerFactoryWithOptions(
		r.clientset,
		0,
		informers.WithNamespace(r.namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.LabelSelector = r.labelSelector
		}),
	)
	informer := factory.Core().V1().ConfigMaps().Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.handleUpsert,
		UpdateFunc: func(_, newObj interface{}) { r.handleUpsert(newObj) },
		DeleteFunc: r.handleDelete,
	}); err != nil {
		r.logger.Error(err, "failed to register plugin ConfigMap event handler")
		return
	}
	r.logger.Info("Starting plugin ConfigMap watch", "namespace", r.namespace, "labelSelector", r.labelSelector)
	informer.Run(stopCh)
}

func asConfigMap(obj interface{}) *corev1.ConfigMap {
	if cm, ok := obj.(*corev1.ConfigMap); ok {
		return cm
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if cm, ok := tombstone.Obj.(*corev1.ConfigMap); ok {
			return cm
		}
	}
	return nil
}

func (r *Registry) handleUpsert(obj interface{}) {
	cm := asConfigMap(obj)
	if cm == nil {
		return
	}
	entry, err := parsePluginConfigMap(cm, r.maxConfigMapBundleBytes)
	if err != nil {
		r.logger.Error(err, "skipping invalid plugin ConfigMap", "configMap", cm.Name)
		return
	}
	if !r.addConfigMapPlugin(cm.Name, *entry) {
		r.logger.Error(fmt.Errorf("plugins.maxConfigMapPlugins (%d) reached", r.maxConfigMapPlugins), "too many plugin ConfigMaps, dropping", "configMap", cm.Name)
		return
	}
	r.logger.Info("Loaded plugin from ConfigMap", "configMap", cm.Name, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
}

func (r *Registry) handleDelete(obj interface{}) {
	cm := asConfigMap(obj)
	if cm == nil {
		return
	}
	r.deleteConfigMapPlugin(cm.Name)
	r.logger.Info("Removed plugin ConfigMap", "configMap", cm.Name)
}

// addConfigMapPlugin records entry under name, unless name is new and the source is already at
// maxConfigMapPlugins - reports whether it was recorded.
func (r *Registry) addConfigMapPlugin(name string, entry pluginEntry) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; !exists && r.maxConfigMapPlugins > 0 && len(r.plugins) >= r.maxConfigMapPlugins {
		return false
	}
	r.plugins[name] = entry
	return true
}

func (r *Registry) deleteConfigMapPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, name)
}

func parsePluginConfigMap(cm *corev1.ConfigMap, maxBundleBytes int64) (*pluginEntry, error) {
	manifestData, ok := cm.Data[manifestFileName]
	if !ok {
		return nil, fmt.Errorf("missing %s", manifestFileName)
	}
	bundleData, ok := cm.BinaryData[bundleFileName]
	if !ok {
		return nil, fmt.Errorf("missing %s", bundleFileName)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleData), int64(len(bundleData)))
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", bundleFileName, err)
	}
	files, err := readZipFiles(zr, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(files))
	for name := range files {
		names[name] = true
	}
	manifest, err := validateManifest([]byte(manifestData), names)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, files: files}, nil
}

// readZipFiles decompresses every regular file entry of zr into memory, up to a combined
// maxBundleBytes across all of them (zero means unbounded) - checked while decompressing rather
// than after the fact, so a small, maliciously high-ratio entry can't balloon into an unbounded
// heap allocation before this ever gets a chance to reject it (a "zip bomb": the ConfigMap
// carrying bundleData is itself size-bounded by etcd, but that bounds the compressed bytes, not
// what they decompress to). Only used for the ConfigMap source - see pluginEntry's doc comment
// for why holding the result in memory is fine there specifically.
func readZipFiles(zr *zip.Reader, maxBundleBytes int64) (map[string][]byte, error) {
	files := make(map[string][]byte, len(zr.File))
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %q in %s: %w", f.Name, bundleFileName, err)
		}
		data, err := readZipFileBudgeted(rc, f.Name, maxBundleBytes, total)
		rc.Close()
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		files[f.Name] = data
	}
	return files, nil
}

// readZipFileBudgeted reads rc (one bundle.zip entry named name) fully, unless maxBundleBytes is
// positive and reading it would push the running total (spent so far, across every entry read
// before this one) past that limit - in which case it stops early and returns an error instead of
// finishing the read.
func readZipFileBudgeted(rc io.Reader, name string, maxBundleBytes, spent int64) ([]byte, error) {
	if maxBundleBytes <= 0 {
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("failed to read %q from %s: %w", name, bundleFileName, err)
		}
		return data, nil
	}
	remaining := maxBundleBytes - spent
	data, err := io.ReadAll(io.LimitReader(rc, remaining+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read %q from %s: %w", name, bundleFileName, err)
	}
	if int64(len(data)) > remaining {
		return nil, fmt.Errorf("%s decompresses past the %d byte limit while reading %q, refusing to read further (possible zip bomb)", bundleFileName, maxBundleBytes, name)
	}
	return data, nil
}

// validateManifest parses manifestJSON and checks that every file it references (Entry,
// Federation.RemoteEntry) is present in the bundle - names is just the bundle's file name set,
// not its content, since disk.go's caller only has names cheaply available (from bundle.zip's
// central directory) before deciding whether the bundle is even worth extracting to disk.
func validateManifest(manifestJSON []byte, names map[string]bool) (*apisv1.PluginManifest, error) {
	var manifest apisv1.PluginManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", manifestFileName, err)
	}
	if manifest.Name == "" {
		return nil, fmt.Errorf("manifest is missing 'name'")
	}
	if manifest.Entry == "" {
		return nil, fmt.Errorf("manifest is missing 'entry'")
	}
	if !names[manifest.Entry] {
		return nil, fmt.Errorf("entry file %q referenced by manifest not found in %s", manifest.Entry, bundleFileName)
	}
	if manifest.Federation != nil {
		if manifest.Federation.RemoteEntry == "" {
			return nil, fmt.Errorf("manifest's federation is missing 'remoteEntry'")
		}
		if !names[manifest.Federation.RemoteEntry] {
			return nil, fmt.Errorf("remote entry file %q referenced by manifest's federation not found in %s", manifest.Federation.RemoteEntry, bundleFileName)
		}
		seenPaths := make(map[string]bool, len(manifest.Federation.Routes))
		for i, route := range manifest.Federation.Routes {
			if route.Path == "" {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d]' is missing 'path'", i)
			}
			if route.SidebarLabel == "" {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d]' is missing 'sidebarLabel'", i)
			}
			if route.ExposedModule == "" {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d]' is missing 'exposedModule'", i)
			}
			// The one reservation the backend can enforce on a route's Path itself - "api/" is
			// its own reserved prefix (see GetPluginFile), not something specific to any one
			// frontend. A route colliding with a given host's own built-in pages (e.g.
			// "/settings") is instead the host's job to reject, the same way it already does for
			// code-registered routes (see plugins.ts's RESERVED_PATHS/dedupeByPath in either
			// frontend) - the backend has no way to know a given host's built-in path list.
			if trimmed := strings.TrimPrefix(route.Path, "/"); strings.HasPrefix(trimmed, "api/") {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].path' %q falls under the reserved 'api/' prefix", i, route.Path)
			}
			if seenPaths[route.Path] {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].path' %q duplicates an earlier route in the same manifest", i, route.Path)
			}
			seenPaths[route.Path] = true
		}
	}
	return &manifest, nil
}

// resolvedEntries merges the ConfigMap and directory sources into one set of pluginEntry values,
// keyed by plugin name, deduplicated the same way Index/File always have: when two sources
// declare the same plugin name, whichever source key sorts first wins and the other is dropped
// (and logged) - mirrors the frontend's dedupeByPath in plugins.ts. ConfigMap-backed keys are
// compared as "configmap/<name>" and directory-backed keys as "directory/<name>", so a ConfigMap
// and a directory sharing a literal name never collide with each other. Callers must hold at
// least r.mu.RLock().
func (r *Registry) resolvedEntries() map[string]pluginEntry {
	type sourced struct {
		sortKey string
		entry   pluginEntry
	}
	all := make([]sourced, 0, len(r.plugins)+len(r.diskPlugins))
	for name, entry := range r.plugins {
		all = append(all, sourced{sortKey: "configmap/" + name, entry: entry})
	}
	for name, entry := range r.diskPlugins {
		all = append(all, sourced{sortKey: "directory/" + name, entry: entry})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sortKey < all[j].sortKey })

	winnerSortKey := make(map[string]string, len(all)) // plugin name -> source key that claimed it
	resolved := make(map[string]pluginEntry, len(all))
	for _, s := range all {
		if owner, ok := winnerSortKey[s.entry.manifest.Name]; ok {
			r.logger.Info("Duplicate plugin name, dropping", "plugin", s.entry.manifest.Name, "source", s.sortKey, "keptSource", owner)
			continue
		}
		winnerSortKey[s.entry.manifest.Name] = s.sortKey
		resolved[s.entry.manifest.Name] = s.entry
	}
	return resolved
}

// Index returns the current set of plugin manifests, deduplicated by manifest name across both
// the ConfigMap and directory sources (see resolvedEntries), sorted by name for a deterministic
// response.
func (r *Registry) Index() []apisv1.PluginManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolved := r.resolvedEntries()
	names := make([]string, 0, len(resolved))
	for name := range resolved {
		names = append(names, name)
	}
	sort.Strings(names)
	manifests := make([]apisv1.PluginManifest, 0, len(resolved))
	for _, name := range names {
		manifests = append(manifests, resolved[name].manifest)
	}
	return manifests
}

// File returns a reader (and its size in bytes, for Content-Length) for filename belonging to
// the plugin named pluginName, as currently known to the registry. Callers must Close the
// returned ReadCloser. When the plugin name is claimed by more than one source, it resolves to
// the same one Index() would keep.
func (r *Registry) File(pluginName, filename string) (io.ReadCloser, int64, bool) {
	r.mu.RLock()
	entry, ok := r.resolvedEntries()[pluginName]
	r.mu.RUnlock()
	if !ok {
		return nil, 0, false
	}
	return entry.open(filename)
}
