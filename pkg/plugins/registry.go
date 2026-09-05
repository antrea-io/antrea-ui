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
// reflects the change on the next request, with no antrea-ui restart required. See
// configmaps.go for the ConfigMap source and disk.go for the directory source.
//
// Both sources ship a single bundle.zip rather than one file per key/directory entry: a plugin
// with subdirectory-nested assets (e.g. Angular's assets/ convention - images, i18n locale
// files, anything referenced by a relative runtime URL rather than pulled into the JS module
// graph) can't be represented as flat ConfigMap keys (the apiserver rejects "/" in a key name)
// or flat directory entries (this package used to skip subdirectories outright, mirroring that
// same flat-namespace convention) - a zip's own internal paths sidestep both restrictions
// without this package needing to special-case either "/" character.
//
// Both sources also serve their bundle from local disk rather than memory: a bundle.zip is
// extracted once, into a per-plugin, per-source subdirectory of a shared scratch cache (see
// diskCacheDir), and every subsequent file request reads straight from there. This requires a
// writable scratch directory (a real /tmp, or a mounted emptyDir if the container's root
// filesystem is read-only) - see docs/plugins.md.
package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	apisv1 "antrea.io/antrea-ui/apis/v1"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
)

const (
	manifestFileName = "manifest.json"
	bundleFileName   = "bundle.zip"

	// configMapSourceName/directorySourceName namespace sortedEntries' sort keys and
	// diskCacheDir's on-disk layout - the same two strings serve both jobs, so a ConfigMap and a
	// directory plugin sharing a literal name never collide either as an index entry or as an
	// extraction directory.
	configMapSourceName = "configmap"
	directorySourceName = "directory"

	// maxPluginLoadRetries bounds the rate-limited retries either source's queue worker gives a
	// plugin that fails to load. Past this many attempts, one that fires transiently (e.g. a
	// ConfigMap update racing an in-flight edit, or a directory read mid-write) has long since
	// succeeded - what's left re-queuing forever is a permanently-invalid one (a malformed
	// manifest.json, a missing bundle.zip, ...), which would otherwise settle into an indefinite
	// background error log (DefaultTypedControllerRateLimiter caps the backoff at 1000s) for the
	// rest of the process's life. A later event for the same plugin re-enqueues it from scratch,
	// so giving up here costs nothing once it's actually fixed.
	maxPluginLoadRetries = 5
)

// pluginEntry is one loaded plugin bundle, always served from its own extracted-bundle.zip
// directory on local disk (see diskCacheDir) - never held decoded in memory, regardless of
// source.
type pluginEntry struct {
	manifest apisv1.PluginManifest
	diskRoot string
	// resourceVersion is the ConfigMap this entry was parsed from's own ResourceVersion; empty
	// for directory-sourced entries, which have no equivalent concept. Lets handleUpsert skip a
	// redundant re-extraction when an Update event fires for a ConfigMap whose content hasn't
	// actually changed - most commonly the informer replaying its cache after a watch reconnect,
	// which redelivers every object as an Update (through this same handler) even though nothing
	// about it changed at all.
	resourceVersion string
}

