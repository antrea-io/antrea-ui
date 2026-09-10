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
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

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
						{"path": "/policies", "sidebarLabel":{"en":"Policy Management"}, "icon": "M0 0h16v16H0z", "exposedModule": "./PolicyRoutes", "kind": "routes"},
						{"path": "/policy-audit", "sidebarLabel":{"en":"Policy Audit Log"}, "exposedModule": "./PolicyAuditPage"},
						{"path": "/policy-summary", "sidebarLabel":{"en":"Policy Summary"}, "exposedModule": "./PolicySummaryPage", "kind": "component"}
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
				{Path: "/policies", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Policy Management"}, Icon: "M0 0h16v16H0z", ExposedModule: "./PolicyRoutes", Kind: "routes"},
				{Path: "/policy-audit", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Policy Audit Log"}, ExposedModule: "./PolicyAuditPage"},
				{Path: "/policy-summary", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Policy Summary"}, ExposedModule: "./PolicySummaryPage", Kind: "component"},
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
		`[{"path": "//policies/", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "a-configmap", "a-plugin", "index.js",
		`[{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page"}]`))

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
		`[{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies", "sidebarLabel":{"en":"Policies Again"}, "exposedModule": "./OtherPage"},
			{"path": "/other", "sidebarLabel":{"en":"Other"}, "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Other"}, ExposedModule: "./OtherPage"},
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
		`[{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page", "kind": "routes"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies/audit", "sidebarLabel":{"en":"Policy Audit"}, "exposedModule": "./AuditPage"},
			{"path": "/other", "sidebarLabel":{"en":"Other"}, "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Other"}, ExposedModule: "./OtherPage"},
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
		`[{"path": "/policies/audit", "sidebarLabel":{"en":"Policy Audit"}, "exposedModule": "./AuditPage"}]`))
	r.handleUpsert(federationConfigMap(t, "b-configmap", "b-plugin", "b.js",
		`[
			{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page", "kind": "routes"},
			{"path": "/other", "sidebarLabel":{"en":"Other"}, "exposedModule": "./OtherPage"}
		]`))

	manifests := r.Index()
	require.Len(t, manifests, 2)
	assert.Equal(t, "a-plugin", manifests[0].Name)
	require.NotNil(t, manifests[0].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/policies/audit", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Policy Audit"}, ExposedModule: "./AuditPage"},
	}, manifests[0].Federation.Routes)

	assert.Equal(t, "b-plugin", manifests[1].Name)
	require.NotNil(t, manifests[1].Federation)
	assert.Equal(t, []apisv1.PluginRoute{
		{Path: "/other", SidebarLabel: apisv1.PluginSidebarLabel{"en": "Other"}, ExposedModule: "./OtherPage"},
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
		`[{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page"}]`))
	// b-configmap sorts before c-configmap, and claims the "dup" name first;
	// its one route collides with aaa's, so the whole manifest is dropped.
	r.handleUpsert(federationConfigMap(t, "b-configmap", "dup", "b.js",
		`[{"path": "/policies", "sidebarLabel":{"en":"Policies"}, "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap(t, "c-configmap", "dup", "c.js",
		`[{"path": "/other", "sidebarLabel":{"en":"Other"}, "exposedModule": "./Page"}]`))

	manifests := r.Index()
	names := make([]string, len(manifests))
	for i, m := range manifests {
		names[i] = m.Name
	}
	assert.Equal(t, []string{"aaa"}, names, "dup must not be listed at all, from either ConfigMap")

	_, _, ok := r.File("dup", "c.js")
	assert.False(t, ok, "c-configmap's entry must never be served for a name Index() doesn't list")
}

func TestRegistryIndexMergesBothSources(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "disk-plugin", podCounterManifest("disk-plugin", "0.1.0"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	t.Cleanup(r.Close)
	r.handleUpsert(configMap(t, "cm-plugin", "cm-plugin", "0.1.0", "index.js", map[string]string{"index.js": "x"}))

	startDirectoryWatch(t, r, dir)

	waitFor(t, time.Second, func() bool { return len(r.Index()) == 2 })
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

func TestRegistryDuplicatePluginNameKeepsConfigMapOverDirectory(t *testing.T) {
	dir := t.TempDir()
	writePluginDir(t, dir, "shared-name", podCounterManifest("shared", "from-disk"), podCounterBundle())

	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	t.Cleanup(r.Close)
	r.handleUpsert(configMap(t, "shared-name", "shared", "from-configmap", "index.js", map[string]string{"index.js": "x"}))

	startDirectoryWatch(t, r, dir)

	// "configmap/shared-name" sorts before "directory/shared-name", so the ConfigMap always
	// wins this collision regardless of load order.
	waitFor(t, time.Second, func() bool {
		manifests := r.Index()
		return len(manifests) == 1 && manifests[0].Version == "from-configmap"
	})
}
