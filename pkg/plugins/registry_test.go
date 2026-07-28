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
	cases := map[string]*corev1.ConfigMap{
		"missing manifest.json": {
			ObjectMeta: metav1.ObjectMeta{Name: "cm"},
			Data:       map[string]string{"index.js": "x"},
		},
		"malformed manifest.json": {
			ObjectMeta: metav1.ObjectMeta{Name: "cm"},
			Data:       map[string]string{"manifest.json": "not json"},
		},
		"missing name":           configMap("cm", "", "0.1.0", "index.js", map[string]string{"index.js": "x"}),
		"missing entry":          configMap("cm", "plugin", "0.1.0", "", map[string]string{"index.js": "x"}),
		"entry file not present": configMap("cm", "plugin", "0.1.0", "index.js", nil),
	}
	for name, cm := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestRegistry(t)
			r.handleUpsert(cm)
			assert.Empty(t, r.Index())
		})
	}
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
