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
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// RunConfigMapWatch watches ConfigMaps matching the registry's namespace and label
// selector until stopCh is closed. It blocks and should be called from a
// goroutine.
func (r *Registry) RunConfigMapWatch(stopCh <-chan struct{}) {
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
// shut down (RunConfigMapWatch returning closes it via its own defer).
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
	entry, err := parsePluginConfigMap(cm, dest, r.maxBundleBytes, r.signatureVerifiers)
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
	r.logger.Info("Loaded plugin from ConfigMap", "configMap", cm.Name, "plugin", entry.manifest.Name, "version", entry.manifest.Version, "verifiedBy", entry.verifiedBy)
	return true
}

// handleDelete drops obj's tracked plugin, if any - used directly by tests exercising deletion in
// isolation; RunConfigMapWatch's own queue instead detects a deletion by the ConfigMap's absence
// from the indexer (see processConfigMapQueueItem) rather than calling this from a DeleteFunc,
// since the object a delete event carries can already be stale by the time the queue gets to it.
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
//
// When verifiers is non-empty, cm must also carry a signature (e.g. a manifest.json.asc key)
// verifying against one of them, and its manifest must carry a bundleSha256 matching bundle.zip
// (see signature.go). Both checks run before extractZip, the only step here with a side effect -
// extractZip removes the previous extraction to make way for the new one, so an unverified bundle
// must never reach it and displace a verified one. With no verifiers, any signature key is ignored
// entirely (there is nothing to verify it against), but a bundleSha256 present in the manifest is
// still checked.
func parsePluginConfigMap(cm *corev1.ConfigMap, dest string, maxBundleBytes int64, verifiers []SignatureVerifier) (*pluginEntry, error) {
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
	var verifiedBy string
	if requireSignature(verifiers) {
		// Same Data-then-BinaryData fallback as manifest.json above: an armored signature is
		// ASCII, so it normally lands in Data, but where `kubectl create configmap --from-file`
		// puts a given file is not something to rely on.
		readSignature := func(fileName string) ([]byte, bool, error) {
			if signature, ok := cm.Data[fileName]; ok {
				return []byte(signature), true, nil
			}
			signature, ok := cm.BinaryData[fileName]
			return signature, ok, nil
		}
		signer, err := verifyManifestSignature(verifiers, []byte(manifestData), readSignature)
		if err != nil {
			return nil, err
		}
		verifiedBy = signer
	}
	zr, err := zip.NewReader(bytes.NewReader(bundleData), int64(len(bundleData)))
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", bundleFileName, err)
	}
	manifest, err := validateManifest([]byte(manifestData), zipEntryNames(zr))
	if err != nil {
		return nil, err
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
