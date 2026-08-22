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
	return NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
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
// nil bundleFiles omits binaryData entirely (for cases exercising a missing bundle.zip).
func configMap(t *testing.T, name, pluginName, version, entry string, bundleFiles map[string]string) *corev1.ConfigMap {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui"},
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
		"route path under reserved api/ prefix": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t,
				`{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{"remoteEntry":"remoteEntry.json","routes":[{"path":"/api/v1/plugin","sidebarLabel":"Plugin","exposedModule":"./Page"}]}}`,
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
		"federation missing remoteEntry": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, `{"name":"plugin","version":"0.1.0","entry":"index.js","federation":{}}`, map[string]string{"index.js": "x"})
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

func TestRegistryIndexIncludesRoutesAndFederation(t *testing.T) {
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
				{Path: "/policies", SidebarLabel: "Policy Management", Icon: "M0 0h16v16H0z", ExposedModule: "./PolicyManagementPage"},
				{Path: "/policies/audit", SidebarLabel: "Policy Audit Log", ExposedModule: "./PolicyAuditPage"},
			},
		},
	}}, r.Index())
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

	r.handleUpsert(configMap(t, "first-cm", "first", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	r.handleUpsert(configMap(t, "second-cm", "second", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.1.0", Entry: "index.js"}}, r.Index())

	// An update to the already-tracked plugin is never blocked by the limit.
	r.handleUpsert(configMap(t, "first-cm", "first", "0.2.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.2.0", Entry: "index.js"}}, r.Index())
}

func TestRegistryRejectsConfigMapBundlePastTheDecompressedSizeLimit(t *testing.T) {
	r := NewRegistry(testr.New(t), nil, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 100)

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
