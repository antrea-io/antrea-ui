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
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func newTestRegistry(t *testing.T) *Registry {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	t.Cleanup(r.Close)
	return r
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(data)
}

// configMap builds a plugin ConfigMap in the current data["manifest.json"] +
// binaryData["bundle.zip"] shape: manifest.json separate (small, human-readable), everything
// else (bundleFiles) zipped into one binaryData key - see registry.go's package doc for why. A
// nil bundleFiles omits binaryData entirely (for cases exercising a missing bundle.zip). Reuses
// version as the ConfigMap's ResourceVersion, standing in for the apiserver bumping it on every
// real write - every test upserting the same name with a new version this way still exercises
// handleUpsert's ResourceVersion-unchanged skip correctly.
func configMap(t *testing.T, name, pluginName, version, entry string, bundleFiles map[string]string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui", ResourceVersion: version},
		Data: map[string]string{
			"manifest.json": fmt.Sprintf(`{"name":%q,"version":%q,"entry":%q}`, pluginName, version, entry),
		},
	}
	if bundleFiles != nil {
		cm.BinaryData = map[string][]byte{"bundle.zip": buildZip(t, bundleFiles)}
	}
	return cm
}

func TestRegistryUpsertAndIndex(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(configMap(t, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string]string{
		"index.js": "console.log('hi')",
	}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())

	rc, size, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, int64(len("console.log('hi')")), size)
	assert.Equal(t, "console.log('hi')", readAll(t, rc))

	_, _, ok = r.File("pod-counter", "does-not-exist.js")
	assert.False(t, ok)

	_, _, ok = r.File("does-not-exist", "index.js")
	assert.False(t, ok)
}

// TestRegistryUpsertReadsManifestFromBinaryData covers a ConfigMap where manifest.json landed in
// BinaryData rather than Data - what `kubectl create configmap --from-file` does for a file the
// apiserver can't store as valid UTF-8 (a BOM, a stray non-UTF-8 byte). configMap (used by most
// other tests here) always writes it into Data, so this needs its own hand-built ConfigMap.
func TestRegistryUpsertReadsManifestFromBinaryData(t *testing.T) {
	r := newTestRegistry(t)

	manifestJSON := `{"name":"pod-counter","version":"0.1.0","entry":"index.js"}`
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-counter-plugin", Namespace: "antrea-ui", ResourceVersion: "0.1.0"},
		BinaryData: map[string][]byte{
			"manifest.json": []byte(manifestJSON),
			"bundle.zip":    buildZip(t, map[string]string{"index.js": "console.log('hi')"}),
		},
	}

	r.handleUpsert(cm)

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())
}

