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
//
// Both sources also serve their bundle from local disk rather than memory: a bundle.zip is
// extracted once, into a per-plugin, per-source subdirectory of a shared scratch cache (see
// diskCacheDir), and every subsequent file request reads straight from there. This requires a
// writable scratch directory (a real /tmp, or a mounted emptyDir if the container's root
// filesystem is read-only) - see docs/plugins.md.
package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	"k8s.io/client-go/util/workqueue"

	apisv1 "antrea.io/antrea-ui/apis/v1"
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

// reservedRoutePrefixes mirrors the nginx config's location blocks
// (_nginx_conf.tpl: "location /api", "location /auth"), which are plain
// string prefixes, not path-segment matches: nginx proxies any URI
// beginning with "/api" or "/auth" - "/apidocs", "/api", "/authors" all
// included - straight to the backend, bypassing the SPA. A manifest route
// under one of these would install and navigate fine client-side, then
// 404 on a hard refresh or a direct link.
var reservedRoutePrefixes = []string{"api", "auth"}

// normalizeRoutePath collapses a route path to the form used for reservation
// and duplicate checks, so "/policies", "policies", "//policies" and
// "/policies/" are all recognized as the same path (and ".." segments can't
// be used to escape the comparison).
func normalizeRoutePath(p string) string {
	return strings.Trim(path.Clean("/"+p), "/")
}

// findRouteOwner reports whether normalized (already run through
// normalizeRoutePath) falls under one of owners, a set of normalized paths
// of PluginRouteKindRoutes routes already claimed in Index's dedupe loop.
// Those routes own their whole subtree (see parsePluginConfigMap's own
// subtree check for a single manifest), so a later plugin's route nested
// under one is the same cross-plugin collision seenRoutePaths catches for an
// exact path match, just spelled differently.
func findRouteOwner(owners map[string]string, normalized string) (string, string, bool) {
	for ownerPath, plugin := range owners {
		if strings.HasPrefix(normalized, ownerPath+"/") {
			return ownerPath, plugin, true
		}
	}
	return "", "", false
}

// findRouteUnder reports whether one of paths already falls under normalized
// (a route about to be claimed as a PluginRouteKindRoutes owner). findRouteOwner
// only catches a nested route processed after its owner; an already-claimed
// path processed first - from a ConfigMap that sorts earlier - would
// otherwise never be checked against an owner route declared later, since
// nothing revisits paths already accepted into seenRoutePaths.
func findRouteUnder(paths map[string]string, normalized string) (string, string, bool) {
	for path, plugin := range paths {
		if strings.HasPrefix(path, normalized+"/") {
			return path, plugin, true
		}
	}
	return "", "", false
}

