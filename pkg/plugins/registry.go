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

// Package plugins watches labeled ConfigMaps for frontend plugin bundles
// (a manifest.json plus the plugin's JS entry file, as ConfigMap data) and
// keeps an in-memory index that the backend's /api/v1/plugins routes serve.
// A ConfigMap can be created or deleted at any time; the registry reflects
// the change on the next request, with no antrea-ui restart required.
package plugins

import (
	"encoding/json"
	"fmt"
	"sort"
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

	mu      sync.RWMutex
	plugins map[string]pluginEntry // keyed by the backing ConfigMap's name
}

func NewRegistry(logger logr.Logger, clientset kubernetes.Interface, namespace, labelSelector string) *Registry {
	return &Registry{
		logger:        logger,
		clientset:     clientset,
		namespace:     namespace,
		labelSelector: labelSelector,
		plugins:       make(map[string]pluginEntry),
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
	r.mu.Lock()
	r.plugins[cm.Name] = *entry
	r.mu.Unlock()
	r.logger.Info("Loaded plugin from ConfigMap", "configMap", cm.Name, "plugin", entry.manifest.Name, "version", entry.manifest.Version)
}

func (r *Registry) handleDelete(obj interface{}) {
	cm := asConfigMap(obj)
	if cm == nil {
		return
	}
	r.mu.Lock()
	delete(r.plugins, cm.Name)
	r.mu.Unlock()
	r.logger.Info("Removed plugin ConfigMap", "configMap", cm.Name)
}

func parsePluginConfigMap(cm *corev1.ConfigMap) (*pluginEntry, error) {
	files := make(map[string][]byte, len(cm.Data)+len(cm.BinaryData))
	for k, v := range cm.Data {
		files[k] = []byte(v)
	}
	for k, v := range cm.BinaryData {
		files[k] = v
	}
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
		return nil, fmt.Errorf("entry file %q referenced by manifest not found in ConfigMap", manifest.Entry)
	}
	return &pluginEntry{manifest: manifest, files: files}, nil
}

// sortedConfigMapNames returns the currently known backing ConfigMap names in
// a deterministic order, so that dedup-by-plugin-name (in Index and File)
// consistently picks the same winner on every call.
func (r *Registry) sortedConfigMapNames() []string {
	names := make([]string, 0, len(r.plugins))
	for name := range r.plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Index returns the current set of plugin manifests, deduplicated by
// manifest name. If two ConfigMaps declare the same plugin name, the one
// whose backing ConfigMap name sorts first wins and the other is dropped
// (and logged) - mirrors the frontend's dedupeByPath in plugins.ts.
func (r *Registry) Index() []apisv1.PluginManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]string) // plugin name -> ConfigMap name that claimed it
	manifests := make([]apisv1.PluginManifest, 0, len(r.plugins))
	for _, cmName := range r.sortedConfigMapNames() {
		entry := r.plugins[cmName]
		if owner, ok := seen[entry.manifest.Name]; ok {
			r.logger.Info("Duplicate plugin name, dropping", "plugin", entry.manifest.Name, "configMap", cmName, "keptConfigMap", owner)
			continue
		}
		seen[entry.manifest.Name] = cmName
		manifests = append(manifests, entry.manifest)
	}
	return manifests
}

// File returns the contents of filename belonging to the plugin named
// pluginName, as currently known to the registry. When the plugin name is
// claimed by more than one ConfigMap, it resolves to the same one Index()
// would keep.
func (r *Registry) File(pluginName, filename string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, cmName := range r.sortedConfigMapNames() {
		entry := r.plugins[cmName]
		if entry.manifest.Name != pluginName {
			continue
		}
		data, ok := entry.files[filename]
		return data, ok
	}
	return nil, false
}
