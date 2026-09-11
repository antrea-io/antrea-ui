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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

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
		// No public key is configured for this registry, so nothing requires a manifest.json.asc
		// - but a bundleSha256 that is present is still verified, and a mismatch is a rejection.
		"bundleSha256 not matching bundle.zip": func(t *testing.T) *corev1.ConfigMap {
			return withBundle(t, manifestWithDigest("plugin", "0.1.0", "index.js", bundleDigest([]byte("some other bundle"))), map[string]string{"index.js": "x"})
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

func TestRegistryRejectsNewConfigMapPluginPastLimit(t *testing.T) {
	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 1, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)

	r.handleUpsert(configMap(t, "first-cm", "first", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	r.handleUpsert(configMap(t, "second-cm", "second", "0.1.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.1.0", Entry: "index.js"}}, r.Index())

	// An update to the already-tracked plugin is never blocked by the limit.
	r.handleUpsert(configMap(t, "first-cm", "first", "0.2.0", "index.js", map[string]string{"index.js": "x"}))
	assert.Equal(t, []apisv1.PluginManifest{{Name: "first", Version: "0.2.0", Entry: "index.js"}}, r.Index())
}

func TestRegistryRejectsConfigMapBundlePastTheDecompressedSizeLimit(t *testing.T) {
	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 100})
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

// countingIndexer counts GetByKey calls so a test can assert exactly how many attempts a
// ConfigMap got out of processConfigMapQueueItem's retry budget - every attempt starts with
// that lookup (the queue carries keys, not objects), so the count is the attempt count.
type countingIndexer struct {
	cache.Indexer
	gets atomic.Int64
}

func (i *countingIndexer) GetByKey(key string) (interface{}, bool, error) {
	i.gets.Add(1)
	return i.Indexer.GetByKey(key)
}

// newTestIndexer builds an indexer keyed the way the informer's own is (namespace/name), so the
// keys enqueueConfigMap produces resolve back through it.
func newTestIndexer(t *testing.T, cms ...*corev1.ConfigMap) *countingIndexer {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, cm := range cms {
		require.NoError(t, indexer.Add(cm))
	}
	return &countingIndexer{Indexer: indexer}
}

func configMapKey(t *testing.T, cm *corev1.ConfigMap) string {
	t.Helper()
	key, err := cache.MetaNamespaceKeyFunc(cm)
	require.NoError(t, err)
	return key
}

// invalidConfigMap is a labeled ConfigMap that never parses as a plugin - handleUpsert returns
// false for it, which is what puts it on processConfigMapQueueItem's retry path.
func invalidConfigMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "antrea-ui", ResourceVersion: "1"},
		Data:       map[string]string{"manifest.json": "not json"},
	}
}

// TestConfigMapQueueRetriesFailureUntilItSucceeds exercises processConfigMapQueueItem's
// AddRateLimited path end to end, through runConfigMapWorker and the workqueue's own
// exponential backoff, under synctest's fake clock - the backoff is real time the worker waits
// on, so a real-clock version would either sleep or reach into the rate limiter.
//
// It also covers why processConfigMapQueueItem re-reads the key from the indexer instead of
// trusting the object the event carried: nothing re-enqueues the ConfigMap after it's fixed
// here, so the plugin can only load if the already-scheduled retry picks up the newer object.
func TestConfigMapQueueRetriesFailureUntilItSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
		t.Cleanup(r.Close)

		broken := invalidConfigMap("pod-counter-cm")
		indexer := newTestIndexer(t, broken)
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer func() {
			queue.ShutDown()
			synctest.Wait()
		}()
		go r.runConfigMapWorker(indexer, queue)

		key := configMapKey(t, broken)
		queue.Add(key)
		synctest.Wait()
		require.Empty(t, r.Index(), "an unparseable ConfigMap must not register a plugin")
		require.Equal(t, 1, queue.NumRequeues(key), "a failed load must be requeued, not dropped")
		require.EqualValues(t, 1, indexer.gets.Load())

		// Whoever owns the ConfigMap fixes it. The retry already scheduled above is the only
		// thing that will look at it again.
		fixed := configMap(t, "pod-counter-cm", "pod-counter", "0.1.0", "index.js", map[string]string{"index.js": "x"})
		require.NoError(t, indexer.Update(fixed))

		// Past the first backoff (5ms with the default rate limiter, well under a second).
		time.Sleep(time.Second)
		synctest.Wait()
		require.Len(t, r.Index(), 1)
		assert.Equal(t, "pod-counter", r.Index()[0].Name)
		assert.Equal(t, 0, queue.NumRequeues(key), "a successful load must Forget the key")
	})
}

