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

package supportbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

const (
	testAPIServerHost = "apiserver.test"
	testAdminUser     = "system:serviceaccount:ns:antrea-ui-admin"
	testRequester     = "alice"
)

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type recordedRequest struct {
	method string
	host   string
	path   string
	header http.Header
	body   string
	at     time.Time
}

// fakeSource implements the source protocol in memory, with knobs for the ways a source can
// misbehave.
type fakeSource struct {
	mutex sync.Mutex
	// pollsBeforeCollected is how many status calls answer Collecting first. Negative means
	// the bundle is never collected.
	pollsBeforeCollected int
	// remoteID is the ID returned on create.
	remoteID string
	// createResponse, when set, replaces the whole create response.
	createResponse func(w http.ResponseWriter)
	// statusResponse, when set, replaces the whole status response.
	statusResponse func(w http.ResponseWriter, poll int)
	retryAfter     string
	payload        []byte
	// gate, when set, is called before each request is served, without holding mutex, so that it
	// can block one request without holding up the others. An error fails the request in the
	// transport, as a canceled request would.
	gate func(r *http.Request) error

	polls    int
	requests []recordedRequest
	// outstanding counts the bundles created and not deleted yet, and maxOutstanding is its peak.
	outstanding    int
	maxOutstanding int
}

func newFakeSource() *fakeSource {
	return &fakeSource{remoteID: "remote-1", retryAfter: "1", payload: []byte("source tarball")}
}

func (f *fakeSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/supportbundles"):
		if f.createResponse != nil {
			f.createResponse(w)
			return
		}
		f.outstanding++
		f.maxOutstanding = max(f.maxOutstanding, f.outstanding)
		w.Header().Set("Retry-After", f.retryAfter)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(sourceBundle(f.remoteID, apisv1.SupportBundleStatusCollecting, ""))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/supportbundles/"+f.remoteID):
		f.polls++
		if f.statusResponse != nil {
			f.statusResponse(w, f.polls)
			return
		}
		status := apisv1.SupportBundleStatusCollected
		if f.pollsBeforeCollected < 0 || f.polls <= f.pollsBeforeCollected {
			status = apisv1.SupportBundleStatusCollecting
			w.Header().Set("Retry-After", f.retryAfter)
		}
		_ = json.NewEncoder(w).Encode(sourceBundle(f.remoteID, status, ""))
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/supportbundles/"+f.remoteID+"/download"):
		_, _ = w.Write(f.payload)
	case r.Method == http.MethodDelete && strings.HasSuffix(path, "/supportbundles/"+f.remoteID):
		f.outstanding--
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// sourceBundle is a source's description of its bundle, in the source protocol.
func sourceBundle(name string, phase apisv1.SupportBundleStatus, message string) apisv1.SourceBundle {
	return apisv1.SourceBundle{
		Metadata: apisv1.SourceBundleMetadata{Name: name},
		Status:   apisv1.SourceBundleStatus{Phase: phase, Message: message},
	}
}

func (f *fakeSource) recorded() []recordedRequest {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeSource) countRequests(method, suffix string) int {
	n := 0
	for _, r := range f.recorded() {
		if r.method == method && strings.HasSuffix(r.path, suffix) {
			n++
		}
	}
	return n
}

// fakeAPIServer routes requests to fake sources by path prefix, in memory, and records every
// request it is asked to send, wherever it is addressed.
type fakeAPIServer struct {
	mutex    sync.Mutex
	sources  map[string]*fakeSource
	requests []recordedRequest
}

// transport stands in for the transport authenticating as antrea-ui's own ServiceAccount.
func (f *fakeAPIServer) transport() http.RoundTripper {
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		r := recordedRequest{method: req.Method, host: req.URL.Host, path: req.URL.Path, header: req.Header.Clone(), at: time.Now()}
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			r.body = string(body)
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		f.mutex.Lock()
		f.requests = append(f.requests, r)
		f.mutex.Unlock()
		if req.URL.Host != testAPIServerHost {
			return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
		}
		for prefix, source := range f.sources {
			if strings.HasPrefix(req.URL.Path, prefix+"/") {
				source.mutex.Lock()
				source.requests = append(source.requests, r)
				gate := source.gate
				source.mutex.Unlock()
				if gate != nil {
					if err := gate(req); err != nil {
						return nil, err
					}
				}
				rec := httptest.NewRecorder()
				source.ServeHTTP(rec, req)
				return rec.Result(), nil
			}
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
}

func (f *fakeAPIServer) recorded() []recordedRequest {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

type testManager struct {
	*manager
	apiServer    *fakeAPIServer
	logDirectory string
	pluginsMutex sync.Mutex
	plugins      []apisv1.PluginManifest
	// stop stops the manager, and waits for every collection to be drained.
	stop func()
}

func testConfig(t *testing.T) *serverconfig.SupportBundleConfig {
	return &serverconfig.SupportBundleConfig{
		Enabled:           true,
		Directory:         t.TempDir(),
		MaxBundles:        5,
		MaxConcurrent:     2,
		MaxTotalBytes:     100 * 1024 * 1024,
		MaxSourceBytes:    1024,
		TTL:               6 * time.Hour,
		CollectionTimeout: 10 * time.Minute,
	}
}

// newTestManager builds a manager and starts it. It must be called inside a synctest bubble: the
// manager is stopped, and every collection drained, when the test ends.
func newTestManager(t *testing.T, config *serverconfig.SupportBundleConfig, sources map[string]*fakeSource, plugins []apisv1.PluginManifest) *testManager {
	logger := testr.New(t)
	logDirectory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(logDirectory, LogFileName), []byte("current log\n"), 0o600))

	apiServer := &fakeAPIServer{sources: sources}
	tm := &testManager{apiServer: apiServer, logDirectory: logDirectory, plugins: plugins}
	serverConfig := &serverconfig.Config{}
	serverConfig.Auth.OIDC.ClientSecret = "very-secret"
	m, err := NewManager(Options{
		Logger: logger,
		Config: config,
		Backend: BackendOptions{
			LogDirectory: logDirectory,
			Config:       serverConfig,
			Plugins: func() []apisv1.PluginManifest {
				tm.pluginsMutex.Lock()
				defer tm.pluginsMutex.Unlock()
				return tm.plugins
			},
		},
		APIServerURL:       &url.URL{Scheme: "https", Host: testAPIServerHost},
		APIServerTransport: apiServer.transport(),
		AdminUserName:      testAdminUser,
	})
	require.NoError(t, err)
	tm.manager = m.(*manager)

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tm.Run(stopCh)
	}()
	tm.stop = sync.OnceFunc(func() {
		close(stopCh)
		<-done
		synctest.Wait()
	})
	t.Cleanup(tm.stop)
	return tm
}

