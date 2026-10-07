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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// RunConfigMapWatch watches ConfigMaps matching the registry's namespace and label
// selector until stopCh is closed. It blocks and should be called from a
// goroutine. A no-op returning immediately when the registry has no namespace configured: an
// empty namespace means the ConfigMap plugin source is disabled, and informers.WithNamespace("")
// means "all namespaces" rather than "none", so this guard has to live here rather than relying
// on callers to skip invoking it.
func (r *Registry) RunConfigMapWatch(stopCh <-chan struct{}) {
	if r.namespace == "" {
		r.logger.Info("ConfigMap plugin source disabled, no namespace configured")
		return
	}
	factory := informers.NewSharedInformerFactoryWithOptions(
		r.clientset,
		0,
		informers.WithNamespace(r.namespace),
		// Server-side filtering: the selector goes into ListOptions, so the API server only ever
		// sends us labeled ConfigMaps and the informer cache holds nothing else. It is not a
		// security boundary though - RBAC has no label dimension, so the ServiceAccount's grant
		// necessarily covers every ConfigMap in r.namespace (see plugins-rbac.yaml).
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
	//
	// A failing bundle download is retried on its own, faster-capped backoff (see
	// Registry.downloadBackoff) instead of this queue's, so that ConfigMaps that load their
	// bundle from the ConfigMap itself keep the default limiter's bounded retry budget.
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { r.enqueueConfigMap(queue, obj) },
		UpdateFunc: func(_, newObj any) { r.enqueueConfigMap(queue, newObj) },
		DeleteFunc: func(obj any) { r.enqueueConfigMap(queue, obj) },
	}); err != nil {
		r.logger.Error(err, "failed to register plugin ConfigMap event handler")
		return
	}

	// See disk.go's RunDirectoryWatch for why this WaitGroup - and this exact defer order -
	// matters: queue.ShutDown only unblocks the worker's next queue.Get, it doesn't wait for the
	// worker goroutine to actually exit.
	var wg sync.WaitGroup
	defer wg.Wait()
	defer queue.ShutDown()
	// Cancelled when stopCh closes, so a download in flight doesn't hold up shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// Several workers, because a download can hold one for up to bundleDownloadTimeout, and a
	// stalled bundle server must not keep every other plugin ConfigMap from loading or being removed.
	// The queue never hands the same key to two workers at once.
	for range configMapWorkers {
		wg.Go(func() {
			r.runConfigMapWorker(ctx, informer.GetIndexer(), queue)
		})
	}

	r.logger.Info("Starting plugin ConfigMap watch", "namespace", r.namespace, "labelSelector", r.labelSelector)
	informer.Run(stopCh)
}

func asConfigMap(obj any) *corev1.ConfigMap {
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
func (r *Registry) enqueueConfigMap(queue workqueue.TypedRateLimitingInterface[string], obj any) {
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

// configMapWorkers is how many ConfigMaps are processed concurrently.
const configMapWorkers = 4

// runConfigMapWorker drains queue, (re)loading or dropping one ConfigMap per item, until queue is
// shut down (RunConfigMapWatch returning closes it via its own defer).
func (r *Registry) runConfigMapWorker(ctx context.Context, indexer cache.Indexer, queue workqueue.TypedRateLimitingInterface[string]) {
	for {
		key, shutdown := queue.Get()
		if shutdown {
			return
		}
		r.processConfigMapQueueItem(ctx, indexer, key, queue)
		queue.Done(key)
	}
}

// processConfigMapQueueItem looks key back up in indexer rather than trusting whatever object the
// triggering event carried - the informer may have already moved on by the time this runs, and a
// deleted ConfigMap's key still needs handling uniformly with an upsert. Not exists means deleted
// (or never existed - a delete event for something this registry never tracked is a no-op either
// way).
func (r *Registry) processConfigMapQueueItem(ctx context.Context, indexer cache.Indexer, key string, queue workqueue.TypedRateLimitingInterface[string]) {
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
		r.downloadBackoff.Forget(key)
		return
	}
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		queue.Forget(key)
		return
	}
	result := r.handleUpsert(ctx, cm)
	if result != upsertRetryAlways {
		r.downloadBackoff.Forget(key)
	}
	switch result {
	case upsertDone, upsertRejected:
		queue.Forget(key)
	case upsertRetryAlways:
		// Nothing about the ConfigMap is wrong - what failed is outside it (see
		// retryableError) - so unlike below this never gives up. A new event for the ConfigMap
		// (a new bundleSha256, say) still restarts it: Add puts the key on the queue with no
		// delay, and the next failure continues the backoff from where it was.
		queue.Forget(key)
		queue.AddAfter(key, r.downloadBackoff.When(key))
	case upsertRetry:
		if queue.NumRequeues(key) >= maxPluginLoadRetries {
			// Every attempt has failed the same way handleUpsert already logged - keep
			// re-queuing a permanently-invalid ConfigMap forever instead of only a transient
			// one. Give up until the next Add/Update event on this ConfigMap (see
			// maxPluginLoadRetries).
			r.logger.Error(fmt.Errorf("plugin ConfigMap failed to load after %d retries, giving up until it changes again", maxPluginLoadRetries), "giving up on plugin ConfigMap", "configMap", name)
			queue.Forget(key)
		} else {
			queue.AddRateLimited(key)
		}
	}
}

