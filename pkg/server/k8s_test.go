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

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestK8sProxyRequest(t *testing.T) {
	testCases := []struct {
		name               string
		path               string
		expectedStatusCode int
		expectedMessage    string
	}{
		{
			name:               "allowed path 1",
			path:               "/apis/crd.antrea.io/v1beta1/antreaagentinfos/node=A",
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "allowed path 2",
			path:               "/apis/crd.antrea.io/v1beta1/antreacontrollerinfos",
			expectedStatusCode: http.StatusOK,
		},
		{
			name:               "forbidden path",
			path:               "/api/v1/pods",
			expectedStatusCode: http.StatusNotFound,
			expectedMessage:    "This K8s API path is not being proxied",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t)
			path, err := url.JoinPath("/api/v1/k8s", tc.path)
			require.NoError(t, err)
			req := httptest.NewRequest("GET", path, nil)
			ts.authorizeRequest(req)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, tc.expectedStatusCode, rr.Code)
			if rr.Code == http.StatusOK {
				assert.Equal(t, tc.path, ts.k8sProxyHandler.request.URL.Path)
			}
			resp := rr.Result()
			if tc.expectedMessage != "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Contains(t, string(body), tc.expectedMessage)
			}
			assert.Empty(t, resp.Header.Get("Authorization"))
		})
	}
}