func TestRegistryDelete(t *testing.T) {
	r := newTestRegistry(t)
	cm := configMap(t, "pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "x"})

	r.handleUpsert(cm)
	assert.Len(t, r.Index(), 1)

	r.handleDelete(cm)
	assert.Empty(t, r.Index())
}

func TestRegistryUpdateReplacesPreviousContents(t *testing.T) {
	r := newTestRegistry(t)
	name := "pod-counter-plugin"

	r.handleUpsert(configMap(t, name, "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "v1"}))
	r.handleUpsert(configMap(t, name, "pod-counter", "0.2.0", "index.js", map[string]string{"index.js": "v2"}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.2.0", Entry: "index.js"},
	}, r.Index())
	rc, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "v2", readAll(t, rc))
}

func TestRegistrySkipsRedundantUpsertWithUnchangedResourceVersion(t *testing.T) {
	r := newTestRegistry(t)
	name := "pod-counter-plugin"

	r.handleUpsert(configMap(t, name, "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "v1"}))

	// Same ResourceVersion ("0.1.0", reused by the configMap helper - see its doc comment) as
	// an Update event replaying the informer's cache after a watch reconnect would carry, even
	// though nothing about the ConfigMap actually changed. A changed manifest/bundle here would
	// only show up if handleUpsert incorrectly re-parsed and re-extracted it.
	cm := configMap(t, name, "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "should not be applied"})
	r.handleUpsert(cm)

	rc, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "v1", readAll(t, rc))

	// A genuine change (new ResourceVersion) is still picked up normally.
	r.handleUpsert(configMap(t, name, "pod-counter", "0.2.0", "index.js", map[string]string{"index.js": "v2"}))
	rc, _, ok = r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "v2", readAll(t, rc))
}

func TestRegistrySkipsInvalidConfigMaps(t *testing.T) {
	withBundle := func(t *testing.T, manifestJSON string, bundleFiles map[string]string) *corev1.ConfigMap {
		t.Helper()
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "cm"},
			Data:       map[string]string{"manifest.json": manifestJSON},
			BinaryData: map[string][]byte{"bundle.zip": buildZip(t, bundleFiles)},
		}
	}

	cases := map[string]func(t *testing.T) *corev1.ConfigMap{
		"missing manifest.json": func(t *testing.T) *corev1.ConfigMap {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				BinaryData: map[string][]byte{"bundle.zip": buildZip(t, map[string]string{"index.js": "x"})},
			}
		},
		"missing bundle.zip": func(t *testing.T) *corev1.ConfigMap {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data:       map[string]string{"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js"}`},
			}
		},
		"malformed manifest.json": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, "not json", map[string]string{"index.js": "x"})
		},
		"malformed bundle.zip": func(t *testing.T) *corev1.ConfigMap {
			return &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data:       map[string]string{"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js"}`},
				BinaryData: map[string][]byte{"bundle.zip": []byte("not a zip")},
			}
		},
		"missing name": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, `{"version":"0.1.0","entry":"index.js"}`, map[string]string{"index.js": "x"})
		},
		"missing entry": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, `{"name":"plugin","version":"0.1.0"}`, map[string]string{"index.js": "x"})
		},
		"entry file not present": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, `{"name":"plugin","version":"0.1.0","entry":"index.js"}`, map[string]string{"other.js": "x"})
		},
		"route missing path": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route missing sidebarLabel": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route missing exposedModule": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","sidebarLabel":"Plugin"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route with an unknown kind": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page","kind":"route"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route nested under a routes-kind route": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
					{"path":"/policies","sidebarLabel":"Policies","exposedModule":"./PolicyRoutes","kind":"routes"},
					{"path":"/policies/audit","sidebarLabel":"Audit","exposedModule":"./PolicyAuditPage"}
				]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		// Same collision as above with the two routes declared the other way round (and spelled
		// with different slashes), since declaration order says nothing about which owns the path.
		"routes-kind route declared after the route it owns": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
					{"path":"policies/audit","sidebarLabel":"Audit","exposedModule":"./PolicyAuditPage"},
					{"path":"/policies/","sidebarLabel":"Policies","exposedModule":"./PolicyRoutes","kind":"routes"}
				]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route path under reserved api prefix": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/apiobjects","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route path under reserved auth prefix": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/authors","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route path is the root path": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"route path collapses to the root path via dot segments": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin/..","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"duplicate route path in the same manifest": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
					{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
					{"path":"/plugin","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
				]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"duplicate route path differing only by leading slash": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
					{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
					{"path":"plugin","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
				]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"duplicate route path differing only by doubled and trailing slashes": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
					{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
					{"path":"//plugin/","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
				]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"federation with no routes": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[]}}`,
				map[string]string{"index.js": "x", "remoteEntry.json": "x"})
		},
		"federation missing remoteEntry": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{}}`, map[string]string{"index.js": "x"})
		},
		"federation remoteEntry same file as entry": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"shared.js","federation":{"remoteEntry":"shared.js","routes":[{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"shared.js": "x"})
		},
		"federation remoteEntry same file as entry, differing only by a non-clean prefix": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"shared.js","federation":{"remoteEntry":"./shared.js","routes":[{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
				map[string]string{"shared.js": "x"})
		},
		"federation remoteEntry file not present": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json"}}`,
				map[string]string{"index.js": "x"})
		},
	}
	for name, buildCM := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestRegistry(t)
			r.handleUpsert(buildCM(t))
			assert.Empty(t, r.Index())
		})
	}
}

// TestRegistryHandleUpsertSkipsInvalidConfigMap exercises handleUpsert's own
// error handling (parsePluginConfigMap's error cases are covered directly,
// by message, in TestRegistrySkipsInvalidConfigMaps).
func TestRegistryHandleUpsertSkipsInvalidConfigMap(t *testing.T) {
	r := newTestRegistry(t)
	r.handleUpsert(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm"},
		Data:       map[string]string{"manifest.json": "not json"},
	})
	assert.Empty(t, r.Index())
}