// isReservedRoutePath reports whether normalized (already run through
// normalizeRoutePath) is off-limits for a manifest-declared route: it falls
// under one of reservedRoutePrefixes, or it's the empty string - the root
// path, which normalizeRoutePath also collapses "/", "." and ".." to, and
// which plugins.ts's own RESERVED_PATHS reserves unconditionally (its "").
func isReservedRoutePath(normalized string) bool {
	if normalized == "" {
		return true
	}
	for _, prefix := range reservedRoutePrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// errDestGone marks an extractZip failure that happened after it already removed the previous
// extraction at dest to make way for the new one - as opposed to a failure before that point,
// which leaves dest (and whatever it was already serving) untouched. Callers that keep a
// previous extraction alive across a failed reload (handleUpsert) need to tell the two apart:
// wrapped with errors.Is, not returned as a distinct type, so every other caller can keep
// treating extractZip's error as opaque.
var errDestGone = errors.New("previous extraction removed")

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

// safeJoin joins root and name the way net/http.Dir does: name is treated as rooted (as if it
// had a leading "/") before being cleaned, so any number of ".." segments collapse instead of
// escaping root - the standard technique for turning a URL path segment (pluginEntry.open, from
// an unauthenticated HTTP request) or an untrusted archive entry name (extractZip, "zip slip")
// into a safe local filesystem path.
func safeJoin(root, name string) string {
	return filepath.Join(root, filepath.FromSlash(cleanEntryName(name)))
}

// cleanEntryName normalizes a bundle-relative name (a zip central-directory entry, or a
// manifest's Entry/Federation.RemoteEntry) the same way safeJoin resolves it when writing
// (extractZip) or reading (pluginEntry.open) that file, so a name carrying a non-clean prefix
// (e.g. "./index.js", "/index.js", "a/./b.js") is keyed and looked up identically everywhere -
// see zipEntryNames and validateManifest.
func cleanEntryName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(name)), "/")
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

	// A rate-limited queue of ConfigMap keys rather than doing all the parsing/extraction work
	// directly in the event handlers below (which run on the informer's own goroutine): it gives
	// a transient failure (e.g. the extraction cache directory momentarily failing to create) a
	// rate-limited retry via AddRateLimited, the same way the directory source's queue already
	// does - see processConfigMapQueueItem.
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { r.enqueueConfigMap(queue, obj) },
		UpdateFunc: func(_, newObj interface{}) { r.enqueueConfigMap(queue, newObj) },
		DeleteFunc: func(obj interface{}) { r.enqueueConfigMap(queue, obj) },
	}); err != nil {
		r.logger.Error(err, "failed to register plugin ConfigMap event handler")
		return
	}

	// See disk.go's RunDirectoryWatch for why this WaitGroup - and this exact defer order -
	// matters: queue.ShutDown only unblocks the worker's next queue.Get, it doesn't wait for the
	// worker goroutine to actually exit.
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	defer queue.ShutDown()
	go func() {
		defer wg.Done()
		r.runConfigMapWorker(informer.GetIndexer(), queue)
	}()

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

// enqueueConfigMap adds obj's namespace/name key to queue, the same key shape
// cache.MetaNamespaceKeyFunc and indexer.GetByKey already agree on - see processConfigMapQueueItem.
func (r *Registry) enqueueConfigMap(queue workqueue.TypedRateLimitingInterface[string], obj interface{}) {
	cm := asConfigMap(obj)
	if cm == nil {
		return
	}
	key, err := cache.MetaNamespaceKeyFunc(cm)
	if err != nil {
		return // cm came from the informer, so it always has Name/Namespace set; cannot happen
	}
	queue.Add(key)
}

// runConfigMapWorker drains queue, (re)loading or dropping one ConfigMap per item, until queue is
// shut down (Run returning closes it via its own defer).
func (r *Registry) runConfigMapWorker(indexer cache.Indexer, queue workqueue.TypedRateLimitingInterface[string]) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}
		r.processConfigMapQueueItem(indexer, key, queue)
		queue.Done(key)
	}
}

// processConfigMapQueueItem looks key back up in indexer rather than trusting whatever object the
// triggering event carried - the informer may have already moved on by the time this runs, and a
// deleted ConfigMap's key still needs handling uniformly with an upsert. Not exists means deleted
// (or never existed - a delete event for something this registry never tracked is a no-op either
// way).
func (r *Registry) processConfigMapQueueItem(indexer cache.Indexer, key string, queue workqueue.TypedRateLimitingInterface[string]) {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		queue.Forget(key)
		return
	}
	obj, exists, err := indexer.GetByKey(key)
	if err != nil {
		r.logger.Error(err, "failed to look up plugin ConfigMap, will retry", "configMap", name)
		queue.AddRateLimited(key)
		return
	}
	if !exists {
		r.deleteConfigMapPluginByName(name)
		queue.Forget(key)
		return
	}
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		queue.Forget(key)
		return
	}
	if r.handleUpsert(cm) {
		queue.Forget(key)
	} else if queue.NumRequeues(key) >= maxPluginLoadRetries {
		// Every attempt has failed the same way handleUpsert already logged - keep re-queuing a
		// permanently-invalid ConfigMap forever instead of only a transient one. Give up until
		// the next Add/Update event on this ConfigMap (see maxPluginLoadRetries).
		r.logger.Error(fmt.Errorf("plugin ConfigMap failed to load after %d retries, giving up until it changes again", maxPluginLoadRetries), "giving up on plugin ConfigMap", "configMap", name)
		queue.Forget(key)
	} else {
		queue.AddRateLimited(key)
	}
}

