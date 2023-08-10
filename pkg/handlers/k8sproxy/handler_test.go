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

package k8sproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestK8sProxyHandler(t *testing.T) {
	var capturedReq *http.Request
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq = r
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	logger := testr.New(t)
	serverURL, err := url.Parse(ts.URL)
	require.NoError(t, err)
	h := NewK8sProxyHandler(logger, serverURL, http.DefaultTransport)

	req := httptest.NewRequest("GET", "/api/v1/pods", nil)
	req.RemoteAddr = "127.0.0.1:32167"
	req.Header.Add("X-Forwarded-For", "10.0.0.1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedReq)
	assert.Equal(t, "GET", capturedReq.Method)
	assert.Equal(t, "/api/v1/pods", capturedReq.URL.String())
	header := capturedReq.Header
	// original X-Forwarded-For value should have been preserved
	assert.Equal(t, "10.0.0.1, 127.0.0.1", header.Get("X-Forwarded-For"))
	// example.com is default for httptest.NewRequest
	assert.Equal(t, "example.com", header.Get("X-Forwarded-Host"))
	assert.Equal(t, "http", header.Get("X-Forwarded-Proto"))
	assert.Equal(t, serverURL.Host, capturedReq.Host)
}