func apiServerExtraSource(name, group string) serverconfig.SupportBundleExtraSource {
	return serverconfig.SupportBundleExtraSource{Name: name, APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/" + group + "/v1"}}
}

// setPlugins replaces the plugin manifests the manager sees.
func (tm *testManager) setPlugins(plugins []apisv1.PluginManifest) {
	tm.pluginsMutex.Lock()
	defer tm.pluginsMutex.Unlock()
	tm.plugins = plugins
}

// waitDone advances time until the bundle is no longer collecting, and returns it.
func (tm *testManager) waitDone(t *testing.T, id string) *apisv1.SupportBundle {
	t.Helper()
	for range 1000 {
		b, err := tm.Get(id)
		require.NoError(t, err)
		if b.Status != apisv1.SupportBundleStatusCollecting {
			return b
		}
		time.Sleep(time.Second)
	}
	require.FailNow(t, "bundle still collecting")
	return nil
}

func readTarball(t *testing.T, r io.Reader) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(r)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[h.Name] = string(data)
	}
	return files
}

func sourceStatus(b *apisv1.SupportBundle, name string) apisv1.SupportBundleSourceStatus {
	for _, s := range b.Sources {
		if s.Name == name {
			return s
		}
	}
	return apisv1.SupportBundleSourceStatus{}
}

func TestCollectBundle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo, plugin := newFakeSource(), newFakeSource()
		foo.pollsBeforeCollected = 2
		plugin.payload = []byte("plugin tarball")
		tm := newTestManager(t, config, map[string]*fakeSource{
			"/apis/foo.example.com/v1":    foo,
			"/apis/plugin.example.com/v1": plugin,
		}, []apisv1.PluginManifest{
			{Name: "no-source"},
			{Name: "with-source", SupportBundle: &apisv1.SupportBundleSourceSpec{APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/plugin.example.com/v1"}}},
			{Name: "future-source", SupportBundle: &apisv1.SupportBundleSourceSpec{}},
		})

		created, err := tm.Create(testRequester, &apisv1.SupportBundleRequest{Since: "1h"})
		require.NoError(t, err)
		assert.Equal(t, apisv1.SupportBundleStatusCollecting, created.Status)
		assert.Equal(t, testRequester, created.CreatedBy)
		assert.Equal(t, created.CreatedAt.Add(6*time.Hour), created.ExpiresAt)

		b := tm.waitDone(t, created.ID)
		require.Equal(t, apisv1.SupportBundleStatusCollected, b.Status, b.Error)
		assert.Equal(t, []apisv1.SupportBundleSourceStatus{
			{Name: "foo", Kind: "extra", Status: apisv1.SupportBundleStatusCollected, Size: int64(len("source tarball"))},
			{Name: "with-source", Kind: "plugin", Status: apisv1.SupportBundleStatusCollected, Size: int64(len("plugin tarball"))},
			{Name: "future-source", Kind: "plugin", Status: apisv1.SupportBundleStatusFailed, Error: "unsupported source declaration"},
		}, b.Sources)
		assert.Equal(t, []apisv1.SupportBundle{*b}, tm.List())

		f, opened, err := tm.Open(created.ID)
		require.NoError(t, err)
		defer f.Close()
		assert.Equal(t, b, opened)
		files := readTarball(t, f)
		info, err := os.Stat(filepath.Join(config.Directory, created.ID, bundleFileName))
		require.NoError(t, err)
		assert.Equal(t, info.Size(), b.Size)

		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		assert.ElementsMatch(t, []string{
			"manifest.json",
			"antrea-ui-backend/version.txt",
			"antrea-ui-backend/config.yaml",
			"antrea-ui-backend/plugins.json",
			"antrea-ui-backend/goroutines.txt",
			"antrea-ui-backend/logs/antrea-ui.log",
			"extra/foo.tar.gz",
			"plugins/with-source.tar.gz",
		}, names)
		assert.Equal(t, "source tarball", files["extra/foo.tar.gz"])
		assert.Equal(t, "plugin tarball", files["plugins/with-source.tar.gz"])
		assert.Equal(t, "current log\n", files["antrea-ui-backend/logs/antrea-ui.log"])
		assert.Contains(t, files["antrea-ui-backend/config.yaml"], "<redacted>")
		assert.NotContains(t, files["antrea-ui-backend/config.yaml"], "very-secret")
		assert.Contains(t, files["antrea-ui-backend/plugins.json"], "future-source")

		var manifest bundleManifest
		require.NoError(t, json.Unmarshal([]byte(files["manifest.json"]), &manifest))
		assert.Equal(t, created.ID, manifest.ID)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, manifest.Status)
		assert.Equal(t, b.Sources, manifest.Sources)
		assert.NotEmpty(t, manifest.AntreaUIVersion)

		// The work directory is gone, and only the tarball is charged to the budget.
		_, err = os.Stat(filepath.Join(config.Directory, created.ID, workDirName))
		assert.True(t, os.IsNotExist(err))
		assert.Equal(t, b.Size, tm.budget.used.Load())

		for _, source := range []*fakeSource{foo, plugin} {
			assert.Equal(t, 1, source.countRequests(http.MethodPost, "/supportbundles"))
			assert.Equal(t, 1, source.countRequests(http.MethodGet, "/download"))
			assert.Equal(t, 1, source.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
			for _, r := range source.recorded() {
				if r.method == http.MethodPost {
					assert.JSONEq(t, `{"metadata":{"generateName":"antrea-ui-"},"spec":{"since":"1h"}}`, r.body)
				}
			}
		}
		assert.Equal(t, 3, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
		// Every request, the final DELETE included, is made as antrea-ui-admin, telling the
		// apiserver who requested which bundle.
		requests := tm.apiServer.recorded()
		require.Len(t, requests, len(foo.recorded())+len(plugin.recorded()))
		for _, r := range requests {
			assertServiceIdentity(t, r, created.ID)
		}
	})
}

// assertServiceIdentity checks that r, sent through the apiserver, impersonates antrea-ui-admin
// with the audit extras for bundleID, and carries no credential other than the transport's.
func assertServiceIdentity(t *testing.T, r recordedRequest, bundleID string) {
	t.Helper()
	assert.Equal(t, testAdminUser, r.header.Get("Impersonate-User"), r.method+" "+r.path)
	assert.Equal(t, []string{testRequester}, r.header.Values("Impersonate-Extra-supportbundle.ui.antrea.io%2Frequested-by"), r.method+" "+r.path)
	assert.Equal(t, []string{bundleID}, r.header.Values("Impersonate-Extra-supportbundle.ui.antrea.io%2Fbundle-id"), r.method+" "+r.path)
	assert.Empty(t, r.header.Get("Authorization"), r.method+" "+r.path)
}

func TestCollectBundleSinceFiltersRotatedLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := newTestManager(t, testConfig(t), nil, nil)
		now := time.Now()
		for name, age := range map[string]time.Duration{
			LogFileName:                            3 * time.Hour,
			"antrea-ui-2000-01-01T00-00-00.log.gz": 3 * time.Hour,
			"antrea-ui-2000-01-01T02-50-00.log.gz": 10 * time.Minute,
		} {
			path := filepath.Join(tm.logDirectory, name)
			require.NoError(t, os.WriteFile(path, []byte(name), 0o600))
			require.NoError(t, os.Chtimes(path, now.Add(-age), now.Add(-age)))
		}

		created, err := tm.Create(testRequester, &apisv1.SupportBundleRequest{Since: "1h"})
		require.NoError(t, err)
		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, created.ID).Status)
		f, _, err := tm.Open(created.ID)
		require.NoError(t, err)
		defer f.Close()
		files := readTarball(t, f)
		// The current file is always collected, whatever its age.
		assert.Contains(t, files, "antrea-ui-backend/logs/"+LogFileName)
		assert.Contains(t, files, "antrea-ui-backend/logs/antrea-ui-2000-01-01T02-50-00.log.gz")
		assert.NotContains(t, files, "antrea-ui-backend/logs/antrea-ui-2000-01-01T00-00-00.log.gz")
	})
}

func TestCollectBundleOnlyCollectsLogFiles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := newTestManager(t, testConfig(t), nil, nil)
		for _, name := range []string{LogFileName, "antrea-ui-2000-01-01T00-00-00.000.log", "antrea-ui-2000-01-02T00-00-00.000.log.gz", "other.log", "secret.txt", "antrea-ui-notes.txt"} {
			require.NoError(t, os.WriteFile(filepath.Join(tm.logDirectory, name), []byte(name), 0o600))
		}

		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, created.ID).Status)
		f, _, err := tm.Open(created.ID)
		require.NoError(t, err)
		defer f.Close()
		var logs []string
		for name := range readTarball(t, f) {
			if dir, file := filepath.Split(name); dir == "antrea-ui-backend/logs/" && file != "" {
				logs = append(logs, file)
			}
		}
		assert.ElementsMatch(t, []string{LogFileName, "antrea-ui-2000-01-01T00-00-00.000.log", "antrea-ui-2000-01-02T00-00-00.000.log.gz"}, logs)
	})
}

func TestCollectBundleWithoutFileLogging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := newTestManager(t, testConfig(t), nil, nil)
		tm.backend.LogDirectory = ""
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, created.ID).Status)
		f, _, err := tm.Open(created.ID)
		require.NoError(t, err)
		defer f.Close()
		files := readTarball(t, f)
		assert.Contains(t, files["antrea-ui-backend/logs/README.txt"], "File logging is disabled")
		assert.NotContains(t, files, "antrea-ui-backend/logs/"+LogFileName)
	})
}

func TestCollectBundleSkipsLogBeingCompressed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := newTestManager(t, testConfig(t), nil, nil)
		for _, name := range []string{LogFileName, "antrea-ui-2000-01-01T00-00-00.log", "antrea-ui-2000-01-01T00-00-00.log.gz"} {
			require.NoError(t, os.WriteFile(filepath.Join(tm.logDirectory, name), []byte(name), 0o600))
		}

		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, created.ID).Status)
		f, _, err := tm.Open(created.ID)
		require.NoError(t, err)
		defer f.Close()
		files := readTarball(t, f)
		assert.Contains(t, files, "antrea-ui-backend/logs/antrea-ui-2000-01-01T00-00-00.log")
		assert.NotContains(t, files, "antrea-ui-backend/logs/antrea-ui-2000-01-01T00-00-00.log.gz")
	})
}

func TestPluginWithUnsafeNameIsNotCollected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		evil := newFakeSource()
		spec := &apisv1.SupportBundleSourceSpec{APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/evil.example.com/v1"}}
		tm := newTestManager(t, testConfig(t), map[string]*fakeSource{"/apis/evil.example.com/v1": evil}, []apisv1.PluginManifest{
			{Name: "../evil", SupportBundle: spec},
			{Name: "a/b", SupportBundle: spec},
		})
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		b := tm.waitDone(t, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		for _, name := range []string{"../evil", "a/b"} {
			assert.Equal(t, apisv1.SupportBundleSourceStatus{Name: name, Kind: apisv1.SupportBundleSourceKindPlugin, Status: apisv1.SupportBundleStatusFailed, Error: "plugin name cannot be used as a file name"}, sourceStatus(b, name))
		}
		assert.Empty(t, tm.apiServer.recorded())
	})
}

func TestCreateRejectsInvalidSince(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tm := newTestManager(t, testConfig(t), nil, nil)
		for _, since := range []string{"yesterday", "-1h", "0s"} {
			_, err := tm.Create(testRequester, &apisv1.SupportBundleRequest{Since: since})
			assert.ErrorIs(t, err, ErrInvalidRequest, since)
		}
		assert.Empty(t, tm.List())
	})
}

func TestExpiredBundlesAreGarbageCollected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.TTL = time.Hour
		tm := newTestManager(t, config, nil, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, created.ID).Status)

		time.Sleep(30 * time.Minute)
		_, err = tm.Get(created.ID)
		require.NoError(t, err)

		synctest.Sleep(30*time.Minute + gcInterval)
		_, err = tm.Get(created.ID)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = os.Stat(filepath.Join(config.Directory, created.ID))
		assert.True(t, os.IsNotExist(err))
		assert.Zero(t, tm.budget.used.Load())
	})
}

func TestBundleExpiringWhileCollectingIsRemoved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.TTL = time.Hour
		config.CollectionTimeout = 2 * time.Hour
		foo := newFakeSource()
		foo.pollsBeforeCollected = -1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)

		synctest.Sleep(time.Hour + gcInterval)
		_, err = tm.Get(created.ID)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = os.Stat(filepath.Join(config.Directory, created.ID))
		assert.True(t, os.IsNotExist(err))
		assert.Zero(t, tm.budget.used.Load())
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
	})
}

func TestCreateEnforcesBundleCaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxBundles = 2
		config.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		foo.pollsBeforeCollected = 5
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)

		first, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached, "maxConcurrent")

		require.Equal(t, apisv1.SupportBundleStatusCollected, tm.waitDone(t, first.ID).Status)
		_, err = tm.Create(testRequester, nil)
		require.NoError(t, err)
		// Collected or not, every retained bundle counts against maxBundles.
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached, "maxBundles")

		require.NoError(t, tm.Delete(first.ID))
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached, "maxConcurrent, again")
	})
}