func TestRegistryIndexIncludesFederation(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "policy-management-plugin", Namespace: "antrea-ui"},
		Data: map[string]string{
			"manifest.json": `{
				"name": "policy-management",
				"version": "0.2.0",
				"entry": "index.js",
				"federation": {
					"remoteEntry": "remoteEntry.json",
					"routes": [
						{"path": "/policies", "sidebarLabel": "Policy Management", "icon": "M0 0h16v16H0z", "exposedModule": "./PolicyRoutes", "kind": "routes"},
						{"path": "/policy-audit", "sidebarLabel": "Policy Audit Log", "exposedModule": "./PolicyAuditPage"},
						{"path": "/policy-summary", "sidebarLabel": "Policy Summary", "exposedModule": "./PolicySummaryPage", "kind": "component"}
					]
				}
			}`,
		},
		BinaryData: map[string][]byte{
			"bundle.zip": buildZip(t, map[string]string{"index.js": "x", "remoteEntry.json": "{}"}),
		},
	})

	assert.Equal(t, []apisv1.PluginManifest{{
		Name:    "policy-management",
		Version: "0.2.0",
		Entry:   "index.js",
		Federation: &apisv1.PluginFederation{
			RemoteEntry: "remoteEntry.json",
			Routes: []apisv1.PluginRoute{
				{Path: "/policies", SidebarLabel: "Policy Management", Icon: "M0 0h16v16H0z", ExposedModule: "./PolicyRoutes", Kind: "routes"},
				{Path: "/policy-audit", SidebarLabel: "Policy Audit Log", ExposedModule: "./PolicyAuditPage"},
				{Path: "/policy-summary", SidebarLabel: "Policy Summary", ExposedModule: "./PolicySummaryPage", Kind: "component"},
			},
		},
	}}, r.Index())
}

func federationConfigMap(t *testing.T, cmName, pluginName, entry string, routes string) *corev1.ConfigMap {
	t.Helper()
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "antrea-ui"},
		Data: map[string]string{
			"manifest.json": `{
				"name": "` + pluginName + `",
				"version": "0.1.0",
				"entry": "` + entry + `",
				"federation": {"remoteEntry": "remoteEntry.json", "routes": ` + routes + `}
			}`,
		},
		BinaryData: map[string][]byte{
			"bundle.zip": buildZip(t, map[string]string{
				entry:              "x",
				"remoteEntry.json": "x",
			}),
		},
	}
}

// TestRegistryIndexDropsPluginWhenAllFederationRoutesCollide pins the
// cross-plugin path normalization: the two manifests spell the same route
// path differently ("/policies" vs "//policies/"), so the test would still
// pass if seenRoutePaths compared raw, un-normalized paths.
func TestRegistryIndexDropsPluginWhenAllFederationRoutesCollide(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "index.js",
		`[{"path": "//policies/", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "a-configmap", "a-plugin", "index.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))

	manifests := r.Index()
	require.Len(t, manifests, 1)
	assert.Equal(t, "a-plugin", manifests[0].Name)
}

// TestRegistryIndexFiltersCollidingFederationRouteKeepsRestOfPlugin checks
// that a route collision drops only the colliding route, not the whole
// manifest: b-plugin's "entry" and its non-colliding "/other" route stay
// listed.
func TestRegistryIndexFiltersCollidingFederationRouteKeepsRestOfPlugin(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap(t, "a-configmap", "a-plugin", "a.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies", "sidebarLabel": "Policies Again", "exposedModule": "./OtherPage"},
			{"path": "/other", "sidebarLabel": "Other", "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: "Other", ExposedModule: "./OtherPage"},
	}, manifests[1].Federation.Routes)
}

// TestRegistryIndexFiltersFederationRouteUnderEarlierPluginsRouteTree checks
// that a "routes"-kind route's cross-plugin ownership of its own subtree,
// enforced within a single manifest by parsePluginConfigMap, also applies
// across manifests: a-plugin's "/policies" (kind "routes") owns
// "/policies/audit" just as surely as if b-plugin had declared "/policies"
// itself, so Index() must drop the nested route the same way it drops an
// exact duplicate.
func TestRegistryIndexFiltersFederationRouteUnderEarlierPluginsRouteTree(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap(t, "a-configmap", "a-plugin", "a.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page", "kind": "routes"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies/audit", "sidebarLabel": "Policy Audit", "exposedModule": "./AuditPage"},
			{"path": "/other", "sidebarLabel": "Other", "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: "Other", ExposedModule: "./OtherPage"},
	}, manifests[1].Federation.Routes)
}

// TestRegistryIndexFiltersRouteTreeRouteThatWouldClaimAnAlreadyClaimedPath
// covers the reverse declaration order from
// TestRegistryIndexFiltersFederationRouteUnderEarlierPluginsRouteTree:
// a-configmap sorts first and legitimately claims "/policies/audit" before
// anything marks it as living under a route tree, so when b-configmap's
// "/policies" (kind "routes") is processed afterwards, it's b's whole
// route - not just a's nested one - that has to be dropped, since keeping
// it would let it claim a path a-plugin already owns.
func TestRegistryIndexFiltersRouteTreeRouteThatWouldClaimAnAlreadyClaimedPath(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap(t, "a-configmap", "a-plugin", "a.js",
		`[{"path": "/policies/audit", "sidebarLabel": "Policy Audit", "exposedModule": "./AuditPage"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page", "kind": "routes"},
			{"path": "/other", "sidebarLabel": "Other", "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	require.NotNil(t, manifests[0].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/policies/audit", SidebarLabel: "Policy Audit", ExposedModule: "./AuditPage"},
	}, manifests[0].Federation.Routes)

	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: "Other", ExposedModule: "./OtherPage"},
	}, manifests[1].Federation.Routes)
}

