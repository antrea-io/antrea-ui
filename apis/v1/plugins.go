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
// labeled ConfigMap. It carries just enough for the host to fetch and
// import() the right file. Page extensions (e.g. edge details, flow table
// columns) are always registered by the plugin's own code, since the host
// needs an actual function reference for those; a whole-page route is the
// one exception - Route lets the host build it from data alone, without
// running any of the plugin's code first (see Route's own doc).
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Entry   string `json:"entry"`

	// Route optionally declares a whole-page route/sidebar entry for this
	// plugin as data the host can act on immediately, instead of waiting for
	// the plugin's own code to call registerRoute()/registerSidebarEntry()
	// (see antrea-ui-plugin-sdk) as a side effect of loading it. Chiefly
	// useful for a plugin loaded via module federation (see Federation),
	// where the point of the exercise is to defer fetching/running the
	// plugin's code until its route is actually visited - which requires the
	// host to already know the route before that happens.
	Route *PluginRoute `json:"route,omitempty"`

	// Federation optionally declares that Entry is a module federation
	// remote entry (e.g. a Native Federation "remoteEntry.json"), rather
	// than a module the host can plainly import(). A host that understands
	// this field loads ExposedModule out of it via its federation runtime
	// instead; a host that doesn't (or a plugin that omits this field
	// entirely) keeps using Entry as a plain ES module, unaffected.
	Federation *PluginFederation `json:"federation,omitempty"`
}

// PluginRoute is the data-only counterpart to the antrea-ui-plugin-sdk
// registerRoute()/registerSidebarEntry() calls: same information (a path and
// a sidebar label), just readable off the manifest instead of requiring the
// plugin's code to run and call back into the host.
type PluginRoute struct {
	Path         string `json:"path"`
	SidebarLabel string `json:"sidebarLabel"`
	// Icon is optional SVG path "d" data for a 16x16 (viewBox "0 0 16 16")
	// icon, matching PluginSidebarEntry.icon in antrea-ui-plugin-sdk.
	Icon string `json:"icon,omitempty"`
}

// PluginFederation carries the one extra piece of information a federation
// runtime needs beyond a remote entry URL: which of that remote's exposed
// modules to load. Shared-dependency negotiation is otherwise handled
// entirely by the remote entry file itself (e.g. by the plugin's own
// federation.config.js at build time), so nothing else belongs here.
type PluginFederation struct {
	ExposedModule string `json:"exposedModule"`
}