// open returns a reader (and its size, for Content-Length) for filename within this plugin's
// bundle. Callers must Close the returned ReadCloser. ok is false if filename isn't part of the
// bundle.
func (e *pluginEntry) open(filename string) (io.ReadCloser, int64, bool) {
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
	// maxBundleBytes bounds how much a single plugin's bundle.zip may decompress to in total,
	// shared by both sources rather than a separate limit each - a plugin directory carries
	// about as much trust as a plugin ConfigMap, so there's no reason for the two to differ.
	// Checked while extracting (extractZip) rather than after the fact - a backstop against a
	// "zip bomb". Zero means unbounded.
	maxBundleBytes int64

	mu          sync.RWMutex
	plugins     map[string]pluginEntry // keyed by the backing ConfigMap's name
	diskPlugins map[string]pluginEntry // keyed by the backing directory's name
	// resolved and claimed are both derived from plugins/diskPlugins (see
	// refreshResolvedEntriesLocked), recomputed on every mutation of either map rather than on
	// every Index()/File() call - File() in particular backs an unauthenticated HTTP route, so
	// redoing the sort/dedup/route-collision work (and its log-per-duplicate lines) on every
	// request would be an easy way for a misconfigured pair of plugins to turn routine traffic
	// into log spam.
	//
	// resolved is what Index() lists: one pluginEntry per manifest name, already deduplicated
	// and with any colliding federation routes filtered out. claimed is a superset of resolved's
	// keys: every name currently claimed by some source, including one whose sole entry lost
	// every one of its federation routes to an earlier collision and so holds the name without
	// being listed in resolved (see Index's doc comment) - File() resolves through claimed, not
	// resolved, so it still serves that entry's files even though Index() doesn't list it.
	resolved map[string]pluginEntry
	claimed  map[string]sourcedEntry

	// cacheRoot is the local scratch directory extracted plugin bundles (both sources) are
	// written to and served from, created lazily on first use (see diskCacheDir) - a deployment
	// using only the ConfigMap source never calls RunDirectoryWatch, which used to be what
	// created and owned this directory. Guarded by its own mutex rather than a sync.Once: a
	// transient MkdirTemp failure (e.g. /tmp not yet mounted, momentary ENOSPC) must not
	// permanently latch both plugin sources into a failed state for the life of the process, the
	// way a sync.Once caching its own error would. A long-lived server process never removes it -
	// the OS/container runtime reclaims it along with the rest of the container's writable
	// filesystem on exit - but Close lets a short-lived Registry (tests) do so explicitly.
	cacheRootMu sync.Mutex
	cacheRoot   string
}

func NewRegistry(logger logr.Logger, clientset kubernetes.Interface, namespace, labelSelector string, maxConfigMapPlugins, maxDirectoryPlugins int, maxBundleBytes int64) *Registry {
	return &Registry{
		logger:              logger,
		clientset:           clientset,
		namespace:           namespace,
		labelSelector:       labelSelector,
		maxConfigMapPlugins: maxConfigMapPlugins,
		maxDirectoryPlugins: maxDirectoryPlugins,
		maxBundleBytes:      maxBundleBytes,
		plugins:             make(map[string]pluginEntry),
		diskPlugins:         make(map[string]pluginEntry),
	}
}

// diskCacheDir returns the shared local scratch directory extracted plugin bundles live under,
// creating it on the first successful call from either source. A failed attempt isn't cached -
// the next call (the next ConfigMap/directory event) tries again, so a transient failure doesn't
// take both plugin sources down for good.
func (r *Registry) diskCacheDir() (string, error) {
	r.cacheRootMu.Lock()
	defer r.cacheRootMu.Unlock()
	if r.cacheRoot != "" {
		return r.cacheRoot, nil
	}
	cacheRoot, err := os.MkdirTemp("", "antrea-ui-plugins-*")
	if err != nil {
		return "", err
	}
	r.cacheRoot = cacheRoot
	return r.cacheRoot, nil
}

// Close removes the registry's extracted-plugin scratch directory, if diskCacheDir ever created
// one. A long-lived server process has no need to call this - the container/OS reclaims the
// whole writable filesystem on exit - but tests build many short-lived Registrys
// (newTestRegistry, testServer) that would otherwise each leak a MkdirTemp under the real /tmp.
func (r *Registry) Close() {
	r.cacheRootMu.Lock()
	defer r.cacheRootMu.Unlock()
	if r.cacheRoot == "" {
		return
	}
	if err := os.RemoveAll(r.cacheRoot); err != nil {
		r.logger.Error(err, "failed to remove plugin extraction cache directory", "directory", r.cacheRoot)
	}
	r.cacheRoot = ""
}

// extractedPluginDir returns the local scratch directory name's bundle.zip is (or would be)
// extracted into, namespaced by source (configMapSourceName/directorySourceName) so a ConfigMap
// and a directory plugin sharing a literal name never collide on disk, the same way
// sortedEntries already keeps them from colliding as index entries.
func (r *Registry) extractedPluginDir(source, name string) (string, error) {
	cacheRoot, err := r.diskCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to create plugin extraction cache directory: %w", err)
	}
	return filepath.Join(cacheRoot, source, name), nil
}