func TestCreateRefusesWhenStorageBudgetIsLow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxTotalBytes = 100 * 1024
		config.MaxSourceBytes = 10 * 1024
		tm := newTestManager(t, config, nil, nil)
		// Stands in for the bytes other bundles hold on disk.
		require.True(t, tm.budget.reserve(config.MaxTotalBytes-2*config.MaxSourceBytes))
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err, "exactly one source of the maximum size still fits")
		tm.waitDone(t, created.ID)
		require.NoError(t, tm.Delete(created.ID))
		// Delete releases the bundle's bytes once its files are removed, in the background.
		synctest.Wait()
		require.Equal(t, config.MaxTotalBytes-2*config.MaxSourceBytes, tm.budget.used.Load())

		require.True(t, tm.budget.reserve(1))
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached)
		assert.ErrorContains(t, err, "maxTotalBytes")
	})
}

func TestDeletedBundleCountsAgainstMaxConcurrentUntilItStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		// The create call to foo does not return until released, whatever its context.
		release := make(chan struct{})
		foo.createResponse = func(w http.ResponseWriter) {
			<-release
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(sourceBundle(foo.remoteID, apisv1.SupportBundleStatusCollecting, ""))
		}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)

		first, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Wait()
		require.NoError(t, tm.Delete(first.ID))
		synctest.Wait()
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached, "the deleted bundle's collection is still running")

		close(release)
		synctest.Wait()
		_, err = tm.Create(testRequester, nil)
		assert.NoError(t, err)
	})
}

func TestBundleFailsWhenStorageBudgetIsExceeded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxTotalBytes = 2048
		config.MaxSourceBytes = 1024
		tm := newTestManager(t, config, nil, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		b := tm.waitDone(t, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		assert.Contains(t, b.Error, "storage budget")
		// A failed bundle keeps only its metadata, and gives its bytes back.
		entries, err := os.ReadDir(filepath.Join(config.Directory, created.ID))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, metadataFileName, entries[0].Name())
		assert.Equal(t, apisv1.SupportBundleStatusFailed, readMetadata(t, config.Directory, created.ID).Status)
		assert.Zero(t, tm.budget.used.Load())
		_, _, err = tm.Open(created.ID)
		assert.ErrorIs(t, err, ErrNotCollected)
	})
}

func TestSourceExceedingStorageBudgetFailsBundle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		config := testConfig(t)
		// bar never finishes: it is given up on as soon as foo fails the bundle.
		bar := newFakeSource()
		bar.pollsBeforeCollected = -1
		foo := newFakeSource()
		foo.payload = bytes.Repeat([]byte("x"), int(config.MaxSourceBytes))
		// Holds the source back until the backend's own files are written.
		release := make(chan struct{})
		foo.createResponse = func(w http.ResponseWriter) {
			<-release
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(sourceBundle(foo.remoteID, apisv1.SupportBundleStatusCollecting, ""))
		}
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com"), apiServerExtraSource("bar", "bar.example.com")}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo, "/apis/bar.example.com/v1": bar}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Wait()
		// Stands in for other bundles: what is left fits half of the source.
		reserved := tm.budget.max - tm.budget.used.Load() - config.MaxSourceBytes/2
		require.True(t, tm.budget.reserve(reserved))
		close(release)

		b := tm.waitDone(t, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		assert.Contains(t, b.Error, "storage budget")
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "storage budget")
		status = sourceStatus(b, "bar")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "storage budget")
		assert.Less(t, time.Since(start), time.Minute, "the bundle fails without waiting for bar")
		entries, err := os.ReadDir(filepath.Join(config.Directory, created.ID))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, metadataFileName, entries[0].Name())
		assert.Equal(t, reserved, tm.budget.used.Load())
		for _, source := range []*fakeSource{foo, bar} {
			assert.Equal(t, 1, source.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
		}
	})
}

func TestBundleFailureFailsPendingSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxTotalBytes = 2048
		config.MaxSourceBytes = 1024
		// The backend's own diagnostics exceed the budget, so foo is never collected.
		tm, b := collectFromFoo(t, config, newFakeSource())
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		assert.Contains(t, b.Error, "storage budget")
		assert.Equal(t, apisv1.SupportBundleSourceStatus{Name: "foo", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusFailed, Error: b.Error}, sourceStatus(b, "foo"))
		assert.Equal(t, b, readMetadata(t, config.Directory, b.ID), "the source status is persisted")
		assert.Empty(t, tm.apiServer.recorded())
	})
}

// collectFromFoo collects a bundle with a single extra source, foo, and returns the bundle.
func collectFromFoo(t *testing.T, config *serverconfig.SupportBundleConfig, foo *fakeSource) (*testManager, *apisv1.SupportBundle) {
	config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
	tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
	created, err := tm.Create(testRequester, nil)
	require.NoError(t, err)
	return tm, tm.waitDone(t, created.ID)
}

func TestSourceRedirectIsNotFollowed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.createResponse = func(w http.ResponseWriter) {
			w.Header().Set("Location", "https://evil.example.com/steal")
			w.WriteHeader(http.StatusFound)
		}
		tm, b := collectFromFoo(t, testConfig(t), foo)
		// The bundle is still delivered: one failed source does not fail it.
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "HTTP 302")
		requests := tm.apiServer.recorded()
		require.Len(t, requests, 1)
		assert.Equal(t, testAPIServerHost, requests[0].host)
	})
}

func TestSourceWithInvalidRemoteIDFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, id := range []string{"../x", "..", "a/b", ""} {
			foo := newFakeSource()
			foo.remoteID = id
			tm, b := collectFromFoo(t, testConfig(t), foo)
			status := sourceStatus(b, "foo")
			assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status, id)
			assert.Contains(t, status.Error, "invalid bundle name", id)
			// Nothing is ever sent to a URL built from the ID.
			assert.Len(t, tm.apiServer.recorded(), 1, id)
		}
	})
}

func TestOversizedSourceFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.MaxSourceBytes = 10
		foo := newFakeSource()
		foo.payload = []byte("01234567890")
		tm, b := collectFromFoo(t, config, foo)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "maxSourceBytes")
		f, _, err := tm.Open(b.ID)
		require.NoError(t, err)
		defer f.Close()
		assert.NotContains(t, readTarball(t, f), "extra/foo.tar.gz")
		assert.Equal(t, b.Size, tm.budget.used.Load())
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
	})
}

// The status response is written out, as a Kubernetes apiserver would serve it, so that the test
// also pins the protocol's JSON field names.
func TestSourceFailureIsReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			_, _ = w.Write([]byte(`{
				"apiVersion": "foo.example.com/v1",
				"kind": "SupportBundle",
				"metadata": {"name": "remote-1", "creationTimestamp": "2026-10-24T00:00:00Z"},
				"status": {"phase": "Failed", "message": "disk full"}
			}`))
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "disk full")
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
	})
}

func TestSourceUnknownStatusFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			_ = json.NewEncoder(w).Encode(sourceBundle("remote-1", "collected", ""))
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, `unknown bundle phase "collected"`)
		assert.Equal(t, 1, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"), "no polling after an unknown phase")
	})
}

