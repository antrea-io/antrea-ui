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

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// PluginManifest describes a frontend plugin discovered by the backend from a labeled ConfigMap
// or a plugin directory (see pkg/plugins). Page extensions (edge details, flow table columns) are
// always registered by the plugin's own code - the host needs an actual
// function reference for those. Federation is the one exception: it lets the
// host build whole-page routes from data alone, without running the plugin's
// code first.
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Entry is the plugin's entry file: a filename that must be an entry in the same plugin's
	// bundle.zip, from either source. Always eagerly import()-ed at startup, for whatever
	// page-extension registration the plugin's code performs (see
	// antrea-ui-plugin-sdk) - regardless of whether Federation below is also
	// set.
	Entry string `json:"entry"`

	// CompatibleAntreaUI is a node-semver range (e.g. "1.0.0-rc.1 || 1.0.0-rc.2") of the full
	// versions of the frontend that may load this plugin, prerelease suffix included: the ones
	// the plugin was built and tested against. Required. The backend only checks that it is
	// present and within a narrow subset of node-semver's syntax: "||"-separated sets of
	// space-separated comparators ("<", "<=", ">", ">=", "=", "^", "~" or none), each followed by
	// a full MAJOR.MINOR.PATCH[-prerelease] version (no x-ranges, "*", hyphen ranges, partial
	// versions, "v" prefix or build metadata) and at most 256 characters, so a range it accepts
	// always parses in node-semver. Each frontend evaluates it against its own build-time version
	// before fetching any plugin code, since only the frontend knows which version it actually is
	// (the backend serves the same index to every frontend).
	CompatibleAntreaUI string `json:"compatibleAntreaUI"`

	// Federation optionally declares the plugin's module federation remote
	// and the routes it serves, as data instead of the plugin's code calling
	// registerRoute()/registerSidebarEntry() (see antrea-ui-plugin-sdk). Its
	// own file, not Entry, so an eager page extension and a lazily-loaded
	// federated page never have to share one build artifact.
	Federation *PluginFederation `json:"federation,omitempty"`

	// BundleSha256 is the hex SHA-256 (64 characters, either case) of the plugin's bundle.zip,
	// as delivered (the ConfigMap binaryData value, or the file on disk) - not of its extracted
	// contents. It is
	// what makes a signature over this manifest cover the bundle too: manifest.json is the only
	// artifact that is signed (manifest.json.asc), and the bundle is authenticated transitively
	// through this digest. Required when plugins.signature.trustedKeys is non-empty; optional
	// otherwise, but always verified when present.
	BundleSha256 string `json:"bundleSha256,omitempty"`

	// BundleSource tells the backend where to download the plugin's bundle.zip from, for a
	// plugin whose ConfigMap carries only manifest.json and its signature (a bundle too large
	// for a ConfigMap's ~1MiB limit). Nil for a plugin whose bundle.zip ships in the ConfigMap
	// or the plugin directory. It is only supported for ConfigMap plugins and requires
	// BundleSha256: the digest is what pins the bytes accepted, wherever they come from.
	BundleSource *PluginBundleSource `json:"bundleSource,omitempty"`

	// SupportBundle optionally declares a source of diagnostics that antrea-ui collects into its
	// own support bundle (see docs/supportbundle.md). A declaration with no variant this backend
	// recognizes does not reject the manifest: the source is skipped, and the support bundle
	// records why.
	SupportBundle *SupportBundleSourceSpec `json:"supportBundle,omitempty"`
}

// PluginBundleSource is where a plugin's bundle.zip is downloaded from. Exactly one transport
// must be set.
type PluginBundleSource struct {
	// APIServer downloads the bundle through the Kubernetes API server, from an aggregated API
	// server (a service the plugin's provider runs).
	APIServer *PluginBundleAPIServer `json:"apiServer,omitempty"`

	// HTTP downloads the bundle from an HTTP(S) URL, which the plugin's ConfigMap carries under
	// the data key "bundleURL" rather than the manifest, as it depends on where the plugin is
	// installed (e.g. the namespace of the Service serving the bundle). The request is
	// unauthenticated; the digest in the manifest is what makes the response trustworthy.
	HTTP *PluginBundleHTTP `json:"http,omitempty"`
}

// PluginBundleHTTP selects the HTTP transport for a plugin's bundle. It has no settings: the URL
// is in the ConfigMap, not the manifest.
type PluginBundleHTTP struct{}