// removeExtractedPluginDir best-effort removes name's extracted bundle under source, if any -
// called whenever a plugin stops being tracked (deleted, or failed to register after a
// successful extraction) so the cache directory doesn't accumulate content for plugins the
// registry isn't serving. Errors are logged, not returned: this is cleanup, not something
// callers should fail over.
func (r *Registry) removeExtractedPluginDir(source, name string) {
	cacheRoot, err := r.diskCacheDir()
	if err != nil {
		return // nothing was ever extracted if the cache dir itself couldn't be created
	}
	if err := os.RemoveAll(filepath.Join(cacheRoot, source, name)); err != nil {
		r.logger.Error(err, "failed to remove extracted plugin directory", "source", source, "plugin", name)
	}
}

// sourcedEntry pairs a pluginEntry with the deterministic sort key ("configmap/<name>" or
// "directory/<name>") Index/File use to resolve a plugin-name or federation-route-path collision
// to a single winner - mirrors the frontend's dedupeByPath in plugins.ts. ConfigMap-backed keys
// sort before directory-backed ones, so a ConfigMap always wins a collision against a
// same-named directory plugin.
type sourcedEntry struct {
	sortKey string
	entry   pluginEntry
}

// sortedEntries returns every currently known plugin entry from both sources, in the order
// Index/File resolve collisions against (see sourcedEntry). Callers must hold at least
// r.mu.RLock().
func (r *Registry) sortedEntries() []sourcedEntry {
	all := make([]sourcedEntry, 0, len(r.plugins)+len(r.diskPlugins))
	for name, entry := range r.plugins {
		all = append(all, sourcedEntry{sortKey: configMapSourceName + "/" + name, entry: entry})
	}
	for name, entry := range r.diskPlugins {
		all = append(all, sourcedEntry{sortKey: directorySourceName + "/" + name, entry: entry})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sortKey < all[j].sortKey })
	return all
}

