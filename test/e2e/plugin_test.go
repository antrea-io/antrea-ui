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
// pod-counter example plugin is delivered as a labeled ConfigMap in antrea-ui's own namespace
// (see docs/plugins.md), and this checks that the backend's ConfigMap watch serves the merged
// manifest index and the plugin's JS bundle, and that the K8s proxy allow-list + RBAC
// aggregation actually let the plugin's API call through.
func TestPluginLoading(t *testing.T) {
	ctx := context.Background()

	t.Run("plugin index", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "api/v1/plugins/index.json", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var manifests []struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.Unmarshal(body, &manifests))

		var found bool
		for _, m := range manifests {
			if m.Name == "pod-counter" {
				found = true
			}
		}
		assert.True(t, found, "expected to find pod-counter in the plugin index: %s", string(body))
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
