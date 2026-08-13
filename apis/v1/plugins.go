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
// needs an actual function reference for those - Entry is always eagerly
// imported for that purpose alone, whether or not Route/Federation are also
// present. A whole-page route is the one exception - Route lets the host
// build it from data alone, without running any of the plugin's code first
// (see Route's own doc). A plugin can do both at once (e.g. an eager Entry
// that only registers a page-extension renderer, plus a Route/Federation
// pair describing an unrelated, separately-built, lazily-loaded page) -
// Federation.RemoteEntry is deliberately its own file, not Entry, so the two
// concerns never have to share one build artifact.
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Entry is always eagerly import()-ed by the host at startup, for
	// whatever page-extension registration the plugin's code performs via
	// antrea-ui-plugin-sdk (registerEdgeExtraRenderer() etc) - regardless of
	// whether Route/Federation below are also present. A plugin with
	// nothing to register eagerly still needs a (possibly no-op) Entry: it
	// remains required.
	Entry string `json:"entry"`

	// Route optionally declares a whole-page route/sidebar entry for this
	// plugin as data the host can act on immediately, instead of waiting for
	// the plugin's own code to call registerRoute()/registerSidebarEntry()
	// (see antrea-ui-plugin-sdk) as a side effect of loading it. Currently
	// only meaningful paired with Federation (required whenever Route is
	// set - see parsePluginConfigMap): Federation.ExposedModule is what the
	// host actually mounts at Route.Path. A plain custom-element plugin has
	// no equivalent here (there's no "Tag" field, unlike the SDK's
	// PluginRoute) because registering such a route via code is already
	// cheap - the data-only path exists specifically to defer loading a
	// route's code, which only pays for itself when there's a nontrivial
	// federated bundle behind it. Nothing prevents adding a lighter,
	// Federation-less variant later if a use case for it shows up.
	Route *PluginRoute `json:"route,omitempty"`

	// Federation optionally declares a module federation remote (e.g. a
	// Native Federation "remoteEntry.json") the host can lazily load a
	// component out of - normally paired with Route, to render at that
	// route. Deliberately a file of its own rather than reusing Entry: Entry
	// stays reserved for whatever a plugin needs eagerly loaded (page
	// extensions), which may have nothing to do with, and may be far
	// cheaper than, a lazily-loaded federated page.
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

// PluginFederation carries the two things a federation runtime needs beyond
// what Entry already provides: its own remote entry file (see
// PluginManifest's doc for why this isn't just Entry), and which of that
// remote's exposed modules to load. Shared-dependency negotiation is
// otherwise handled entirely by the remote entry file itself (e.g. by the
// plugin's own federation.config.js at build time), so nothing else belongs
// here.
type PluginFederation struct {
	RemoteEntry   string `json:"remoteEntry"`
	ExposedModule string `json:"exposedModule"`
}
