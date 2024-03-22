// Copyright 2023 Antrea Authors.
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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLoginRateLimiting(t *testing.T) {
	ctx := context.Background()
	badLogin := func() int {
		resp, err := Request(ctx, host, "POST", "auth/login", nil, func(req *http.Request) {
			req.SetBasicAuth("admin", "bad") // invalid password
		})
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// reset rate limiting for login API
	t.Cleanup(func() { time.Sleep(1 * time.Second) })

	require.Equal(t, http.StatusUnauthorized, badLogin())
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, http.StatusTooManyRequests, badLogin())
	time.Sleep(1 * time.Second)
	require.Equal(t, http.StatusUnauthorized, badLogin())
}

func setAccessTokenMutator(token string) func(req *http.Request) {
	return func(req *http.Request) {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	}
}

func getResponseBody[T any](resp *http.Response, data *T) error {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, data)
}

func TestAPI(t *testing.T) {
	ctx := context.Background()
	token, err := GetAccessToken(ctx, host)
	require.NoError(t, err)
	t.Logf("Obtained access token to UI")

	t.Run("k8s", func(t *testing.T) {
		t.Run("antreaagentinfos", func(t *testing.T) {
			resp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos", nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			var data metav1.PartialObjectMetadataList
			require.NoError(t, getResponseBody(resp, &data))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.NotEmpty(t, data.Items)
		})
		t.Run("antreacontrollerinfos", func(t *testing.T) {
			resp, err := Request(ctx, host, "GET", "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller", nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			var data metav1.PartialObjectMetadata
			require.NoError(t, getResponseBody(resp, &data))
			require.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, "antrea-controller", data.Name)
		})
	})

	t.Run("featuregates", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "api/v1/featuregates", nil, setAccessTokenMutator(token))
		require.NoError(t, err)
		var featureGates []any
		require.NoError(t, getResponseBody(resp, &featureGates))
		assert.NotEmpty(t, featureGates)
	})

	t.Run("traceflow", func(t *testing.T) {
		// set-up: we create 2 Pods which we can use for a simple Traceflow request
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, ns)
		_, pods, err := createTestDeployment(ctx, ns, "tf", 2)
		require.NoError(t, err)
		require.Len(t, pods, 2)

		tf := map[string]interface{}{
			"spec": map[string]interface{}{
				"source": map[string]interface{}{
					"namespace": ns,
					"pod":       pods[0].Name,
				},
				"destination": map[string]interface{}{
					"namespace": ns,
					"pod":       pods[1].Name,
				},
			},
		}
		b, err := json.Marshal(tf)
		require.NoError(t, err)

		// we assume that rate-limiting won't be an issue for this test
		resp, err := Request(ctx, host, "POST", "api/v1/traceflow", bytes.NewBuffer(b), setAccessTokenMutator(token))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		url, err := resp.Location()
		require.NoError(t, err)
		reqURI := url.RequestURI()
		defer func() {
			resp, err := Request(ctx, host, "DELETE", reqURI, nil, setAccessTokenMutator(token))
			if !assert.NoError(t, err) {
				return
			}
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "Failed to delete Traceflow")
		}()

		statusURI := reqURI + "/status"

		require.Eventually(t, func() bool {
			resp, err := Request(ctx, host, "GET", statusURI, nil, setAccessTokenMutator(token))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			// when the Traceflow completes, there will be an automatic redirect to the result
			return strings.HasSuffix(resp.Request.URL.Path, "/result")
		}, 30*time.Second, 1*time.Second)
	})
}

func TestAPIUnauthorized(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		path   string
		method string
	}{
		{
			path:   "api/v1/k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos",
			method: "GET",
		},
		{
			path:   "api/v1/traceflow",
			method: "POST",
		},
		{
			path:   "api/v1/account/password",
			method: "PUT",
		},
	}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			resp, err := Request(ctx, host, tc.method, tc.path, nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}
