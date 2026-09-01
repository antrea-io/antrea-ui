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
			wantErr: "'federation.routes[0].path' \"/apiobjects\" falls under a reserved prefix",
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
			wantErr: "'federation.routes[0].path' \"/authors\" falls under a reserved prefix",
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

func TestRegistryIndexDropsFederationRoutePathCollidingWithAnotherPlugin(t *testing.T) {
	r := newTestRegistry(t)

	manifest := func(cmName string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "antrea-ui"},
			Data: map[string]string{
				"manifest.json": `{
					"name": "` + cmName + `-plugin",
					"version": "0.1.0",
					"entry": "index.js",
					"federation": {
						"remoteEntry": "remoteEntry.json",
						"routes": [{"path": "/policies", "sidebarLabel": "Policies", "exposedModule": "./Page"}]
					}
				}`,
				"index.js":         "x",
				"remoteEntry.json": "x",
			},
		}
	}

	r.handleUpsert(manifest("b-configmap"))
	r.handleUpsert(manifest("a-configmap"))

	manifests := r.Index()
	require.Len(t, manifests, 1)
	assert.Equal(t, "a-configmap-plugin", manifests[0].Name)
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
