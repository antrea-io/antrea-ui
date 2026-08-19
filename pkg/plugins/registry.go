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

// Package plugins discovers frontend plugin bundles (a manifest.json plus
// the plugin's JS entry file) from two sources - labeled ConfigMaps, and
// optionally a filesystem directory - and keeps an in-memory index that the
// backend's /api/v1/plugins routes serve. Either source can change at any
// time; the registry reflects the change on the next request, with no
// antrea-ui restart required. See registry.go for the ConfigMap source and
// disk.go for the directory source.
package plugins

import (
	"encoding/json"
	"fmt"
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

const manifestFileName = "manifest.json"

type pluginEntry struct {
	manifest apisv1.PluginManifest
	files    map[string][]byte
}

type Registry struct {
	logger        logr.Logger
	clientset     kubernetes.Interface
	namespace     string
	labelSelector string

	mu          sync.RWMutex
	plugins     map[string]pluginEntry // keyed by the backing ConfigMap's name
	diskPlugins map[string]pluginEntry // keyed by the backing directory's name
}

func NewRegistry(logger logr.Logger, clientset kubernetes.Interface, namespace, labelSelector string) *Registry {
	return &Registry{
		logger:        logger,
		clientset:     clientset,
		namespace:     namespace,
		labelSelector: labelSelector,
		plugins:       make(map[string]pluginEntry),
		diskPlugins:   make(map[string]pluginEntry),
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
	entry, err := parsePluginConfigMap(cm)
	if err != nil {
		r.logger.Error(err, "skipping invalid plugin ConfigMap", "configMap", cm.Name)
		return
	}
	r.addConfigMapPlugin(cm.Name, *entry)
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

func (r *Registry) addConfigMapPlugin(name string, entry pluginEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plugins[name] = entry
}

func (r *Registry) deleteConfigMapPlugin(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, name)
}

func parsePluginConfigMap(cm *corev1.ConfigMap) (*pluginEntry, error) {
	files := make(map[string][]byte, len(cm.Data)+len(cm.BinaryData))
	for k, v := range cm.Data {
		files[k] = []byte(v)
	}
	for k, v := range cm.BinaryData {
		files[k] = v
	}
	return parseManifestAndFiles(files)
}

// parseManifestAndFiles validates a plugin bundle's flat file set - shared by the ConfigMap
// source (registry.go) and the directory source (disk.go), which differ only in how they collect
// files into this same map[string][]byte shape.
func parseManifestAndFiles(files map[string][]byte) (*pluginEntry, error) {
	manifestData, ok := files[manifestFileName]
	if !ok {
		return nil, fmt.Errorf("missing %s", manifestFileName)
	}
	var manifest apisv1.PluginManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", manifestFileName, err)
	}
	if manifest.Name == "" {
		return nil, fmt.Errorf("manifest is missing 'name'")
	}
	if manifest.Entry == "" {
		return nil, fmt.Errorf("manifest is missing 'entry'")
	}
	if _, ok := files[manifest.Entry]; !ok {
		return nil, fmt.Errorf("entry file %q referenced by manifest not found in plugin bundle", manifest.Entry)
	}
	if manifest.Federation != nil {
		if manifest.Federation.RemoteEntry == "" {
			return nil, fmt.Errorf("manifest's federation is missing 'remoteEntry'")
		}
		if _, ok := files[manifest.Federation.RemoteEntry]; !ok {
			return nil, fmt.Errorf("remote entry file %q referenced by manifest's federation not found in plugin bundle", manifest.Federation.RemoteEntry)
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
	return &pluginEntry{manifest: manifest, files: files}, nil
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

// File returns the contents of filename belonging to the plugin named pluginName, as currently
// known to the registry. When the plugin name is claimed by more than one source, it resolves to
// the same one Index() would keep.
func (r *Registry) File(pluginName, filename string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.resolvedEntries()[pluginName]
	if !ok {
		return nil, false
	}
	data, ok := entry.files[filename]
	return data, ok
}