// TestConfigMapQueueGivesUpAfterMaxRetries pins maxPluginLoadRetries: a ConfigMap that fails the
// same way every time is retried a bounded number of times and then abandoned until it changes
// again, rather than being requeued for the life of the process.
func TestConfigMapQueueGivesUpAfterMaxRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 0, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
		t.Cleanup(r.Close)

		broken := invalidConfigMap("pod-counter-cm")
		indexer := newTestIndexer(t, broken)
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
		defer func() {
			queue.ShutDown()
			synctest.Wait()
		}()
		go r.runConfigMapWorker(indexer, queue)

		key := configMapKey(t, broken)
		queue.Add(key)

		// Long enough for every backoff in the budget to elapse under the fake clock, and then
		// for a further stretch during which nothing more may happen.
		time.Sleep(time.Hour)
		synctest.Wait()

		// The initial attempt plus maxPluginLoadRetries retries: the give-up branch triggers on
		// the attempt that finds NumRequeues already at maxPluginLoadRetries.
		assert.EqualValues(t, maxPluginLoadRetries+1, indexer.gets.Load(), "retry budget must be bounded by maxPluginLoadRetries")
		assert.Equal(t, 0, queue.NumRequeues(key), "giving up must Forget the key, so a later event starts from a clean budget")
		assert.Empty(t, r.Index())
	})
}

// TestConfigMapQueueCapRejectionSkipsRetryBudget covers the two things that make a cap rejection
// different from a load failure: it costs no extraction (the check runs before
// extractedPluginDir/parsePluginConfigMap), and it doesn't spend the retry budget on something
// no retry can fix - handleUpsert returns true, so processConfigMapQueueItem Forgets the key
// instead of requeuing it (the contrasting case, a load failure that does spend the budget, is
// TestConfigMapQueueRetriesFailureUntilItSucceeds). No timing involved, so this drives
// processConfigMapQueueItem directly.
func TestConfigMapQueueCapRejectionSkipsRetryBudget(t *testing.T) {
	r := NewRegistry(Options{Logger: testr.New(t), Clientset: nil, Namespace: "antrea-ui", LabelSelector: "ui.antrea.io/plugin=true", MaxConfigMapPlugins: 1, MaxDirectoryPlugins: 0, MaxBundleBytes: 0})
	t.Cleanup(r.Close)

	first := configMap(t, "first-cm", "first", "0.1.0", "index.js", map[string]string{"index.js": "x"})
	pastCap := configMap(t, "second-cm", "second", "0.1.0", "index.js", map[string]string{"index.js": "x"})
	indexer := newTestIndexer(t, first, pastCap)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	t.Cleanup(queue.ShutDown)

	r.processConfigMapQueueItem(indexer, configMapKey(t, first), queue)
	require.Len(t, r.Index(), 1)

	capKey := configMapKey(t, pastCap)
	r.processConfigMapQueueItem(indexer, capKey, queue)
	assert.Len(t, r.Index(), 1, "a plugin past the cap must not be served")
	assert.Equal(t, 0, queue.NumRequeues(capKey), "a cap rejection must not spend the retry budget")

	// The cap check runs before any extraction, so the rejected plugin never gets a directory
	// under the scratch cache - not even one created and then cleaned up, which is all the
	// index assertion above would catch on its own.
	require.NotEmpty(t, r.cacheRoot, "the first plugin's extraction must have created the cache directory")
	assert.NoDirExists(t, filepath.Join(r.cacheRoot, configMapSourceName, "second-cm"))
}