// upsertResult is what handleUpsert tells its caller to do with the ConfigMap's queue key.
type upsertResult int

const (
	// upsertDone: the ConfigMap is loaded, or deliberately skipped (unchanged, or over the
	// plugin cap, which no retry can fix).
	upsertDone upsertResult = iota
	// upsertRetry: loading failed; retry with a bounded budget (maxPluginLoadRetries).
	upsertRetry
	// upsertRetryAlways: loading failed because of something outside the ConfigMap that may
	// resolve on its own (see retryableError); retry without a limit.
	upsertRetryAlways
	// upsertRejected: loading failed because the ConfigMap itself is invalid; retrying cannot
	// help until it changes.
	upsertRejected
)

// retryableError marks a failure to load a plugin that is not the ConfigMap's fault - a bundle
// that couldn't be downloaded, or arrived with the wrong digest - and so is worth retrying
// without limit. Anything else about a remote-bundle ConfigMap (a bad signature, a malformed
// manifest, an undecodable bundle with a matching digest) is not: it cannot change without the
// ConfigMap changing.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

// retryRateLimiter is the exponential per-item backoff of a failing bundle download, 1s up to
// 1min with jitter, rather than the queue's 5ms up to 1000s: the download is retried indefinitely
// (the bundle server not ready yet, a rolling upgrade where some replicas don't have the new digest yet)
// and has to pick up again within a minute of the bundle server becoming healthy. The jitter keeps a
// restart from having every plugin's download retry in lockstep against the bundle server.
type retryRateLimiter struct {
	workqueue.TypedRateLimiter[string]
}

func newRetryRateLimiter() workqueue.TypedRateLimiter[string] {
	return retryRateLimiter{workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Second, time.Minute)}
}

func (l retryRateLimiter) When(item string) time.Duration {
	return wait.Jitter(l.TypedRateLimiter.When(item), 0.2)
}