func TestCollectionTimeoutFailsOnlyPendingSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.CollectionTimeout = time.Minute
		foo := newFakeSource()
		foo.pollsBeforeCollected = -1
		tm, b := collectFromFoo(t, config, foo)
		// The bundle is still delivered, without the source that timed out.
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "deadline exceeded")
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
		assert.Equal(t, b.Size, tm.budget.used.Load())
	})
}

func TestSourceEmptyStatusIsCollecting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, poll int) {
			switch poll {
			case 1:
				_, _ = w.Write([]byte(`{"metadata": {"name": "remote-1"}}`))
			case 2:
				_, _ = w.Write([]byte(`{"metadata": {"name": "remote-1"}, "status": {}}`))
			default:
				_ = json.NewEncoder(w).Encode(sourceBundle("remote-1", apisv1.SupportBundleStatusCollected, ""))
			}
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		assert.Equal(t, apisv1.SupportBundleSourceStatus{Name: "foo", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusCollected, Size: int64(len("source tarball"))}, sourceStatus(b, "foo"))
		assert.Equal(t, 3, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
	})
}

func TestSourceFailureMessageIsTruncated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			_ = json.NewEncoder(w).Encode(sourceBundle("remote-1", apisv1.SupportBundleStatusFailed, strings.Repeat("x", 2*maxErrorBodyBytes)))
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Equal(t, "source failed to collect its bundle: "+strings.Repeat("x", maxErrorBodyBytes), status.Error)
	})
}

// A create or status response is read up to a cap, whatever its size.
func TestSourceJSONResponseIsCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.createResponse = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(sourceBundle(foo.remoteID, apisv1.SupportBundleStatusCollecting, strings.Repeat("x", maxJSONBodyBytes)))
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "invalid response from source")
	})
}

// countingReader serves an endless body, and counts how much of it was read.
type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.n += len(p)
	return len(p), nil
}

func TestStatusErrorReadsAtMostMaxErrorBodyBytes(t *testing.T) {
	body := &countingReader{}
	err := statusError(&http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(body)})
	assert.Equal(t, "source returned HTTP 500: "+strings.Repeat("x", maxErrorBodyBytes), err.Error())
	assert.LessOrEqual(t, body.n, maxErrorBodyBytes)
}

func TestSourceStatusIsRetriedOnTransientErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, poll int) {
			switch poll {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
			case 2:
				w.WriteHeader(http.StatusTooManyRequests)
			default:
				_ = json.NewEncoder(w).Encode(sourceBundle("remote-1", apisv1.SupportBundleStatusCollected, ""))
			}
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		assert.Equal(t, apisv1.SupportBundleSourceStatus{Name: "foo", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusCollected, Size: int64(len("source tarball"))}, sourceStatus(b, "foo"))
		assert.Equal(t, 3, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
	})
}

func TestSourceStatusTransientErrorsGiveUpAtTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.CollectionTimeout = time.Minute
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, b := collectFromFoo(t, config, foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "gave up waiting for the bundle")
		assert.Contains(t, status.Error, "HTTP 503")
		assert.Greater(t, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"), 1)
	})
}

func TestSourceStatusOtherErrorsFailTheSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusNotFound)
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Contains(t, status.Error, "failed to get the bundle status: source returned HTTP 404")
		assert.Equal(t, 1, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
	})
}

func TestSourcesAreCollectedAtMostMaxParallelSourcesAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		sources := map[string]*fakeSource{}
		release := make(chan struct{})
		var inFlight atomic.Int32
		for i := range maxParallelSources + 2 {
			name := fmt.Sprintf("s%d", i)
			config.ExtraSources = append(config.ExtraSources, apiServerExtraSource(name, name+".example.com"))
			s := newFakeSource()
			s.createResponse = func(w http.ResponseWriter) {
				inFlight.Add(1)
				<-release
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(sourceBundle(s.remoteID, apisv1.SupportBundleStatusCollecting, ""))
			}
			sources["/apis/"+name+".example.com/v1"] = s
		}
		tm := newTestManager(t, config, sources, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Wait()
		assert.Equal(t, int32(maxParallelSources), inFlight.Load())

		close(release)
		b := tm.waitDone(t, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		assert.Equal(t, int32(maxParallelSources+2), inFlight.Load())
		for _, status := range b.Sources {
			assert.Equal(t, apisv1.SupportBundleStatusCollected, status.Status, status.Name)
		}
	})
}

// A source declaring maxConcurrent: 1 may use the same ID for every bundle: antrea-ui never creates
// a bundle there before the previous one is deleted.
func TestSourceMaxConcurrentSerializesCollections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		foo := newFakeSource()
		foo.remoteID = "foobar"
		plugins := []apisv1.PluginManifest{
			{Name: "foo", SupportBundle: &apisv1.SupportBundleSourceSpec{APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/foo.example.com/v1"}, MaxConcurrent: 1}},
		}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, plugins)
		first, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		second, err := tm.Create(testRequester, nil)
		require.NoError(t, err)

		for _, id := range []string{first.ID, second.ID} {
			b := tm.waitDone(t, id)
			assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
			assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "foo").Status)
		}
		var sequence []string
		for _, r := range foo.recorded() {
			if r.method == http.MethodPost || r.method == http.MethodDelete {
				sequence = append(sequence, r.method)
			}
		}
		assert.Equal(t, []string{http.MethodPost, http.MethodDelete, http.MethodPost, http.MethodDelete}, sequence)
		assert.Equal(t, 1, foo.maxOutstanding)
		assert.Empty(t, tm.slots.inUse)
	})
}

// The slot is held until the remote DELETE has returned. A collection that cannot get one within
// the collection timeout fails that source only.
func TestSourceMaxConcurrentWaitGivesUpAtTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		limited := apiServerExtraSource("limited", "limited.example.com")
		limited.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{limited, apiServerExtraSource("other", "other.example.com")}
		limitedSource, otherSource := newFakeSource(), newFakeSource()
		release := make(chan struct{})
		limitedSource.gate = func(r *http.Request) error {
			if r.Method == http.MethodDelete {
				<-release
			}
			return nil
		}
		tm := newTestManager(t, config, map[string]*fakeSource{
			"/apis/limited.example.com/v1": limitedSource,
			"/apis/other.example.com/v1":   otherSource,
		}, nil)
		var ids []string
		for range 2 {
			created, err := tm.Create(testRequester, nil)
			require.NoError(t, err)
			ids = append(ids, created.ID)
		}

		// Whichever bundle got the slot is stuck in its DELETE: the other one gives up on the
		// source at the collection timeout.
		var waiter *apisv1.SupportBundle
		for range 1000 {
			for _, id := range ids {
				b, err := tm.Get(id)
				require.NoError(t, err)
				if b.Status != apisv1.SupportBundleStatusCollecting {
					waiter = b
				}
			}
			if waiter != nil {
				break
			}
			time.Sleep(time.Second)
		}
		require.NotNil(t, waiter, "no bundle done")
		assert.Equal(t, apisv1.SupportBundleStatusCollected, waiter.Status)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, sourceStatus(waiter, "limited").Status)
		assert.Contains(t, sourceStatus(waiter, "limited").Error, "gave up waiting for other collections from this source to finish")
		assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(waiter, "other").Status)
		assert.Equal(t, 1, limitedSource.countRequests(http.MethodPost, "/supportbundles"))

		close(release)
		holder := ids[0]
		if waiter.ID == holder {
			holder = ids[1]
		}
		b := tm.waitDone(t, holder)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "limited").Status)
		assert.Equal(t, 1, limitedSource.countRequests(http.MethodPost, "/supportbundles"))
		assert.Equal(t, 1, limitedSource.maxOutstanding)
	})
}

