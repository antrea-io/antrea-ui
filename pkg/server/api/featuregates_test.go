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
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"antrea.io/antrea-ui/pkg/auth/session"
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
	// The handler resolves the caller's credential from the context it is given, so assert that
	// the identity actually made it across. Passing the *gin.Context here instead of the request
	// context would silently drop it: gin only forwards Value() lookups to the request context
	// when Engine.ContextWithFallback is set.
	ts.antreaSvcRequestsHandler.EXPECT().Request(gomock.Any(), "GET", "/featuregates", nil).
		DoAndReturn(func(ctx context.Context, _ string, _ string, _ io.Reader) ([]byte, int, error) {
			ra, ok := session.RequestAuthFrom(ctx)
			require.True(t, ok, "context passed to the Antrea Service carries no identity")
			assert.Equal(t, session.ModeAdmin, ra.Mode)
			return testData, http.StatusOK, nil
		})
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	resp := rr.Result()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, testData, b)
}
