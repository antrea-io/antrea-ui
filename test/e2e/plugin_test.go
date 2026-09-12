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