// handleUpsert parses and extracts cm - downloading its bundle first, if cm only carries a
// manifest - updating the tracked entry for its name. The result tells processConfigMapQueueItem
// whether to requeue.
func (r *Registry) handleUpsert(ctx context.Context, cm *corev1.ConfigMap) upsertResult {
	if existing, ok := r.getConfigMapPlugin(cm.Name); ok && existing.resourceVersion == cm.ResourceVersion {
		// Nothing actually changed - most commonly the informer replaying its cache after a
		// watch reconnect. Skip the redundant re-extraction rather than rewriting to disk bytes
		// that are already there.
		return upsertDone
	}
	if !r.hasConfigMapCapacity(cm.Name) {
		// Checked before parsing/extracting, not just before addConfigMapPlugin below: a cap
		// rejection is permanent for the current state of the world (nothing about the bundle
		// itself is wrong), so it shouldn't cost an extraction, and returning true here (instead
		// of false) skips processConfigMapQueueItem's retry budget entirely rather than spending
		// it on a rejection that a retry can't fix.
		r.logger.Error(fmt.Errorf("plugins.maxConfigMapPlugins (%d) reached", r.maxConfigMapPlugins), "too many plugin ConfigMaps, dropping", "configMap", cm.Name)
		return upsertDone
	}
	dest, err := r.extractedPluginDir(configMapSourceName, cm.Name)
	if err != nil {
		r.logger.Error(err, "skipping plugin ConfigMap", "configMap", cm.Name)
		return upsertRetry
	}
	remote := hasRemoteBundle(cm)
	var entry *pluginEntry
	if remote {
		existing, _ := r.getConfigMapPlugin(cm.Name)
		entry, err = r.parseRemotePluginConfigMap(ctx, cm, dest, existing)
	} else {
		entry, err = parsePluginConfigMap(cm, dest, r.maxBundleBytes, r.signatureVerifiers)
	}
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
		var retryable *retryableError
		if errors.As(err, &retryable) {
			// The previous version, if any, keeps being served in the meantime.
			r.logger.Error(err, "failed to download plugin bundle, will retry", "configMap", cm.Name)
			return upsertRetryAlways
		}
		r.logger.Error(err, "skipping invalid plugin ConfigMap", "configMap", cm.Name)
		if remote {
			return upsertRejected
		}
		return upsertRetry
	}
	if !r.addConfigMapPlugin(cm.Name, *entry) {
		r.logger.Error(fmt.Errorf("plugins.maxConfigMapPlugins (%d) reached", r.maxConfigMapPlugins), "too many plugin ConfigMaps, dropping", "configMap", cm.Name)
		r.removeExtractedPluginDir(configMapSourceName, cm.Name)
		return upsertRetry
	}
	r.logger.Info("Loaded plugin from ConfigMap", "configMap", cm.Name, "plugin", entry.manifest.Name, "version", entry.manifest.Version, "verifiedBy", entry.verifiedBy)
	return upsertDone
}

// handleDelete drops obj's tracked plugin, if any - used directly by tests exercising deletion in
// isolation; RunConfigMapWatch's own queue instead detects a deletion by the ConfigMap's absence
// from the indexer (see processConfigMapQueueItem) rather than calling this from a DeleteFunc,
// since the object a delete event carries can already be stale by the time the queue gets to it.
func (r *Registry) handleDelete(obj any) {
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
//
// When verifiers is non-empty, cm must also carry a signature (e.g. a manifest.json.asc key)
// verifying against one of them, and its manifest must carry a bundleSha256 matching bundle.zip
// (see signature.go). Both checks run before extractZip, the only step here with a side effect -
// extractZip removes the previous extraction to make way for the new one, so an unverified bundle
// must never reach it and displace a verified one. With no verifiers, any signature key is ignored
// entirely (there is nothing to verify it against), but a bundleSha256 present in the manifest is
// still checked.
func parsePluginConfigMap(cm *corev1.ConfigMap, dest string, maxBundleBytes int64, verifiers []SignatureVerifier) (*pluginEntry, error) {
	manifestData, err := configMapManifest(cm)
	if err != nil {
		return nil, err
	}
	bundleData, ok := cm.BinaryData[bundleFileName]
	if !ok {
		return nil, fmt.Errorf("missing %s", bundleFileName)
	}
	var verifiedBy string
	if requireSignature(verifiers) {
		signer, err := verifyManifestSignature(verifiers, manifestData, configMapSignatureReader(cm))
		if err != nil {
			return nil, err
		}
		verifiedBy = signer
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleData), int64(len(bundleData)))
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", bundleFileName, err)
	}
	manifest, err := validateManifest(manifestData, zipEntryNames(zr))
	if err != nil {
		return nil, err
	}
	if manifest.BundleSource != nil {
		// Two bundles, and no telling which the signature and digest were meant to cover.
		return nil, fmt.Errorf("manifest's 'bundleSource' conflicts with the %s in the ConfigMap", bundleFileName)
	}
	// Hashed over bundleData - the binaryData value as Go sees it, not its base64 encoding in the
	// ConfigMap's YAML.
	if err := verifyManifestBundleDigest(verifiers, manifest, bytes.NewReader(bundleData)); err != nil {
		return nil, err
	}
	extractRoot, err := extractZip(zr, dest, maxBundleBytes)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, diskRoot: extractRoot, resourceVersion: cm.ResourceVersion, verifiedBy: verifiedBy}, nil
}