// PluginBundleAPIServer locates a bundle served by an aggregated API server.
type PluginBundleAPIServer struct {
	// Path is the base path of the aggregated API group serving the bundle, which must be
	// exactly "/apis/<group>/<version>", e.g. "/apis/ui.example/v1". The backend requests
	// "<Path>/uipluginbundles/<bundleSha256>/download" from the API server it is configured
	// with: the manifest picks the group and version, never the host nor the resource.
	Path string `json:"path"`
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

// PluginRouteKindComponent and PluginRouteKindRoutes are the only values PluginRoute.Kind may
// hold: "component" and "routes". Kept as plain strings, not a distinct type, so the field
// round-trips through JSON with no custom (un)marshaling and stays directly comparable in
// parsePluginConfigMap, which rejects a manifest carrying anything else. A frontend host mirrors
// the same two values as a string-literal union (see PluginManifest in
// client/web/antrea-ui/src/plugins.ts).
const (
	PluginRouteKindComponent = "component"
	PluginRouteKindRoutes    = "routes"
)

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
	// Kind selects how a host mounts ExposedModule's export for this route: one of
	// PluginRouteKindComponent (the default, and the only behavior before this field existed) or
	// PluginRouteKindRoutes. Component expects a `default` export that is a single page
	// component; Routes expects a `routes` export instead - a whole route tree the plugin fully
	// owns, letting it nest its own sub-paths and register its own route-level providers without
	// the host knowing anything about them.
	Kind string `json:"kind,omitempty"`
	// Requires, when set, makes a host hide this route and its sidebar entry from users who
	// don't meet it, evaluated against their AccessSummary. It is a UI hint, not authorization:
	// Kubernetes RBAC still authorizes every API call the plugin's page makes.
	Requires *PluginAccessRequirement `json:"requires,omitempty"`
}

// PluginAccessRequirement mirrors PluginAccessRequirement in antrea-ui-plugin-sdk.
//
// ClusterAdmin decides visibility alone: when it is set, NamespaceScope and Resources are ignored
// for visibility, but NamespaceScope still enables Namespace scope behavior. Otherwise, without
// NamespaceScope, Resources must hold cluster-wide. With it, the item is for a Namespace-aware
// page: its verdict comes from Resources, and it is available to a user for whom that verdict is
// not "denied", who may list Namespaces, or who is named in at least one RoleBinding. None of this
// is a per-Namespace review, and Kubernetes RBAC still authorizes every call.
type PluginAccessRequirement struct {
	// ClusterAdmin limits the item to users whose AccessSummary reports ClusterAdmin.
	ClusterAdmin bool `json:"clusterAdmin,omitempty"`
	// NamespaceScope declares the page Namespace-aware.
	NamespaceScope bool `json:"namespaceScope,omitempty"`
	// Resources lists queries checked against the user's rules. An unknown verdict (incomplete
	// rules) counts as passing, the same as for the built-in pages.
	Resources *PluginResourceRequirement `json:"resources,omitempty"`
}

// PluginResourceRequirement is "resources" of a PluginAccessRequirement: a plain array of queries
// (all must pass, or with NamespaceScope any of them), {"allOf": [...]} (all must pass), or
// {"anyOf": [...]} (at least one). It is served in the form the manifest wrote it.
type PluginResourceRequirement struct {
	AllOf []PluginResourceQuery
	AnyOf []PluginResourceQuery
	// Array is set when the manifest wrote the plain array, whose queries are in AllOf.
	Array bool
}

// UnmarshalJSON accepts the plain array, or an object with exactly one of "allOf" and "anyOf".
// Unknown keys are rejected, like the rest of a requirement.
func (r *PluginResourceRequirement) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var queries []PluginResourceQuery
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&queries); err != nil {
			return err
		}
		*r = PluginResourceRequirement{AllOf: queries, Array: true}
		return nil
	}
	var decoded struct {
		AllOf []PluginResourceQuery `json:"allOf"`
		AnyOf []PluginResourceQuery `json:"anyOf"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if (decoded.AllOf == nil) == (decoded.AnyOf == nil) {
		return errors.New("must be an array of queries, or an object with exactly one of 'allOf' and 'anyOf'")
	}
	*r = PluginResourceRequirement{AllOf: decoded.AllOf, AnyOf: decoded.AnyOf}
	return nil
}

// MarshalJSON writes the form the manifest used.
func (r PluginResourceRequirement) MarshalJSON() ([]byte, error) {
	switch {
	case r.Array:
		return json.Marshal(r.AllOf)
	case r.AnyOf != nil:
		return json.Marshal(struct {
			AnyOf []PluginResourceQuery `json:"anyOf"`
		}{r.AnyOf})
	default:
		return json.Marshal(struct {
			AllOf []PluginResourceQuery `json:"allOf"`
		}{r.AllOf})
	}
}

// UnmarshalJSON rejects unknown requirement and resource-query fields so a misspelled
// permission cannot silently become an unrestricted route. It also rejects a null anywhere in
// the requirement, which decoding would turn into "not written" (dropping a gate), while a host
// reading the manifest as written treats it as malformed.
func (r *PluginAccessRequirement) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	if containsNull(raw) {
		return errors.New("must not contain null: leave the field out instead")
	}
	type requirement PluginAccessRequirement
	var decoded requirement
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*r = PluginAccessRequirement(decoded)
	return nil
}

func containsNull(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, e := range v {
			if containsNull(e) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(v, containsNull)
	}
	return false
}

// PluginResourceQuery mirrors ResourceQuery in antrea-ui-components' access API.
type PluginResourceQuery struct {
	// Group is the API group, "" for the core group.
	Group    string `json:"group"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Name     string `json:"name,omitempty"`
}