// A source waiting for its slot does not hold one of the bundle's parallel slots, which the
// bundle's other sources can use meanwhile.
func TestSourceWaitingForItsSlotDoesNotHoldUpOtherSources(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		limited := apiServerExtraSource("limited", "limited.example.com")
		limited.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{limited}
		foo := newFakeSource()
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/limited.example.com/v1": foo}, nil)
		// Another bundle's collection holds the only slot of the source.
		release, err := tm.slots.acquire(t.Context(), "extra/limited", func() int { return 1 }, nil)
		require.NoError(t, err)

		parallel := make(chan struct{}, 1)
		var collectErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, collectErr = tm.collectSourceInTurn(t.Context(), &bundle{}, tm.extraSources[0].source, func() int { return 1 }, parallel, requester{username: testRequester, bundleID: "bundle-1"}, &apisv1.SupportBundleRequest{}, t.TempDir())
		}()
		synctest.Wait()
		assert.Empty(t, parallel)
		assert.Zero(t, foo.countRequests(http.MethodPost, "/supportbundles"))

		release()
		<-done
		require.NoError(t, collectErr)
		assert.Empty(t, parallel)
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
	})
}

// A limit added while a collection is running without one (e.g. by a plugin upgrade) still counts
// that collection.
func TestSourceMaxConcurrentAddedCountsRunningCollections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		foo := newFakeSource()
		release := make(chan struct{})
		foo.gate = func(r *http.Request) error {
			if r.Method == http.MethodDelete {
				<-release
			}
			return nil
		}
		spec := apisv1.SupportBundleSourceSpec{APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/foo.example.com/v1"}}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, []apisv1.PluginManifest{{Name: "foo", SupportBundle: &spec}})
		first, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Sleep(time.Minute)
		require.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))

		limited := spec
		limited.MaxConcurrent = 1
		tm.setPlugins([]apisv1.PluginManifest{{Name: "foo", SupportBundle: &limited}})
		second, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Sleep(time.Minute)
		assert.Equal(t, 1, foo.countRequests(http.MethodPost, "/supportbundles"))

		close(release)
		for _, id := range []string{first.ID, second.ID} {
			b := tm.waitDone(t, id)
			assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "foo").Status)
		}
		assert.Equal(t, 1, foo.maxOutstanding)
	})
}

// A limit added after a bundle was requested, but before its collection took its slot of the
// source, applies to that collection. A plugin that no longer declares a source keeps the limit it
// declared when the bundle was requested.
func TestSourceMaxConcurrentIsReadWhenTakingSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		foo := newFakeSource()
		spec := apisv1.SupportBundleSourceSpec{APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/foo.example.com/v1"}}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, []apisv1.PluginManifest{{Name: "foo", SupportBundle: &spec}})
		sources := tm.resolveSources()
		require.Len(t, sources, 1)

		limited := spec
		limited.MaxConcurrent = 1
		tm.setPlugins([]apisv1.PluginManifest{{Name: "foo", SupportBundle: &limited}})
		// Another bundle's collection holds the only slot of the source.
		release, err := tm.slots.acquire(t.Context(), "plugin/foo", func() int { return 1 }, nil)
		require.NoError(t, err)
		var collectErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, collectErr = tm.collectSourceInTurn(t.Context(), &bundle{}, sources[0].source, sources[0].maxConcurrent, make(chan struct{}, 1), requester{username: testRequester, bundleID: "bundle-1"}, &apisv1.SupportBundleRequest{}, t.TempDir())
		}()
		synctest.Wait()
		assert.Zero(t, foo.countRequests(http.MethodPost, "/supportbundles"))

		release()
		<-done
		require.NoError(t, collectErr)
		assert.Equal(t, 1, foo.countRequests(http.MethodPost, "/supportbundles"))

		sources = tm.resolveSources()
		tm.setPlugins(nil)
		assert.Equal(t, 1, sources[0].maxConcurrent())
	})
}

// A DELETE that times out gives back the slot of the source.
func TestSourceSlotIsReleasedWhenDeleteTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		foo := apiServerExtraSource("foo", "foo.example.com")
		foo.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{foo}
		fooSource := newFakeSource()
		var deletes atomic.Int32
		fooSource.gate = func(r *http.Request) error {
			if r.Method == http.MethodDelete && deletes.Add(1) == 1 {
				<-r.Context().Done()
				return r.Context().Err()
			}
			return nil
		}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": fooSource}, nil)
		var ids []string
		for range 2 {
			created, err := tm.Create(testRequester, nil)
			require.NoError(t, err)
			ids = append(ids, created.ID)
		}

		for _, id := range ids {
			b := tm.waitDone(t, id)
			assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "foo").Status)
		}
		var posts, deleteStarts []time.Time
		for _, r := range fooSource.recorded() {
			switch r.method {
			case http.MethodPost:
				posts = append(posts, r.at)
			case http.MethodDelete:
				deleteStarts = append(deleteStarts, r.at)
			}
		}
		require.Len(t, posts, 2)
		require.Len(t, deleteStarts, 2)
		assert.GreaterOrEqual(t, posts[1].Sub(deleteStarts[0]), remoteDeleteTimeout)
		assert.Empty(t, tm.slots.inUse)
	})
}

// The timeout of a DELETE starts once the request is authorized, so that a slow token mint does not
// make it fail.
func TestDeleteTimeoutStartsAfterAuthorization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var deleted bool
		conn := &sourceConn{
			client: newNoRedirectClient(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if err := req.Context().Err(); err != nil {
					return nil, err
				}
				deleted = true
				return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Request: req}, nil
			})),
			base: &url.URL{Scheme: "https", Host: "foo.example.com", Path: "/api/v1"},
			authorize: func(context.Context, *http.Request) error {
				time.Sleep(2 * remoteDeleteTimeout)
				return nil
			},
		}
		p := &protocolClient{conn: conn, maxBytes: 1024}
		require.NoError(t, p.delete("remote-1"))
		assert.True(t, deleted)
	})
}

