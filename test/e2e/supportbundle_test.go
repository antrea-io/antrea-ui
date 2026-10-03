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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	// Installed by ci/e2e-plugins.sh, declaring sourceAPIPath as its support bundle source.
	supportBundlePluginName = "supportbundle-plugin"
	// Configured through the values file written by ci/e2e-supportbundle.sh.
	extraSourceName = "e2e-https"
	// The APIService registered by ci/e2e-supportbundle.sh.
	sourceAPIPath = "/apis/supportbundle.e2e.antrea.io/v1alpha1"
	// The identity every source is called as, whoever requests the bundle.
	adminUser = "system:serviceaccount:" + antreaNamespace + ":antrea-ui-admin"
	// The ServiceAccount requesting the bundle, in a test namespace.
	requesterSAName = "supportbundle-requester"
)

// sourceIdentity is the content of identity.json in a tarball returned by
// test/e2e/supportbundle-source.
type sourceIdentity struct {
	Via                 string `json:"via"`
	User                string `json:"user"`
	Since               string `json:"since"`
	RequestedBy         string `json:"requestedBy"`
	BundleID            string `json:"bundleID"`
	AuthorizationHeader bool   `json:"authorizationHeader"`
}

// readTarball returns the regular files of a .tar.gz, by name.
func readTarball(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		require.NoError(t, err)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		files[h.Name] = content
	}
}

func readIdentity(t *testing.T, files map[string][]byte, name string) sourceIdentity {
	t.Helper()
	data, ok := files[name]
	require.True(t, ok, "expected %s in the bundle", name)
	inner := readTarball(t, data)
	var identity sourceIdentity
	require.NoError(t, json.Unmarshal(inner["identity.json"], &identity))
	return identity
}