// TestRegistryIndexAndFileStayConsistentWhenAllRoutesCollide reproduces the
// scenario where dropping a whole plugin for an all-routes collision, without
// claiming its name, let a later ConfigMap reusing that name get listed in
// Index() with an entry file File() would never actually resolve to (File()
// always resolves a name to its first-sorted ConfigMap, independently of
// Index()'s own bookkeeping).
func TestRegistryIndexAndFileStayConsistentWhenAllRoutesCollide(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap(t, "a-configmap", "aaa", "a.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	// b-configmap sorts before c-configmap, and claims the "dup" name first;
	// its one route collides with aaa's, so the whole manifest is dropped.
	r.handleUpsert(federationConfigMap(t, "b-configmap", "dup", "b.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "c-configmap", "dup", "c.js",
		`[{"path": "/other", "sidebarLabel": "Other", "exposedModule": "./Page"}]`))

	manifests := r.Index()
	names := make([]string, len(manifests))
	for i, m := range manifests {
		names[i] = m.Name
	}
	assert.Equal(t, []string{"aaa"}, names, "dup must not be listed at all, from either ConfigMap")

	_, _, ok := r.File("dup", "c.js")
	assert.False(t, ok, "c-configmap's entry must never be served for a name Index() doesn't list")
}

func TestRegistryDuplicatePluginNameKeepsLowerConfigMapName(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(configMap(t, "b-configmap", "pod-counter", "2.0.0", "index.js", map[string]string{"index.js": "b"}))
	r.handleUpsert(configMap(t, "a-configmap", "pod-counter", "1.0.0", "index.js", map[string]string{"index.js": "a"}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "1.0.0", Entry: "index.js"},
	}, r.Index())
	rc, _, ok := r.File("pod-counter", "index.js")
	require.True(t, ok)
	assert.Equal(t, "a", readAll(t, rc))
}

func TestRegistryRejectsNewConfigMapPluginPastLimit(t *testing.T) {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 1, 0, 0)
	t.Cleanup(r.Close)

	r.handleUpsert(configMap(t, "first-cm", "first", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	r.handleUpsert(configMap(t, "second-cm", "second", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.1.0", Entry: "index.js"}}, r.Index())

	// An update to the already-tracked plugin is never blocked by the limit.
	r.handleUpsert(configMap(t, "first-cm", "first", "0.2.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.2.0", Entry: "index.js"}}, r.Index())
}

func TestRegistryRejectsConfigMapBundlePastTheDecompressedSizeLimit(t *testing.T) {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 100)
	t.Cleanup(r.Close)

	// A single entry over the limit...
	r.handleUpsert(configMap(t, "plugin-cm", "plugin", "0.1.0", "index.js", map[string]string{
		"index.js": strings.Repeat("x", 200),
	}))
	assert.Empty(t, r.Index(), "a bundle decompressing past the limit must be rejected")

	// ...and several entries that only exceed it combined, must both be rejected: the limit
	// applies to the bundle's total decompressed size, not any one entry's.
	r.handleUpsert(configMap(t, "plugin-cm", "plugin", "0.1.0", "index.js", map[string]string{
		"index.js": strings.Repeat("x", 60),
		"other.js": strings.Repeat("y", 60),
	}))
	assert.Empty(t, r.Index())

	// A bundle within the limit is accepted.
	r.handleUpsert(configMap(t, "plugin-cm", "plugin", "0.1.0", "index.js", map[string]string{
		"index.js": strings.Repeat("x", 50),
	}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "plugin", Version: "0.1.0", Entry: "index.js"}}, r.Index())
}