// No POST is sent for a collection that has already given up, since a POST is not canceled once
// sent.
func TestNoCreateForCollectionThatGaveUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(errDeleted)
		_, err := tm.collectSource(ctx, &bundle{}, tm.extraSources[0].source, requester{username: testRequester, bundleID: "bundle-1"}, &apisv1.SupportBundleRequest{}, t.TempDir())
		require.ErrorIs(t, err, errDeleted)
		assert.Contains(t, err.Error(), "gave up on the source before requesting a bundle")
		assert.Empty(t, foo.recorded())
	})
}

// A POST is bounded by remoteCreateTimeout, so a source that never answers it only fails itself,
// and gives back its slot.
func TestCreateTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		foo := apiServerExtraSource("foo", "foo.example.com")
		foo.MaxConcurrent = 1
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{foo}
		fooSource := newFakeSource()
		fooSource.gate = func(r *http.Request) error {
			<-r.Context().Done()
			return r.Context().Err()
		}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": fooSource}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		start := time.Now()

		b := tm.waitDone(t, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, b.Status)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, sourceStatus(b, "foo").Status)
		assert.Contains(t, sourceStatus(b, "foo").Error, "failed to request a bundle")
		assert.Less(t, time.Since(start), remoteCreateTimeout+2*time.Second)
		assert.Empty(t, tm.slots.inUse)
	})
}

// A collection canceled while the source's POST is in flight still reads the response, and
// deletes the bundle the source created.
func TestCreateIsNotAbandonedWhenCollectionIsCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		release := make(chan struct{})
		foo.gate = func(r *http.Request) error {
			if r.Method != http.MethodPost {
				return nil
			}
			select {
			case <-release:
				return nil
			case <-r.Context().Done():
				return r.Context().Err()
			}
		}
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Wait()
		require.Equal(t, 1, foo.countRequests(http.MethodPost, "/supportbundles"))

		require.NoError(t, tm.Delete(created.ID))
		synctest.Wait()
		close(release)
		synctest.Wait()
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
		assert.Zero(t, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
	})
}

func TestRetryAfterIsClamped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		foo := newFakeSource()
		foo.retryAfter = "1000"
		foo.statusResponse = func(w http.ResponseWriter, poll int) {
			status := apisv1.SupportBundleStatusCollecting
			switch poll {
			case 1:
				w.Header().Set("Retry-After", "0")
			case 2:
				w.Header().Set("Retry-After", "not a number")
			default:
				status = apisv1.SupportBundleStatusCollected
			}
			_ = json.NewEncoder(w).Encode(sourceBundle("remote-1", status, ""))
		}
		_, b := collectFromFoo(t, testConfig(t), foo)
		require.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "foo").Status)

		var times []time.Time
		for _, r := range foo.recorded() {
			if r.method == http.MethodPost || r.method == http.MethodGet && strings.HasSuffix(r.path, "/supportbundles/remote-1") {
				times = append(times, r.at)
			}
		}
		require.Len(t, times, 4)
		assert.Equal(t, maxRetryAfter, times[1].Sub(times[0]), "1000s is clamped down")
		assert.Equal(t, minRetryAfter, times[2].Sub(times[1]), "0s is clamped up")
		assert.Equal(t, defaultRetryAfter, times[3].Sub(times[2]), "garbage falls back to the default")
	})
}

func TestDeleteCancelsCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		foo.pollsBeforeCollected = -1
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		synctest.Sleep(10 * time.Second)
		require.Equal(t, 1, foo.countRequests(http.MethodPost, "/supportbundles"))

		require.NoError(t, tm.Delete(created.ID))
		synctest.Wait()
		_, err = tm.Get(created.ID)
		assert.ErrorIs(t, err, ErrNotFound)
		assert.ErrorIs(t, tm.Delete(created.ID), ErrNotFound)
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
		// The cleanup DELETE is still made as antrea-ui-admin.
		requests := foo.recorded()
		require.Equal(t, http.MethodDelete, requests[len(requests)-1].method)
		assertServiceIdentity(t, requests[len(requests)-1], created.ID)
		_, err = os.Stat(filepath.Join(config.Directory, created.ID))
		assert.True(t, os.IsNotExist(err))
		assert.Zero(t, tm.budget.used.Load())

		// No more polling.
		polls := foo.countRequests(http.MethodGet, "/supportbundles/remote-1")
		time.Sleep(time.Minute)
		assert.Equal(t, polls, foo.countRequests(http.MethodGet, "/supportbundles/remote-1"))
	})
}

// A 401 from a source is that source's verdict on antrea-ui-admin: it fails that source only.
func TestSourceUnauthorizedFailsOnlyThatSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{
			apiServerExtraSource("foo", "foo.example.com"),
			apiServerExtraSource("bar", "bar.example.com"),
		}
		foo, bar := newFakeSource(), newFakeSource()
		foo.statusResponse = func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("who are you?"))
		}
		tm := newTestManager(t, config, map[string]*fakeSource{
			"/apis/foo.example.com/v1": foo,
			"/apis/bar.example.com/v1": bar,
		}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		b := tm.waitDone(t, created.ID)
		require.Equal(t, apisv1.SupportBundleStatusCollected, b.Status, b.Error)
		status := sourceStatus(b, "foo")
		assert.Equal(t, apisv1.SupportBundleStatusFailed, status.Status)
		assert.Equal(t, "failed to get the bundle status: source returned HTTP 401: who are you?", status.Error)
		assert.Equal(t, apisv1.SupportBundleStatusCollected, sourceStatus(b, "bar").Status)
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
	})
}

func TestStopCancelsCollection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		foo := newFakeSource()
		foo.pollsBeforeCollected = -1
		tm := newTestManager(t, config, map[string]*fakeSource{"/apis/foo.example.com/v1": foo}, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		time.Sleep(10 * time.Second)
		tm.stop()
		assert.Equal(t, 1, foo.countRequests(http.MethodDelete, "/supportbundles/remote-1"))
		b := readMetadata(t, config.Directory, created.ID)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		assert.Equal(t, errInterrupted, b.Error)
	})
}

