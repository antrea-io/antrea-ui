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

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPluginLoading exercises the production plugin-loading mechanism end to end: the
// pod-counter example plugin is delivered as a labeled ConfigMap in a dedicated plugins
// namespace (plugins.namespace in ci/antrea-ui-values.yml, see docs/plugins.md), and this checks that the backend's ConfigMap watch serves the merged
// manifest index and the plugin's JS bundle, and that the plugin's own K8s proxy call succeeds
// under RBAC aggregation (the K8s proxy no longer applies its own path allowlist; RBAC is the
// only guard).
//
// The e2e deployment enables plugin signature verification (plugins.signature in
// ci/antrea-ui-values.yml), so pod-counter loading at all also covers the signed-plugin path:
// its ConfigMap carries a manifest.json.asc verifying against the key the workflow generates,
// and a manifest bundleSha256 matching its bundle.zip. The counterpart carrying no signature
// is covered by TestPluginWithoutSignatureIsRejected below.
func TestPluginLoading(t *testing.T) {
	ctx := t.Context()

	t.Run("plugin index", func(t *testing.T) {
		manifests, body := pluginIndex(ctx, t)

		var found bool
		for _, m := range manifests {
			if m.Name == "pod-counter" {
				found = true
				// Signature verification is on, so a manifest with no bundleSha256 could not
				// have loaded at all - assert it is actually there rather than leave the
				// signed path indistinguishable from the unsigned one.
				assert.NotEmpty(t, m.BundleSha256, "expected pod-counter's manifest to carry bundleSha256")
			}
		}
		assert.True(t, found, "expected to find pod-counter in the plugin index: %s", body)
	})

	t.Run("plugin bundle is served", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "api/v1/plugins/pod-counter/index.js", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("plugin's K8s proxy call is authorized", func(t *testing.T) {
		token, err := GetAccessToken(ctx, host)
		require.NoError(t, err)

		resp, err := Request(ctx, host, "GET", "api/v1/k8s/api/v1/pods", nil, setAccessTokenMutator(token))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}

// noSignaturePluginName is both the ConfigMap name and the manifest name of the plugin
// ci/e2e-plugins.sh installs to be rejected - the backend logs the former, the index would
// advertise the latter.
const noSignaturePluginName = "no-signature-plugin"

// TestPluginWithoutSignatureIsRejected covers the other half of signature verification: the
// workflow also installs no-signature-plugin, a labeled ConfigMap with a perfectly valid
// manifest.json and bundle.zip but no manifest.json.asc, which the backend must refuse to load.
//
// Everything here asserts an absence, which is only meaningful once the backend has actually
// tried to load the plugin - before that, "not in the index" and "rejected" look identical. So
// the test first waits for the log line the rejection path emits, and only then asserts. A
// retry can only fail the same way (a missing signature is permanent, not transient), so once
// that line exists there is no later moment at which the plugin could appear.
func TestPluginWithoutSignatureIsRejected(t *testing.T) {
	ctx := t.Context()

	// The happens-before edge every assertion below rests on.
	require.NoError(t,
		waitForBackendLogLine(ctx, "skipping invalid plugin ConfigMap", noSignaturePluginName),
		"the backend never logged a rejection for the %s ConfigMap", noSignaturePluginName)

	manifests, body := pluginIndex(ctx, t)
	for _, m := range manifests {
		assert.NotEqual(t, noSignaturePluginName, m.Name, "a plugin with no signature must not be served: %s", body)
	}

	// Nor may its files resolve: Index() and File() are separate lookups in the registry, and a
	// plugin that holds a name without being listed is a state the registry does model (see
	// Registry.claimed), so a 404 here is worth asserting rather than inferring.
	resp, err := Request(ctx, host, "GET", "api/v1/plugins/"+noSignaturePluginName+"/index.js", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestPluginBundleDownload covers a plugin whose ConfigMap carries only a signed manifest.json:
// ci/e2e-plugins.sh installs remote-plugin that way, with its bundle served by a stub aggregated
// API server registered with an APIService. For it to load, the backend's request has to go
// through kube-apiserver's aggregation layer, be authorized there for the antrea-ui ServiceAccount
// (the ClusterRole the script creates, shaped as docs/plugins.md describes), and come back as a
// bundle matching the digest in the signed manifest.
func TestPluginBundleDownload(t *testing.T) {
	ctx := t.Context()

	require.NoError(t,
		waitForBackendLogLine(ctx, "Loaded plugin from ConfigMap", remotePluginName),
		"the backend never loaded the %s ConfigMap", remotePluginName)

	manifests, body := pluginIndex(ctx, t)
	var found bool
	for _, m := range manifests {
		if m.Name == remotePluginName {
			found = true
			assert.NotEmpty(t, m.BundleSha256)
		}
	}
	assert.True(t, found, "expected to find %s in the plugin index: %s", remotePluginName, body)

	resp, err := Request(ctx, host, "GET", "api/v1/plugins/"+remotePluginName+"/index.js", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestPluginBundleDownloadOverHTTP is the same for the "http" transport: http-plugin's ConfigMap
// names, under bundleURL, a plain HTTP URL on the stub server's Service, and the backend fetches
// the bundle from there directly, with no kube-apiserver and no RBAC involved.
func TestPluginBundleDownloadOverHTTP(t *testing.T) {
	ctx := t.Context()

	require.NoError(t,
		waitForBackendLogLine(ctx, "Loaded plugin from ConfigMap", httpPluginName),
		"the backend never loaded the %s ConfigMap", httpPluginName)

	manifests, body := pluginIndex(ctx, t)
	var found bool
	for _, m := range manifests {
		if m.Name == httpPluginName {
			found = true
		}
	}
	assert.True(t, found, "expected to find %s in the plugin index: %s", httpPluginName, body)

	resp, err := Request(ctx, host, "GET", "api/v1/plugins/"+httpPluginName+"/index.js", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestPluginBundleDownloadWithoutRBAC is the other side: remote-plugin-denied names a resource of
// the same API group that the antrea-ui ServiceAccount has no RBAC for, so kube-apiserver refuses
// the download. That is a failure outside the ConfigMap, so the backend keeps retrying rather
// than giving up, and the plugin is never served.
//
// As in TestPluginWithoutSignatureIsRejected, the assertions are absences, so the test first
// waits for the log line showing that the backend tried and was refused.
func TestPluginBundleDownloadWithoutRBAC(t *testing.T) {
	ctx := t.Context()

	require.NoError(t,
		waitForBackendLogLine(ctx, "failed to download plugin bundle, will retry", deniedRemotePluginName, "403"),
		"the backend never logged a refused download for the %s ConfigMap", deniedRemotePluginName)

	manifests, body := pluginIndex(ctx, t)
	for _, m := range manifests {
		assert.NotEqual(t, deniedRemotePluginName, m.Name, "a plugin whose bundle was refused must not be served: %s", body)
	}
	resp, err := Request(ctx, host, "GET", "api/v1/plugins/"+deniedRemotePluginName+"/index.js", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// The ConfigMap and manifest names of the plugins ci/e2e-plugins.sh installs to be downloaded.
const (
	remotePluginName       = "remote-plugin"
	httpPluginName         = "http-plugin"
	deniedRemotePluginName = "remote-plugin-denied"
)

// pluginManifest is the subset of apis/v1.PluginManifest these tests assert on.
type pluginManifest struct {
	Name         string `json:"name"`
	BundleSha256 string `json:"bundleSha256"`
}

// pluginIndex fetches /api/v1/plugins/index.json, returning the decoded manifests plus the raw
// body for assertion messages.
func pluginIndex(ctx context.Context, t *testing.T) ([]pluginManifest, string) {
	t.Helper()
	resp, err := Request(ctx, host, "GET", "api/v1/plugins/index.json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var manifests []pluginManifest
	require.NoError(t, json.Unmarshal(body, &manifests))
	return manifests, string(body)
}