// handleUpsert parses and extracts cm, updating the tracked entry for its name. Reports whether
// processing succeeded - false means a retry might help, used by processConfigMapQueueItem to
// decide whether to requeue.
func (r *Registry) handleUpsert(cm *corev1.ConfigMap) bool {
	if existing, ok := r.getConfigMapPlugin(cm.Name); ok && existing.resourceVersion == cm.ResourceVersion {
		// Nothing actually changed - most commonly the informer replaying its cache after a
		// watch reconnect. Skip the redundant re-extraction rather than rewriting to disk bytes
		// that are already there.
		return true
	}
	if !r.hasConfigMapCapacity(cm.Name) {
		// Checked before parsing/extracting, not just before addConfigMapPlugin below: a cap
		// rejection is permanent for the current state of the world (nothing about the bundle
		// itself is wrong), so it shouldn't cost an extraction, and returning true here (instead
		// of false) skips processConfigMapQueueItem's retry budget entirely rather than spending
		// it on a rejection that a retry can't fix.
		r.logger.Error(fmt.Errorf("plugins.maxConfigMapPlugins (%d) reached", r.maxConfigMapPlugins), "too many plugin ConfigMaps, dropping", "configMap", cm.Name)
		return true
	}
	dest, err := r.extractedPluginDir(configMapSourceName, cm.Name)
	if err != nil {
		r.logger.Error(err, "skipping plugin ConfigMap", "configMap", cm.Name)
		return false
	}
	entry, err := parsePluginConfigMap(cm, dest, r.maxBundleBytes)
	if err != nil {
		if errors.Is(err, errDestGone) {
			// Unlike a parse/validation failure (where dest is untouched and the previous,
			// still-valid version keeps being served), this failed after extractZip already
			// removed dest to make way for the new extraction - dest no longer holds anything.
			// Drop the tracked entry so Index() stops advertising a plugin whose every file
			// request now 404s, rather than leaving it listed until something happens to
			// redeliver this same ConfigMap (e.g. a future edit, or a watch-reconnect relist).
			r.deleteConfigMapPlugin(cm.Name)
		}
		r.logger.Error(err, "skipping invalid plugin ConfigMap", "configMap", cm.Name)
		return false
	}
	if !r.addConfigMapPlugin(cm.Name, *entry) {
		r.logger.Error(fmt.Errorf("plugins.maxConfigMapPlugins (%d) reached", r.maxConfigMapPlugins), "too many plugin ConfigMaps, dropping", "configMap", cm.Name)
		r.removeExtractedPluginDir(configMapSourceName, cm.Name)
		return false
	}
	r.logger.Info("Loaded plugin from ConfigMap", "configMap", cm.Name, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
	return true
}

// handleDelete drops obj's tracked plugin, if any - used directly by tests exercising deletion in
// isolation; Run's own queue instead detects a deletion by the ConfigMap's absence from the
// indexer (see processConfigMapQueueItem) rather than calling this from a DeleteFunc, since the
// object a delete event carries can already be stale by the time the queue gets to it.
func (r *Registry) handleDelete(obj interface{}) {
	cm := asConfigMap(obj)
	if cm == nil {
		return
	}
	r.deleteConfigMapPluginByName(cm.Name)
}

func (r *Registry) deleteConfigMapPluginByName(name string) {
	if !r.deleteConfigMapPlugin(name) {
		// Nothing was ever tracked under this name - a labeled ConfigMap that never loaded as a
		// plugin (a malformed manifest, a missing bundle.zip, or one labeled for a different
		// consumer entirely) reaching this via its absence from the indexer, or a stale delete
		// event for a ConfigMap this registry never indexed. Nothing to clean up or report.
		return
	}
	r.removeExtractedPluginDir(configMapSourceName, name)
	r.logger.Info("Removed plugin ConfigMap", "configMap", name)
}

// hasConfigMapCapacity reports whether name can be loaded without exceeding maxConfigMapPlugins -
// true if name is already tracked (an update is never blocked by the cap, only a new name) or the
// source has room for one more. Checked by handleUpsert before parsing/extracting cm's bundle, so
// a plugin past the cap is rejected without spending a bundle extraction on it.
func (r *Registry) hasConfigMapCapacity(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, exists := r.plugins[name]; exists {
		return true
	}
	return r.maxConfigMapPlugins <= 0 || len(r.plugins) < r.maxConfigMapPlugins
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
	r.refreshResolvedEntriesLocked()
	return true
}

func (r *Registry) getConfigMapPlugin(name string) (pluginEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.plugins[name]
	return entry, ok
}

// deleteConfigMapPlugin removes name's tracked entry, if any, and reports whether it was actually
// tracked - callers use that to avoid cleaning up / logging about a plugin that was never loaded.
func (r *Registry) deleteConfigMapPlugin(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[name]; !exists {
		return false
	}
	delete(r.plugins, name)
	r.refreshResolvedEntriesLocked()
	return true
}

// parsePluginConfigMap validates cm's manifest.json/bundle.zip and extracts the latter into dest
// (see extractZip - a fresh, atomically-swapped-into-place directory, up to maxBundleBytes of
// combined decompressed size).
func parsePluginConfigMap(cm *corev1.ConfigMap, dest string, maxBundleBytes int64) (*pluginEntry, error) {
	manifestData, ok := cm.Data[manifestFileName]
	if !ok {
		// manifest.json is meant to be a small UTF-8 text file and normally lands in Data, but
		// `kubectl create configmap --from-file` (and the apiserver in general) puts any file it
		// can't store as valid UTF-8 - a BOM, a stray non-UTF-8 byte - into BinaryData instead.
		// Falling back here keeps such a ConfigMap working instead of failing with a "missing
		// manifest.json" that doesn't point at the real cause.
		binaryManifestData, ok := cm.BinaryData[manifestFileName]
		if !ok {
			return nil, fmt.Errorf("missing %s", manifestFileName)
		}
		manifestData = string(binaryManifestData)
	}
	bundleData, ok := cm.BinaryData[bundleFileName]
	if !ok {
		return nil, fmt.Errorf("missing %s", bundleFileName)
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleData), int64(len(bundleData)))
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", bundleFileName, err)
	}
	manifest, err := validateManifest([]byte(manifestData), zipEntryNames(zr))
	if err != nil {
		return nil, err
	}
	extractRoot, err := extractZip(zr, dest, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, diskRoot: extractRoot, resourceVersion: cm.ResourceVersion}, nil
}

