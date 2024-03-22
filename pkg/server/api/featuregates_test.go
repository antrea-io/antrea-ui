// Copyright 2024 Antrea Authors.
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

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetFeatureGates(t *testing.T) {
	ts := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/featuregates", nil)
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	testFeatureGates := []featureGate{
		{
			Component: "agent",
			Name:      "AntreaPolicy",
			Status:    "Enabled",
			Version:   "BETA",
		},
		{
			Component: "controller",
			Name:      "AntreaPolicy",
			Status:    "Enabled",
			Version:   "BETA",
		},
	}
	testData, err := json.Marshal(&testFeatureGates)
	require.NoError(t, err)
	ts.antreaSvcRequestsHandler.EXPECT().Request(gomock.Any(), "GET", "/featuregates", nil).Return(testData, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	resp := rr.Result()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, testData, b)
}
