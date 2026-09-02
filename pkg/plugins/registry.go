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
	"path"
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
	if manifest.Federation != nil {
		if manifest.Federation.RemoteEntry == "" {
			return nil, fmt.Errorf("manifest's federation is missing 'remoteEntry'")
		}
		if manifest.Federation.RemoteEntry == manifest.Entry {
			return nil, fmt.Errorf("manifest's 'federation.remoteEntry' must not be the same file as 'entry' - the host always import()s 'entry' as a plain ES module, which a federation remote entry is not")
		}
		if _, ok := files[manifest.Federation.RemoteEntry]; !ok {
			return nil, fmt.Errorf("remote entry file %q referenced by manifest's federation not found in ConfigMap", manifest.Federation.RemoteEntry)
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
// manifest name and by federation route path.
//
// If two ConfigMaps declare the same plugin name, the one whose backing
// ConfigMap name sorts first wins outright; the other is dropped (and
// logged) in full, including its 'entry' - File would otherwise still serve
// files for the loser, which Index no longer lists.
//
// If two different plugins' federation routes collide on path, only the
// colliding routes are dropped from the later plugin (by ConfigMap name
// sort), not the whole manifest - unlike the plugin-name case, 'entry' is
// still eagerly import()ed by every listed manifest regardless of
// 'federation' (see PluginManifest.Entry), so dropping the manifest would
// cost that plugin's page-extension registrations over a route only an
// out-of-tree, federation-aware host ever mounts. This differs from the
// frontend's dedupeByPath in plugins.ts in the same way: dedupeByPath also
// drops individual routes rather than a whole plugin, but its winner is
// registration order (whichever plugin's entry module ran first), not
// ConfigMap name sort. If every one of a plugin's routes collides, its
// federation is left with no routes to serve, which is as meaningless as
// the empty-routes case parsePluginConfigMap already rejects, so the whole
// manifest is dropped instead - same as the plugin-name case.
//
// Either way, a plugin name is claimed by the first ConfigMap that reaches
// it in sort order, whether or not that ConfigMap's manifest ends up listed
// - a name left unclaimed just because its manifest was dropped for an
// all-routes collision would let a later ConfigMap reusing that name win
// the name outright in this loop, while File() (which walks the same order
// independently) kept resolving it to the first, dropped, ConfigMap.
func (r *Registry) Index() []apisv1.PluginManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seenNames := make(map[string]string)       // plugin name -> ConfigMap name that claimed it
	seenRoutePaths := make(map[string]string)  // normalized route path -> plugin name that claimed it
	seenRouteOwners := make(map[string]string) // normalized path of a claimed PluginRouteKindRoutes route -> plugin name that claimed it
	manifests := make([]apisv1.PluginManifest, 0, len(r.plugins))
	for _, cmName := range r.sortedConfigMapNames() {
		entry := r.plugins[cmName]
		if owner, ok := seenNames[entry.manifest.Name]; ok {
			r.logger.Info("Duplicate plugin name, dropping", "plugin", entry.manifest.Name, "configMap", cmName, "keptConfigMap", owner)
			continue
		}

		manifest := entry.manifest
		if manifest.Federation != nil {
			kept := make([]apisv1.PluginRoute, 0, len(manifest.Federation.Routes))
			for _, route := range manifest.Federation.Routes {
				normalized := normalizeRoutePath(route.Path)
				if owner, ok := seenRoutePaths[normalized]; ok {
					r.logger.Info("Plugin's federation route path collides with an earlier plugin, dropping the route", "plugin", manifest.Name, "configMap", cmName, "path", route.Path, "collidesWithPlugin", owner)
					continue
				}
				if ownerPath, owner, ok := findRouteOwner(seenRouteOwners, normalized); ok {
					r.logger.Info("Plugin's federation route path falls under an earlier plugin's route-tree-owning route, dropping the route", "plugin", manifest.Name, "configMap", cmName, "path", route.Path, "collidesWithPlugin", owner, "ownerPath", ownerPath)
					continue
				}
				if route.Kind == apisv1.PluginRouteKindRoutes {
					if nestedPath, owner, ok := findRouteUnder(seenRoutePaths, normalized); ok {
						r.logger.Info("Plugin's route-tree-owning federation route already contains an earlier plugin's route, dropping the route", "plugin", manifest.Name, "configMap", cmName, "path", route.Path, "collidesWithPlugin", owner, "nestedPath", nestedPath)
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
				r.logger.Info("All of plugin's federation routes collided with an earlier plugin, dropping the plugin", "plugin", manifest.Name, "configMap", cmName)
				// Still claim the name: this ConfigMap is the one File() would
				// otherwise serve for it (sortedConfigMapNames order matches this
				// loop's), and leaving it unclaimed would let a later ConfigMap
				// reusing the same name win Index() while File() kept resolving
				// to this one.
				seenNames[manifest.Name] = cmName
				continue
			}
			if len(kept) != len(manifest.Federation.Routes) {
				federation := *manifest.Federation
				federation.Routes = kept
				manifest.Federation = &federation
			}
		}

		seenNames[manifest.Name] = cmName
		manifests = append(manifests, manifest)
	}
	return manifests
}

// File returns the contents of filename belonging to the plugin named
// pluginName, as currently known to the registry. When the plugin name is
// claimed by more than one ConfigMap, it resolves to the ConfigMap that
// claimed the name in Index() - usually the one whose manifest Index()
// lists, except when every one of that manifest's federation routes
// collided and Index() dropped it entirely while still leaving it holding
// the name (see Index).
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
