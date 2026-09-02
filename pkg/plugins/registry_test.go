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

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func newTestRegistry(t *testing.T) *Registry {
	return NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true")
}

func configMap(name, pluginName, version, entry string, extraFiles map[string]string) *corev1.ConfigMap {
	data := map[string]string{
		"manifest.json": `{"name":"` + pluginName + `","version":"` + version + `","entry":"` + entry + `"}`,
	}
	for k, v := range extraFiles {
		data[k] = v
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui"},
		Data:       data,
	}
}

func TestRegistryUpsertAndIndex(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(configMap("pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string]string{
		"index.js": "console.log('hi')",
	}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.1.0", Entry: "index.js"},
	}, r.Index())

	data, ok := r.File("pod-counter", "index.js")
	assert.True(t, ok)
	assert.Equal(t, "console.log('hi')", string(data))

	_, ok = r.File("pod-counter", "does-not-exist.js")
	assert.False(t, ok)

	_, ok = r.File("does-not-exist", "index.js")
	assert.False(t, ok)
}

func TestRegistryDelete(t *testing.T) {
	r := newTestRegistry(t)
	cm := configMap("pod-counter-plugin", "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "x"})

	r.handleUpsert(cm)
	assert.Len(t, r.Index(), 1)

	r.handleDelete(cm)
	assert.Empty(t, r.Index())
}

func TestRegistryUpdateReplacesPreviousContents(t *testing.T) {
	r := newTestRegistry(t)
	name := "pod-counter-plugin"

	r.handleUpsert(configMap(name, "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "v1"}))
	r.handleUpsert(configMap(name, "pod-counter", "0.2.0", "index.js", map[string]string{"index.js": "v2"}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "0.2.0", Entry: "index.js"},
	}, r.Index())
	data, ok := r.File("pod-counter", "index.js")
	assert.True(t, ok)
	assert.Equal(t, "v2", string(data))
}

func TestRegistrySkipsInvalidConfigMaps(t *testing.T) {
	cases := map[string]struct {
		cm      *corev1.ConfigMap
		wantErr string
	}{
		"missing manifest.json": {
			cm:      &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm"}, Data: map[string]string{"index.js": "x"}},
			wantErr: "missing manifest.json",
		},
		"malformed manifest.json": {
			cm:      &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm"}, Data: map[string]string{"manifest.json": "not json"}},
			wantErr: "invalid manifest.json",
		},
		"missing name": {
			cm:      configMap("cm", "", "0.1.0", "index.js", map[string]string{"index.js": "x"}),
			wantErr: "manifest is missing 'name'",
		},
		"missing entry": {
			cm:      configMap("cm", "plugin", "0.1.0", "", map[string]string{"index.js": "x"}),
			wantErr: "manifest is missing 'entry'",
		},
		"entry file not present": {
			cm:      configMap("cm", "plugin", "0.1.0", "index.js", nil),
			wantErr: "entry file \"index.js\" referenced by manifest not found",
		},
		"route missing path": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0]' is missing 'path'",
		},
		"route missing sidebarLabel": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0]' is missing 'sidebarLabel'",
		},
		"route missing exposedModule": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","sidebarLabel":"Plugin"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0]' is missing 'exposedModule'",
		},
		"route path under reserved api prefix": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/apiobjects","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0].path' \"/apiobjects\" is the root path or falls under a reserved prefix",
		},
		"route path under reserved auth prefix": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/authors","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0].path' \"/authors\" is the root path or falls under a reserved prefix",
		},
		"route path is the root path": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0].path' \"/\" is the root path or falls under a reserved prefix",
		},
		"route path collapses to the root path via dot segments": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin/..","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[0].path' \"/plugin/..\" is the root path or falls under a reserved prefix",
		},
		"duplicate route path in the same manifest": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
						{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
						{"path":"/plugin","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
					]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[1].path' \"/plugin\" duplicates earlier route \"/plugin\"",
		},
		"duplicate route path differing only by leading slash": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
						{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
						{"path":"plugin","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
					]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[1].path' \"plugin\" duplicates earlier route \"/plugin\"",
		},
		"duplicate route path differing only by doubled and trailing slashes": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[
						{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"},
						{"path":"//plugin/","sidebarLabel":"Plugin Again","exposedModule":"./OtherPage"}
					]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes[1].path' \"//plugin/\" duplicates earlier route \"/plugin\"",
		},
		"federation with no routes": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json":    `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[]}}`,
					"index.js":         "x",
					"remoteEntry.json": "x",
				},
			},
			wantErr: "'federation.routes' must not be empty",
		},
		"federation missing remoteEntry": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{}}`,
					"index.js":      "x",
				},
			},
			wantErr: "federation is missing 'remoteEntry'",
		},
		"federation remoteEntry same file as entry": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"shared.js","federation":{"remoteEntry":"shared.js","routes":[{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"shared.js":     "x",
				},
			},
			wantErr: "'federation.remoteEntry' must not be the same file as 'entry'",
		},
		"federation remoteEntry file not present": {
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm"},
				Data: map[string]string{
					"manifest.json": `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
					"index.js":      "x",
				},
			},
			wantErr: "remote entry file \"remoteEntry.json\" referenced by manifest's federation not found",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parsePluginConfigMap(tc.cm)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
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
						{"path": "/policies", "sidebarLabel": "Policy Management", "icon": "M0 0h16v16H0z", "exposedModule": "./PolicyManagementPage"},
						{"path": "/policies/audit", "sidebarLabel": "Policy Audit Log", "exposedModule": "./PolicyAuditPage"}
					]
				}
			}`,
			"index.js":         "x",
			"remoteEntry.json": "{}",
		},
	})

	assert.Equal(t, []apisv1.PluginManifest{{
		Name:    "policy-management",
		Version: "0.2.0",
		Entry:   "index.js",
		Federation: &apisv1.PluginFederation{
			RemoteEntry: "remoteEntry.json",
			Routes: []apisv1.PluginRoute{
				{Path: "/policies", SidebarLabel: "Policy Management", Icon: "M0 0h16v16H0z", ExposedModule: "./PolicyManagementPage"},
				{Path: "/policies/audit", SidebarLabel: "Policy Audit Log", ExposedModule: "./PolicyAuditPage"},
			},
		},
	}}, r.Index())
}

func federationConfigMap(cmName, pluginName, entry string, routes string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "antrea-ui"},
		Data: map[string]string{
			"manifest.json": `{
				"name": "` + pluginName + `",
				"version": "0.1.0",
				"entry": "` + entry + `",
				"federation": {"remoteEntry": "remoteEntry.json", "routes": ` + routes + `}
			}`,
			entry:              "x",
			"remoteEntry.json": "x",
		},
	}
}

// TestRegistryIndexDropsPluginWhenAllFederationRoutesCollide pins the
// cross-plugin path normalization: the two manifests spell the same route
// path differently ("/policies" vs "//policies/"), so the test would still
// pass if seenRoutePaths compared raw, un-normalized paths.
func TestRegistryIndexDropsPluginWhenAllFederationRoutesCollide(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap("b-configmap", "b-plugin", "index.js",
		`[{"path": "//policies/", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap("a-configmap", "a-plugin", "index.js",
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

	r.handleUpsert(federationConfigMap("a-configmap", "a-plugin", "a.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap("b-configmap", "b-plugin", "b.js",
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

// TestRegistryIndexAndFileStayConsistentWhenAllRoutesCollide reproduces the
// scenario where dropping a whole plugin for an all-routes collision, without
// claiming its name, let a later ConfigMap reusing that name get listed in
// Index() with an entry file File() would never actually resolve to (File()
// always resolves a name to its first-sorted ConfigMap, independently of
// Index()'s own bookkeeping).
func TestRegistryIndexAndFileStayConsistentWhenAllRoutesCollide(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(federationConfigMap("a-configmap", "aaa", "a.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	// b-configmap sorts before c-configmap, and claims the "dup" name first;
	// its one route collides with aaa's, so the whole manifest is dropped.
	r.handleUpsert(federationConfigMap("b-configmap", "dup", "b.js",
		`[{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]`))
	r.handleUpsert(federationConfigMap("c-configmap", "dup", "c.js",
		`[{"path": "/other", "sidebarLabel": "Other", "exposedModule": "./Page"}]`))

	manifests := r.Index()
	names := make([]string, len(manifests))
	for i, m := range manifests {
		names[i] = m.Name
	}
	assert.Equal(t, []string{"aaa"}, names, "dup must not be listed at all, from either ConfigMap")

	_, ok := r.File("dup", "c.js")
	assert.False(t, ok, "c-configmap's entry must never be served for a name Index() doesn't list")
}

func TestRegistryDuplicatePluginNameKeepsLowerConfigMapName(t *testing.T) {
	r := newTestRegistry(t)

	r.handleUpsert(configMap("b-configmap", "pod-counter", "2.0.0", "index.js", map[string]string{"index.js": "b"}))
	r.handleUpsert(configMap("a-configmap", "pod-counter", "1.0.0", "index.js", map[string]string{"index.js": "a"}))

	assert.Equal(t, []apisv1.PluginManifest{
		{Name: "pod-counter", Version: "1.0.0", Entry: "index.js"},
	}, r.Index())
	data, ok := r.File("pod-counter", "index.js")
	assert.True(t, ok)
	assert.Equal(t, "a", string(data))
}