// Source tarballs do not compress, so a bundle is about the size of its work files: assembling it
// must not need room for both at once.
func TestAssembleReleasesWorkFilesAsItGoes(t *testing.T) {
	const fileSize = 60 * 1024
	config := testConfig(t)
	// Room for the two work files and the tarball holding one of them, not for both copies of
	// both.
	config.MaxTotalBytes = 200 * 1024
	mi, err := NewManager(Options{Logger: testr.New(t), Config: config})
	require.NoError(t, err)
	m := mi.(*manager)
	b := &bundle{dir: filepath.Join(config.Directory, "b"), state: apisv1.SupportBundle{ID: "b"}}
	workDir := filepath.Join(b.dir, workDirName)
	contents := map[string]string{}
	for _, name := range []string{"extra/a.tar.gz", "extra/b.tar.gz"} {
		data := make([]byte, fileSize)
		_, _ = rand.Read(data)
		contents[name] = string(data)
		f, err := createChargedFile(filepath.Join(workDir, name), m.budget, &b.workBytes)
		require.NoError(t, err)
		_, err = f.Write(data)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	require.NoError(t, m.assemble(t.Context(), b, workDir))
	assert.Zero(t, b.workBytes.Load())
	assert.Equal(t, b.tarBytes.Load(), m.budget.used.Load())
	f, err := os.Open(filepath.Join(b.dir, bundleFileName))
	require.NoError(t, err)
	defer f.Close()
	files := readTarball(t, f)
	for name, data := range contents {
		assert.Equal(t, data, files[name], name)
	}
}

func readMetadata(t *testing.T, dir, id string) *apisv1.SupportBundle {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id, metadataFileName))
	require.NoError(t, err)
	var metadata bundleMetadata
	require.NoError(t, json.Unmarshal(data, &metadata))
	assert.Equal(t, metadataFormatVersion, metadata.FormatVersion)
	return &metadata.Bundle
}

func TestRestartRemovesBundlesWithInvalidMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		ids := []string{uuid.New().String(), uuid.New().String(), uuid.New().String()}
		for id, metadata := range map[string]string{
			ids[0]: fmt.Sprintf(`{"formatVersion":2,"bundle":{"id":%q,"status":"Collected"}}`, ids[0]),
			// The metadata of another bundle.
			ids[1]: fmt.Sprintf(`{"formatVersion":1,"bundle":{"id":%q,"status":"Failed"}}`, ids[2]),
			ids[2]: fmt.Sprintf(`{"formatVersion":1,"bundle":{"id":%q,"status":"Unknown"}}`, ids[2]),
		} {
			require.NoError(t, os.Mkdir(filepath.Join(config.Directory, id), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(config.Directory, id, metadataFileName), []byte(metadata), 0o600))
		}

		tm := newTestManager(t, config, nil, nil)
		assert.Empty(t, tm.List())
		for _, id := range ids {
			_, err := os.Stat(filepath.Join(config.Directory, id))
			assert.True(t, os.IsNotExist(err), id)
		}
	})
}

func TestRestartRestoresBundles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		config := testConfig(t)
		config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("foo", "foo.example.com")}
		sources := map[string]*fakeSource{"/apis/foo.example.com/v1": newFakeSource()}
		tm := newTestManager(t, config, sources, nil)
		created, err := tm.Create(testRequester, nil)
		require.NoError(t, err)
		collected := tm.waitDone(t, created.ID)
		require.Equal(t, apisv1.SupportBundleStatusCollected, collected.Status)
		tm.stop()

		bundleDir := func(id string) string { return filepath.Join(config.Directory, id) }
		writeBundle := func(state apisv1.SupportBundle, tarball []byte) {
			require.NoError(t, os.MkdirAll(filepath.Join(bundleDir(state.ID), workDirName), 0o700))
			require.NoError(t, writeMetadata(bundleDir(state.ID), &state))
			if tarball != nil {
				require.NoError(t, os.WriteFile(filepath.Join(bundleDir(state.ID), bundleFileName), tarball, 0o600))
			}
		}
		// Cut short by a crash while collecting.
		interrupted := apisv1.SupportBundle{
			ID:        uuid.New().String(),
			Status:    apisv1.SupportBundleStatusCollecting,
			ExpiresAt: time.Now().Add(time.Hour),
			Sources: []apisv1.SupportBundleSourceStatus{
				{Name: "foo", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusCollecting},
				{Name: "bar", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusCollected, Size: 1},
			},
		}
		writeBundle(interrupted, nil)
		// Collected, but its tarball does not match.
		truncated := apisv1.SupportBundle{ID: uuid.New().String(), Status: apisv1.SupportBundleStatusCollected, Size: 100, ExpiresAt: time.Now().Add(time.Hour)}
		writeBundle(truncated, []byte("short"))
		// No metadata.
		noMetadata := uuid.New().String()
		require.NoError(t, os.MkdirAll(bundleDir(noMetadata), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(bundleDir(noMetadata), bundleFileName), []byte("x"), 0o600))
		// Not ours.
		require.NoError(t, os.MkdirAll(filepath.Join(config.Directory, "not-a-bundle"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(config.Directory, "not-a-bundle", "file"), []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(config.Directory, "stray"), []byte("x"), 0o600))

		tm = newTestManager(t, config, sources, nil)

		restored, err := tm.Get(created.ID)
		require.NoError(t, err)
		assert.Equal(t, collected, restored)
		f, _, err := tm.Open(created.ID)
		require.NoError(t, err)
		files := readTarball(t, f)
		f.Close()
		assert.Contains(t, files, "manifest.json")
		assert.Equal(t, collected.Size, tm.budget.used.Load())

		b, err := tm.Get(interrupted.ID)
		require.NoError(t, err)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		assert.Equal(t, errInterrupted, b.Error)
		assert.Equal(t, apisv1.SupportBundleSourceStatus{Name: "foo", Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusFailed, Error: errInterrupted}, b.Sources[0])
		assert.Equal(t, interrupted.Sources[1], b.Sources[1])
		assert.Equal(t, b, readMetadata(t, config.Directory, interrupted.ID), "the new status is persisted")
		_, err = os.Stat(filepath.Join(bundleDir(interrupted.ID), workDirName))
		assert.True(t, os.IsNotExist(err))

		b, err = tm.Get(truncated.ID)
		require.NoError(t, err)
		assert.Equal(t, apisv1.SupportBundleStatusFailed, b.Status)
		_, err = os.Stat(filepath.Join(bundleDir(truncated.ID), bundleFileName))
		assert.True(t, os.IsNotExist(err))

		_, err = tm.Get(noMetadata)
		assert.ErrorIs(t, err, ErrNotFound)
		_, err = os.Stat(bundleDir(noMetadata))
		assert.True(t, os.IsNotExist(err))

		assert.FileExists(t, filepath.Join(config.Directory, "not-a-bundle", "file"))
		assert.FileExists(t, filepath.Join(config.Directory, "stray"))

		// Restored bundles count against the caps, and can be deleted.
		assert.Len(t, tm.List(), 3)
		tm.config.MaxBundles = 3
		_, err = tm.Create(testRequester, nil)
		assert.ErrorIs(t, err, ErrLimitReached)
		require.NoError(t, tm.Delete(created.ID))
		synctest.Wait()
		_, err = os.Stat(bundleDir(created.ID))
		assert.True(t, os.IsNotExist(err))
		assert.Zero(t, tm.budget.used.Load())
	})
}
