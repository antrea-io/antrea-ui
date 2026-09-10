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
	"encoding/json"
	"fmt"
	"path"
	"strings"

	apisv1 "antrea.io/antrea-ui/apis/v1"
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
			if len(route.SidebarLabel) == 0 {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d]' is missing 'sidebarLabel'", i)
			}
			if route.SidebarLabel[apisv1.DefaultLocale] == "" {
				return nil, fmt.Errorf("manifest's 'federation.routes[%d].sidebarLabel' is missing a %q entry", i, apisv1.DefaultLocale)
			}
			for locale, label := range route.SidebarLabel {
				if label == "" {
					return nil, fmt.Errorf("manifest's 'federation.routes[%d].sidebarLabel' has an empty value for locale %q", i, locale)
				}
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
