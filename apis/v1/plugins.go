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

// PluginManifest describes a frontend plugin discovered by the backend from a
// labeled ConfigMap. Page extensions (edge details, flow table columns) are
// always registered by the plugin's own code - the host needs an actual
// function reference for those. Routes is the one exception: it lets the
// host build a whole-page route from data alone, without running the
// plugin's code first.
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Entry is the plugin's entry file: a filename, a key in the same
	// ConfigMap's data. Always eagerly import()-ed at startup, for whatever
	// page-extension registration the plugin's code performs (see
	// antrea-ui-plugin-sdk) - regardless of whether Routes/Federation below
	// are also set.
	Entry string `json:"entry"`

	// Routes optionally declares whole-page routes as data, instead of the
	// plugin's code calling registerRoute()/registerSidebarEntry() (see
	// antrea-ui-plugin-sdk). A list because registerRoute() isn't limited to
	// one call either. Requires Federation: each entry's ExposedModule is
	// what the host actually mounts at its Path, and nothing else can supply
	// that yet - a plain custom-element route is already cheap via code, so
	// there's nothing here worth deferring for it.
	Routes []PluginRoute `json:"routes,omitempty"`

	// Federation optionally declares the plugin's module federation remote -
	// its own file, not Entry, so an eager page extension and a lazily-loaded
	// federated page never have to share one build artifact.
	Federation *PluginFederation `json:"federation,omitempty"`
}

// PluginRoute mirrors registerRoute()/registerSidebarEntry()'s
// antrea-ui-plugin-sdk info, as data.
type PluginRoute struct {
	Path         string `json:"path"`
	SidebarLabel string `json:"sidebarLabel"`
	// Icon is optional SVG path "d" data, 16x16 viewBox "0 0 16 16", matching
	// PluginSidebarEntry.icon in antrea-ui-plugin-sdk.
	Icon string `json:"icon,omitempty"`
	// ExposedModule names which component Federation.RemoteEntry exposes for
	// this route.
	ExposedModule string `json:"exposedModule"`
}

// PluginFederation is the plugin's federation remote entry file. Which
// component to load from it is per-route (PluginRoute.ExposedModule), since
// one remote can expose components for more than one route.
type PluginFederation struct {
	RemoteEntry string `json:"remoteEntry"`
}