// zipEntryNames returns the set of regular (non-directory) file names in zr - the name-only view
// validateManifest checks a manifest's references against, before either source commits to
// extracting the whole bundle to disk.
func zipEntryNames(zr *zip.Reader) map[string]bool {
	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			names[cleanEntryName(f.Name)] = true
		}
	}
	return names
}

// extractZip extracts every regular file entry of zr into a fresh directory, then atomically
// swaps it into place at dest, replacing any previous extraction there. maxBundleBytes bounds
// the combined decompressed size across every entry (zero means unbounded) - a backstop against
// a "zip bomb", checked while extracting rather than after the fact, so a small,
// maliciously-high-ratio entry can't balloon into an unbounded write before this ever gets a
// chance to reject it.
//
// Extracting to a temporary directory first, rather than clearing dest and writing the new
// bundle directly into it, matters because dest is also what's being served live
// (pluginEntry.diskRoot): a reader that already resolved diskRoot == dest (mid-request, via
// pluginEntry.open) keeps reading whatever's physically there until this function's rename
// below completes, so it never observes a half-extracted dest. os.Rename can't atomically
// replace a non-empty directory (a POSIX limitation, not something fixable here), so dest is
// still removed before the rename - but that's now a two-syscall gap where dest doesn't exist at
// all (a racing request 404s rather than seeing either version), rather than however long it
// takes to overwrite every file in the bundle one at a time. If the RemoveAll or the Rename
// itself then fails, dest is left gone (or partly gone) rather than restored to the previous
// extraction - see errDestGone.
func extractZip(zr *zip.Reader, dest string, maxBundleBytes int64) (string, error) {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("failed to create %q: %w", parent, err)
	}
	tmpDest, err := os.MkdirTemp(parent, filepath.Base(dest)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("failed to create extraction directory: %w", err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			os.RemoveAll(tmpDest)
		}
	}()

	// A non-positive maxBundleBytes means unbounded; treat it as an effectively-infinite budget
	// rather than special-casing "no limit" separately below - no real bundle will ever reach
	// math.MaxInt64 decompressed bytes.
	budgetTotal := maxBundleBytes
	if budgetTotal <= 0 {
		budgetTotal = math.MaxInt64
	}
	var written int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// safeJoin neutralizes a malicious entry name (e.g. "../../etc/cron.d/evil") the same
		// way it neutralizes a malicious URL filename in pluginEntry.open - this is exactly the
		// "zip slip" vulnerability class, on the write side instead of the read side.
		target := safeJoin(tmpDest, f.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", fmt.Errorf("failed to create directory for %q: %w", f.Name, err)
		}
		n, err := extractZipFile(f, target, budgetTotal-written)
		if err != nil {
			return "", err
		}
		written += n
	}

	if err := os.RemoveAll(dest); err != nil {
		// RemoveAll doesn't stop at the first failing entry - it keeps deleting siblings and
		// returns the first error - so dest can come out of this partly deleted rather than
		// fully removed or fully intact. Either way it no longer reliably holds the previous
		// extraction, so this counts as errDestGone too.
		return "", fmt.Errorf("failed to remove previous extraction: %w: %w", errDestGone, err)
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		return "", fmt.Errorf("failed to finalize extraction: %w: %w", errDestGone, err)
	}
	succeeded = true
	return dest, nil
}