// configMapManifest returns cm's manifest.json.
func configMapManifest(cm *corev1.ConfigMap) ([]byte, error) {
	if data, ok := cm.Data[manifestFileName]; ok {
		return []byte(data), nil
	}
	// manifest.json is meant to be a small UTF-8 text file and normally lands in Data, but
	// `kubectl create configmap --from-file` (and the apiserver in general) puts any file it
	// can't store as valid UTF-8 - a BOM, a stray non-UTF-8 byte - into BinaryData instead.
	// Falling back here keeps such a ConfigMap working instead of failing with a "missing
	// manifest.json" that doesn't point at the real cause.
	if data, ok := cm.BinaryData[manifestFileName]; ok {
		return data, nil
	}
	return nil, fmt.Errorf("missing %s", manifestFileName)
}

// configMapSignatureReader reads signature files out of cm. Same Data-then-BinaryData fallback as
// configMapManifest: an armored signature is ASCII, so it normally lands in Data, but where
// `kubectl create configmap --from-file` puts a given file is not something to rely on.
func configMapSignatureReader(cm *corev1.ConfigMap) signatureReader {
	return func(fileName string) ([]byte, bool, error) {
		if signature, ok := cm.Data[fileName]; ok {
			return []byte(signature), true, nil
		}
		signature, ok := cm.BinaryData[fileName]
		return signature, ok, nil
	}
}

// hasRemoteBundle reports whether cm is a manifest-only plugin ConfigMap, whose bundle.zip is
// downloaded instead (see parseRemotePluginConfigMap): it carries no bundle.zip, and its manifest
// declares a bundleSource. This only routes the ConfigMap - nothing read from the manifest here
// is trusted, and parseRemotePluginConfigMap verifies its signature before using any of it. A
// ConfigMap that is neither is invalid, and fails in parsePluginConfigMap as it always has.
func hasRemoteBundle(cm *corev1.ConfigMap) bool {
	if _, ok := cm.BinaryData[bundleFileName]; ok {
		return false
	}
	manifestData, err := configMapManifest(cm)
	if err != nil {
		return false
	}
	var peek struct {
		BundleSource *apisv1.PluginBundleSource `json:"bundleSource"`
	}
	return json.Unmarshal(manifestData, &peek) == nil && peek.BundleSource != nil
}

// reuseExtractedBundle returns an entry for manifest that serves the files existing already
// extracted, or nil if existing doesn't hold the bundle manifest names. The digest is what
// identifies a bundle, so a ConfigMap edit that leaves it unchanged (a label, a new version
// string) needs no download. The files manifest references are still checked, as they would be
// in a fresh download. manifest has been authenticated by the caller.
func reuseExtractedBundle(manifest *apisv1.PluginManifest, existing pluginEntry, resourceVersion, verifiedBy string) *pluginEntry {
	if existing.diskRoot == "" || !strings.EqualFold(existing.manifest.BundleSha256, manifest.BundleSha256) {
		return nil
	}
	files := []string{manifest.Entry}
	if manifest.Federation != nil {
		files = append(files, manifest.Federation.RemoteEntry)
	}
	for _, f := range files {
		if info, err := os.Stat(safeJoin(existing.diskRoot, cleanEntryName(f))); err != nil || !info.Mode().IsRegular() {
			return nil
		}
	}
	return &pluginEntry{manifest: *manifest, diskRoot: existing.diskRoot, resourceVersion: resourceVersion, verifiedBy: verifiedBy}
}

