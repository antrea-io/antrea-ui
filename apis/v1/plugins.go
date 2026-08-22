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

package v1

// PluginManifest describes a frontend plugin discovered by the backend from a labeled ConfigMap
// or a plugin directory (see pkg/plugins). Page extensions (edge details, flow table columns) are
// always registered by the plugin's own code - the host needs an actual
// function reference for those. Federation is the one exception: it lets the
// host build whole-page routes from data alone, without running the plugin's
// code first.
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Entry is the plugin's entry file: a filename, a key in the same ConfigMap's data or a
	// file in the same plugin directory. Always eagerly import()-ed at startup, for whatever
	// page-extension registration the plugin's code performs (see
	// antrea-ui-plugin-sdk) - regardless of whether Federation below is also
	// set.
	Entry string `json:"entry"`

	// Federation optionally declares the plugin's module federation remote
	// and the routes it serves, as data instead of the plugin's code calling
	// registerRoute()/registerSidebarEntry() (see antrea-ui-plugin-sdk). Its
	// own file, not Entry, so an eager page extension and a lazily-loaded
	// federated page never have to share one build artifact.
	Federation *PluginFederation `json:"federation,omitempty"`
}

// PluginFederation is the plugin's federation remote entry file, plus the
// routes to load components from it for. A route's ExposedModule is what the
// host actually mounts at its Path - there's no equivalent for a plain
// custom-element route, since that's already cheap via code, with nothing
// here worth deferring for it.
type PluginFederation struct {
	RemoteEntry string        `json:"remoteEntry"`
	Routes      []PluginRoute `json:"routes"`
}

// PluginRoute mirrors registerRoute()/registerSidebarEntry()'s
// antrea-ui-plugin-sdk info, as data, plus which component to mount.
type PluginRoute struct {
	Path         string `json:"path"`
	SidebarLabel string `json:"sidebarLabel"`
	// Icon is optional SVG path "d" data, 16x16 viewBox "0 0 16 16", matching
	// PluginSidebarEntry.icon in antrea-ui-plugin-sdk.
	Icon string `json:"icon,omitempty"`
	// ExposedModule names which component the enclosing PluginFederation's
	// RemoteEntry exposes for this route.
	ExposedModule string `json:"exposedModule"`
}
