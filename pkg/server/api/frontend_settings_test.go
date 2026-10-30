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

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

func TestFlowHistorySettings(t *testing.T) {
	ts := newTestServer(t)
	for _, live := range []bool{false, true} {
		for _, history := range []bool{false, true} {
			config := &serverconfig.Config{}
			config.FlowAggregator.Enabled = live
			config.FlowHistory.Enabled = history
			config.FlowHistory.Address = "private-history.example.test:14740"
			config.FlowHistory.CAConfigMap = "private-history-ca"
			ts.s.frontendSettings = buildFrontendSettingsFromConfig(config)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/settings", nil))
			require.Equal(t, 200, rr.Code)
			var settings apisv1.FrontendSettings
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &settings))
			require.Equal(t, live && history, settings.Features.FlowHistoryEnabled)
			require.NotContains(t, rr.Body.String(), "private-history")
		}
	}
}