// extractZipFile writes f's decompressed content to target, stopping and returning an error if
// it would write more than budget bytes - budget is whatever's left of the caller's total
// maxBundleBytes after every earlier entry in the same bundle (see extractZip), so the limit
// applies to the bundle as a whole rather than resetting per file. Returns the number of bytes
// written.
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
	// budget can be math.MaxInt64 (unbounded case) - budget+1 would overflow, so instead of
	// io.CopyN(out, rc, budget+1) copy exactly budget bytes, then peek one more to tell "exactly
	// budget bytes" apart from "more remained".
	written, err := io.Copy(out, io.LimitReader(rc, budget))
	if err != nil {
		out.Close()
		return 0, fmt.Errorf("failed to write %q: %w", target, err)
	}
	if written == budget {
		// io.Reader permits a (0, nil) return that callers must not treat as EOF, so loop until
		// we get either a byte (more data remained past the budget) or a real error/io.EOF.
		var extra [1]byte
		for {
			n, err := rc.Read(extra[:])
			if n > 0 {
				out.Close()
				return 0, fmt.Errorf("%q decompresses past this plugin's bundle size budget, refusing to extract further (possible zip bomb)", f.Name)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				out.Close()
				return 0, fmt.Errorf("failed to read %q past its size budget: %w", f.Name, err)
			}
		}
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("failed to write %q: %w", target, err)
	}
	return written, nil
}