// refreshResolvedEntriesLocked recomputes r.resolved and r.claimed from sortedEntries.
//
// If two sources declare the same plugin name, whichever sorts first (see sortedEntries) wins
// outright; the other is dropped (and logged) in full, including its 'entry' - File would
// otherwise still serve files for the loser, which resolved (and so Index) no longer lists.
//
// If two different plugins' federation routes collide on path, only the colliding routes are
// dropped from the later plugin (by that same sort order), not the whole manifest - unlike the
// plugin-name case, 'entry' is still eagerly import()ed by every listed manifest regardless of
// 'federation' (see PluginManifest.Entry), so dropping the manifest would cost that plugin's
// page-extension registrations over a route only an out-of-tree, federation-aware host ever
// mounts. This differs from the frontend's dedupeByPath in plugins.ts in the same way:
// dedupeByPath also drops individual routes rather than a whole plugin, but its winner is
// registration order (whichever plugin's entry module ran first), not this sort order. If every
// one of a plugin's routes collides, its federation is left with no routes to serve, which is as
// meaningless as the empty-routes case validateManifest already rejects, so the whole manifest
// is dropped from resolved instead - same as the plugin-name case - though it still claims the
// name (see claimed's doc comment on the Registry struct).
//
// Called after every mutation of either map (addConfigMapPlugin/deleteConfigMapPlugin/
// addDiskPlugin/deleteDiskPlugin) rather than from Index()/File() on every call: File() in
// particular backs an unauthenticated HTTP route, so redoing this sort/dedup/route-collision
// work (and its log-per-duplicate lines) on every request would turn routine traffic against a
// misconfigured pair of plugins into log spam. Callers must hold r.mu for writing.
func (r *Registry) refreshResolvedEntriesLocked() {
	seenRoutePaths := make(map[string]string)  // normalized route path -> plugin name that claimed it
	seenRouteOwners := make(map[string]string) // normalized path of a claimed PluginRouteKindRoutes route -> plugin name that claimed it
	all := r.sortedEntries()
	claimed := make(map[string]sourcedEntry, len(all))
	resolved := make(map[string]pluginEntry, len(all))
	for _, s := range all {
		if owner, ok := claimed[s.entry.manifest.Name]; ok {
			r.logger.Info("Duplicate plugin name, dropping", "plugin", s.entry.manifest.Name, "source", s.sortKey, "keptSource", owner.sortKey)
			continue
		}

		manifest := s.entry.manifest
		if manifest.Federation != nil {
			kept := make([]apisv1.PluginRoute, 0, len(manifest.Federation.Routes))
			for _, route := range manifest.Federation.Routes {
				normalized := normalizeRoutePath(route.Path)
				if owner, ok := seenRoutePaths[normalized]; ok {
					r.logger.Info("Plugin's federation route path collides with an earlier plugin, dropping the route", "plugin", manifest.Name, "source", s.sortKey, "path", route.Path, "collidesWithPlugin", owner)
					continue
				}
				if ownerPath, owner, ok := findRouteOwner(seenRouteOwners, normalized); ok {
					r.logger.Info("Plugin's federation route path falls under an earlier plugin's route-tree-owning route, dropping the route", "plugin", manifest.Name, "source", s.sortKey, "path", route.Path, "collidesWithPlugin", owner, "ownerPath", ownerPath)
					continue
				}
				if route.Kind == apisv1.PluginRouteKindRoutes {
					if nestedPath, owner, ok := findRouteUnder(seenRoutePaths, normalized); ok {
						r.logger.Info("Plugin's route-tree-owning federation route already contains an earlier plugin's route, dropping the route", "plugin", manifest.Name, "source", s.sortKey, "path", route.Path, "collidesWithPlugin", owner, "nestedPath", nestedPath)
						continue
					}
				}
				seenRoutePaths[normalized] = manifest.Name
				if route.Kind == apisv1.PluginRouteKindRoutes {
					seenRouteOwners[normalized] = manifest.Name
				}
				kept = append(kept, route)
			}
			if len(kept) == 0 {
				r.logger.Info("All of plugin's federation routes collided with an earlier plugin, dropping the plugin", "plugin", manifest.Name, "source", s.sortKey)
				// Still claim the name: this is the source File() should still resolve it
				// to, and leaving it unclaimed would let a later entry reusing the same
				// name win the name outright here, while File() (via claimed) kept
				// resolving it to this one.
				claimed[manifest.Name] = s
				continue
			}
			if len(kept) != len(manifest.Federation.Routes) {
				federation := *manifest.Federation
				federation.Routes = kept
				manifest.Federation = &federation
			}
		}

		s.entry.manifest = manifest
		claimed[manifest.Name] = s
		resolved[manifest.Name] = s.entry
	}
	r.resolved = resolved
	r.claimed = claimed
}

// Index returns the current set of plugin manifests, deduplicated by manifest name and by
// federation route path, across both the ConfigMap and directory sources (see
// refreshResolvedEntriesLocked), sorted by name for a deterministic response.
func (r *Registry) Index() []apisv1.PluginManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.resolved))
	for name := range r.resolved {
		names = append(names, name)
	}
	sort.Strings(names)
	manifests := make([]apisv1.PluginManifest, 0, len(r.resolved))
	for _, name := range names {
		manifests = append(manifests, r.resolved[name].manifest)
	}
	return manifests
}

// File returns a reader (and its size in bytes, for Content-Length) for filename belonging to
// the plugin named pluginName, as currently known to the registry. Callers must Close the
// returned ReadCloser. When the plugin name is claimed by more than one source, it resolves to
// the source that claimed the name in Index() - usually the one whose manifest Index() lists,
// except when every one of that manifest's federation routes collided and Index() dropped it
// entirely while still leaving it holding the name (see refreshResolvedEntriesLocked).
func (r *Registry) File(pluginName, filename string) (io.ReadCloser, int64, bool) {
	r.mu.RLock()
	claimed, ok := r.claimed[pluginName]
	r.mu.RUnlock()
	if !ok {
		return nil, 0, false
	}
	return claimed.entry.open(filename)
}