func supportBundleRequest(ctx context.Context, t *testing.T, token, method, path string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	resp, err := Request(ctx, host, method, path, body, setAccessTokenMutator(token), func(req *http.Request) {
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, data
}

// sourceBundles lists the bundles the source still holds, through the apiserver as cluster-admin.
func sourceBundles(ctx context.Context) ([]apisv1.SupportBundle, error) {
	data, err := k8sClient.Discovery().RESTClient().Get().AbsPath(sourceAPIPath, "supportbundle").DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	var list apisv1.SupportBundleList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// TestSupportBundle collects a support bundle from both kinds of secondary source, served by
// test/e2e/supportbundle-source: an APIService declared by a plugin, and an HTTPS extra source. The
// bundle is requested by a ServiceAccount that only holds the support bundle grant. Each source
// records the identity it was called as, which must be antrea-ui-admin, and who antrea-ui says
// requested which bundle.
func TestSupportBundle(t *testing.T) {
	ctx := t.Context()

	// Plugin ConfigMaps are created after Antrea UI is installed, and loaded asynchronously.
	var index string
	require.NoError(t, wait.PollUntilContextTimeout(ctx, time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		resp, err := Request(ctx, host, "GET", "api/v1/plugins/index.json", nil)
		if err != nil {
			index = err.Error()
			return false, nil
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		index = string(data)
		var manifests []pluginManifest
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &manifests) != nil {
			return false, nil
		}
		return slices.ContainsFunc(manifests, func(m pluginManifest) bool { return m.Name == supportBundlePluginName }), nil
	}), "%s never appeared in the plugin index: %s", supportBundlePluginName, index)

	ns, err := createTestNamespace(ctx)
	require.NoError(t, err)
	defer deleteNamespace(context.Background(), ns)
	token := createServiceAccountWithToken(ctx, t, ns, requesterSAName)
	requester := "system:serviceaccount:" + ns + ":" + requesterSAName
	createClusterRoleBinding(ctx, t, randName("e2e-supportbundle-requester-"),
		[]rbacv1.PolicyRule{{APIGroups: []string{"ui.antrea.io"}, Resources: []string{"supportbundles"}, Verbs: []string{"create", "get", "list", "delete"}}},
		rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: requesterSAName, Namespace: ns},
	)

	// Retried on 403, until the binding has propagated, and on transient errors.
	var resp *http.Response
	var body []byte
	var lastErr string
	require.NoError(t, wait.PollUntilContextTimeout(ctx, time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		r, err := Request(ctx, host, "POST", "api/v1/supportbundle", strings.NewReader(`{"since": "1h"}`), setAccessTokenMutator(token), func(req *http.Request) {
			req.Header.Set("Content-Type", "application/json")
		})
		if err != nil {
			lastErr = err.Error()
			return false, nil
		}
		defer r.Body.Close()
		resp = r
		body, _ = io.ReadAll(r.Body)
		lastErr = fmt.Sprintf("HTTP %d: %s", r.StatusCode, body)
		return r.StatusCode != http.StatusForbidden, nil
	}), "the support bundle grant never propagated: %s", lastErr)
	require.Equal(t, http.StatusAccepted, resp.StatusCode, string(body))
	var created apisv1.SupportBundle
	require.NoError(t, json.Unmarshal(body, &created))
	assert.Equal(t, requester, created.CreatedBy)
	statusPath := "api/v1/supportbundle/" + created.ID + "/status"
	assert.Equal(t, "/"+statusPath, resp.Header.Get("Location"))

	var bundle apisv1.SupportBundle
	var lastStatus string
	require.NoError(t, wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Retried rather than fatal: a transient error is not the bundle's outcome.
		resp, err := Request(ctx, host, "GET", statusPath, nil, setAccessTokenMutator(token))
		if err != nil {
			lastStatus = err.Error()
			return false, nil
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		lastStatus = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, body)
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &bundle) != nil {
			return false, nil
		}
		return bundle.Status != apisv1.SupportBundleStatusCollecting, nil
	}), "the bundle never finished collecting, last status: %s", lastStatus)
	require.Equal(t, apisv1.SupportBundleStatusCollected, bundle.Status, bundle.Error)
	sources := make([]apisv1.SupportBundleSourceStatus, 0, len(bundle.Sources))
	for _, s := range bundle.Sources {
		// Sizes depend on gzip's output.
		s.Size = 0
		sources = append(sources, s)
	}
	assert.ElementsMatch(t, []apisv1.SupportBundleSourceStatus{
		{Name: supportBundlePluginName, Kind: apisv1.SupportBundleSourceKindPlugin, Status: apisv1.SupportBundleStatusCollected},
		{Name: extraSourceName, Kind: apisv1.SupportBundleSourceKindExtra, Status: apisv1.SupportBundleStatusCollected},
	}, sources)

	t.Run("download", func(t *testing.T) {
		resp, data := supportBundleRequest(ctx, t, token, "GET", "api/v1/supportbundle/"+created.ID+"/download", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(data))
		files := readTarball(t, data)
		for _, name := range []string{"manifest.json", "antrea-ui-backend/version.txt", "antrea-ui-backend/config.yaml", "antrea-ui-backend/plugins.json", "antrea-ui-backend/logs/antrea-ui.log"} {
			assert.Contains(t, files, name)
		}
		assert.NotContains(t, string(files["antrea-ui-backend/config.yaml"]), "antrea-ui-e2e-secret", "the OIDC client secret (ci/antrea-ui-values.yml) must be redacted")

		// The apiserver forwards the identity antrea-ui impersonated and its extras, and no
		// credential; the HTTPS source resolves the token it received with a TokenReview for its
		// audience, and reads the X-Antrea-UI-* headers.
		assert.Equal(t, sourceIdentity{Via: "apiserver", User: adminUser, Since: "1h", RequestedBy: requester, BundleID: created.ID, AuthorizationHeader: false}, readIdentity(t, files, "plugins/"+supportBundlePluginName+".tar.gz"))
		assert.Equal(t, sourceIdentity{Via: "https", User: adminUser, Since: "1h", RequestedBy: requester, BundleID: created.ID, AuthorizationHeader: true}, readIdentity(t, files, "extra/"+extraSourceName+".tar.gz"))
	})

	t.Run("sources' bundles are deleted", func(t *testing.T) {
		var bundles []apisv1.SupportBundle
		var lastErr error
		err := wait.PollUntilContextTimeout(ctx, time.Second, 10*time.Second, true, func(ctx context.Context) (bool, error) {
			bundles, lastErr = sourceBundles(ctx)
			return lastErr == nil && len(bundles) == 0, nil
		})
		assert.NoError(t, err, "the source still holds %v (last error: %v)", bundles, lastErr)
	})

	t.Run("list and delete", func(t *testing.T) {
		resp, body := supportBundleRequest(ctx, t, token, "GET", "api/v1/supportbundle", nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		var list apisv1.SupportBundleList
		require.NoError(t, json.Unmarshal(body, &list))
		assert.True(t, slices.ContainsFunc(list.Items, func(b apisv1.SupportBundle) bool { return b.ID == created.ID }))

		resp, body = supportBundleRequest(ctx, t, token, "DELETE", "api/v1/supportbundle/"+created.ID, nil)
		require.Less(t, resp.StatusCode, 300, string(body))
		resp, _ = supportBundleRequest(ctx, t, token, "GET", statusPath, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