// validateManifest parses manifestJSON and checks that every file it references (Entry,
// Federation.RemoteEntry) is present in the bundle - names is just the bundle's file name set,
// not its content, since both callers only have names cheaply available (from bundle.zip's
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
	entry := cleanEntryName(manifest.Entry)
	if !names[entry] {
		return nil, fmt.Errorf("entry file %q referenced by manifest not found in %s", manifest.Entry, bundleFileName)
	}
	if manifest.Federation != nil {
		if manifest.Federation.RemoteEntry == "" {
			return nil, fmt.Errorf("manifest's federation is missing 'remoteEntry'")
		}
		remoteEntry := cleanEntryName(manifest.Federation.RemoteEntry)
		if remoteEntry == entry {
			return nil, fmt.Errorf("manifest's 'federation.remoteEntry' must not be the same file as 'entry' - the host always import()s 'entry' as a plain ES module, which a federation remote entry is not")
		}
		if !names[remoteEntry] {
			return nil, fmt.Errorf("remote entry file %q referenced by manifest's federation not found in %s", manifest.Federation.RemoteEntry, bundleFileName)
		}
		if len(manifest.Federation.Routes) == 0 {
			return nil, fmt.Errorf("manifest's 'federation.routes' must not be empty")
		}
		seenPaths := make(map[string]string, len(manifest.Federation.Routes))
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
			// Reject anything other than the two known Kind values outright rather than letting
			// an unrecognized one (e.g. a typo'd "route") pass through and silently fall back to
			// PluginRouteKindComponent on the host - that failure mode surfaces much later, as an
			// opaque loadComponent() error with no hint the manifest itself was ever at fault.
			if route.Kind != "" && route.Kind != apisv1.PluginRouteKindComponent && route.Kind != apisv1.PluginRouteKindRoutes {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].kind' %q is not one of %q, %q", i, route.Kind, apisv1.PluginRouteKindComponent, apisv1.PluginRouteKindRoutes)
			}
			// The reservations the backend can enforce on a route's Path itself are the root
			// path and the nginx-served prefixes (see isReservedRoutePath), neither specific to
			// any one frontend. A route colliding with a given host's own built-in pages (e.g.
			// "/settings") is instead the host's job to reject, the same way this repo's
			// plugins.ts (RESERVED_PATHS/dedupeByPath) already does for its own code-registered
			// routes - the backend has no way to know a given host's built-in path list, and the
			// out-of-tree, module-federation-aware host that actually consumes 'federation' needs
			// its own equivalent for manifest-declared routes.
			normalized := normalizeRoutePath(route.Path)
			if isReservedRoutePath(normalized) {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].path' %q is the root path or falls under a reserved prefix (%s)", i, route.Path, strings.Join(reservedRoutePrefixes, ", "))
			}
			if other, ok := seenPaths[normalized]; ok {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].path' %q duplicates earlier route %q in the same manifest", i, route.Path, other)
			}
			seenPaths[normalized] = route.Path
		}
		// A PluginRouteKindRoutes route owns every sub-path under its own Path - that's the whole
		// point of it (see apis/v1.PluginRoute.Kind) - so a sibling route nested under one is the
		// same collision an exact duplicate Path is, just spelled differently: the plugin's own
		// route tree and the sibling both claim that path, and which one a host's router mounts
		// there is whichever it happens to match first. Checked in its own pass over the routes
		// so it doesn't depend on the order the two are declared in.
		for i, route := range manifest.Federation.Routes {
			if route.Kind != apisv1.PluginRouteKindRoutes {
				continue
			}
			owner := normalizeRoutePath(route.Path)
			for j, other := range manifest.Federation.Routes {
				if i == j {
					continue
				}
				if !strings.HasPrefix(normalizeRoutePath(other.Path), owner+"/") {
					continue
				}
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].path' %q falls under 'federation.routes[%d].path' %q, whose kind %q makes the plugin own that whole route tree", j, other.Path, i, route.Path, apisv1.PluginRouteKindRoutes)
			}
		}
	}
	return &manifest, nil
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