// parseRemotePluginConfigMap is parsePluginConfigMap for a ConfigMap that carries no bundle.zip:
// its manifest's bundleSource says where to download it from. Everything is checked in the order
// that keeps an untrusted request or byte from going any further than it has earned:
//
//  1. The manifest's signature, when verification is enabled, before anything is read from it. A
//     bundleSource.apiServer manifest is rejected when it is not: nothing would then
//     authenticate the path it names, which is the location the backend sends its own
//     credentials to. A bundleSource.http manifest has no such need - its request carries no
//     credentials, and the digest decides what is accepted - so it loads unsigned when no
//     verification is configured, as an inline bundle does.
//  2. The manifest itself, including its source and the digest it requires, a positive
//     maxBundleBytes to bound the download with, and for an http source the URL in the ConfigMap.
//  3. The download, unless existing already holds a bundle with this digest, bounded by
//     maxBundleBytes, into a private file.
//  4. The digest of those exact bytes, then the zip's central directory, then the extraction -
//     all reading the one private copy, as in parsePluginArchive.
//
// Failures of the download and the digest check are wrapped in retryableError.
func (r *Registry) parseRemotePluginConfigMap(ctx context.Context, cm *corev1.ConfigMap, dest string, existing pluginEntry) (*pluginEntry, error) {
	manifestData, err := configMapManifest(cm)
	if err != nil {
		return nil, err
	}
	var verifiedBy string
	if requireSignature(r.signatureVerifiers) {
		verifiedBy, err = verifyManifestSignature(r.signatureVerifiers, manifestData, configMapSignatureReader(cm))
		if err != nil {
			return nil, err
		}
	}
	manifest, err := parseManifest(manifestData)
	if err != nil {
		return nil, err
	}
	if manifest.BundleSource == nil {
		return nil, errors.New("manifest is missing 'bundleSource'")
	}
	if manifest.BundleSha256 == "" {
		return nil, fmt.Errorf("manifest is missing 'bundleSha256', which a manifest with a 'bundleSource' requires")
	}
	if err := checkBundleSha256(manifest.BundleSha256); err != nil {
		return nil, err
	}

	if r.maxBundleBytes <= 0 {
		// Nothing would bound a download, which comes from a server the backend does not
		// control, into the local disk.
		return nil, errors.New("a manifest with a 'bundleSource' needs a positive plugins.maxBundleBytes")
	}

	var fetcher BundleFetcher
	var target string
	switch {
	case manifest.BundleSource.APIServer != nil:
		if !requireSignature(r.signatureVerifiers) {
			return nil, errors.New("a manifest with 'bundleSource.apiServer' is only supported when plugin signature verification is enabled (plugins.signature)")
		}
		fetcher = r.bundleFetcher
		target = bundleRequestPath(manifest.BundleSource.APIServer.Path, manifest.BundleSha256)
	default:
		if target, err = validateBundleURL(cm.Data[bundleURLKey]); err != nil {
			return nil, err
		}
		fetcher = r.httpBundleFetcher
	}
	if reused := reuseExtractedBundle(manifest, existing, cm.ResourceVersion, verifiedBy); reused != nil {
		return reused, nil
	}
	if fetcher == nil {
		return nil, errors.New("downloading plugin bundles is not configured")
	}

	if manifest.BundleSource.APIServer != nil {
		r.logger.Info("Downloading plugin bundle", "configMap", cm.Name, "plugin", manifest.Name, "path", target)
	} else {
		// Not the URL, which may hold a secret.
		r.logger.Info("Downloading plugin bundle", "configMap", cm.Name, "plugin", manifest.Name, "source", "http")
	}
	body, err := fetcher.Fetch(ctx, target)
	if err != nil {
		return nil, &retryableError{fmt.Errorf("failed to download %s: %w", bundleFileName, err)}
	}
	bundle, size, err := copyBundleToTemp(body, dest, r.maxBundleBytes)
	body.Close()
	if err != nil {
		if errors.Is(err, errBundleTooLarge) {
			return nil, err
		}
		return nil, &retryableError{err}
	}
	defer func() {
		bundle.Close()
		os.Remove(bundle.Name())
	}()

	if err := verifyBundleDigest(io.NewSectionReader(bundle, 0, size), manifest.BundleSha256); err != nil {
		return nil, &retryableError{err}
	}
	zr, err := zip.NewReader(bundle, size)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", bundleFileName, err)
	}
	if err := checkManifestFiles(manifest, zipEntryNames(zr)); err != nil {
		return nil, err
	}
	extractRoot, err := extractZip(zr, dest, r.maxBundleBytes)
	if err != nil {
		return nil, err
	}
	return &pluginEntry{manifest: *manifest, diskRoot: extractRoot, resourceVersion: cm.ResourceVersion, verifiedBy: verifiedBy}, nil
}
