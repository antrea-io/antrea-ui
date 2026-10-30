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

func TestFlowSourceSettings(t *testing.T) {
	ts := newTestServer(t)
	for name, sources := range map[string][]serverconfig.FlowSourceConfig{
		"none": nil,
		"one": {{
			Name: "private-source", Capabilities: []string{serverconfig.FlowSourceCapabilityList},
			Address: "private-history.example.test:14740", CAConfigMap: "private-history-ca",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			config := &serverconfig.Config{FlowSources: sources}
			ts.s.frontendSettings = buildFrontendSettingsFromConfig(config)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/settings", nil))
			require.Equal(t, 200, rr.Code)
			var settings apisv1.FrontendSettings
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &settings))
			require.Equal(t, len(sources) > 0, settings.Features.FlowVisibilityEnabled)
			// The endpoint is unauthenticated: it must not describe the deployment's sources.
			require.NotContains(t, rr.Body.String(), "private-")
		})
	}
}
