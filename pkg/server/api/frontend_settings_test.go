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
	for name, tc := range map[string]struct {
		sources []serverconfig.FlowSourceConfig
		want    []apisv1.FrontendFlowSource
		visible bool
	}{
		"none": {want: []apisv1.FrontendFlowSource{}},
		"list only": {
			sources: []serverconfig.FlowSourceConfig{{Name: "flow-history", Capabilities: []string{serverconfig.FlowSourceCapabilityList}}},
			want:    []apisv1.FrontendFlowSource{{Name: "flow-history", Capabilities: []string{"list"}}},
			visible: true,
		},
		"in order": {
			visible: true,
			sources: []serverconfig.FlowSourceConfig{
				{
					Name: "flow-aggregator", Capabilities: []string{serverconfig.FlowSourceCapabilityWatch},
					Address: "private-fa.example.test:14740", Namespace: "private-ns", CAConfigMap: "private-fa-ca",
					ServerName: "private-name",
				},
				{
					Name: "flow-history", Capabilities: []string{serverconfig.FlowSourceCapabilityList, serverconfig.FlowSourceCapabilityWatch},
					Address: "private-history.example.test:14740", CAConfigMap: "private-history-ca", InsecureSkipVerify: true,
				},
			},
			want: []apisv1.FrontendFlowSource{
				{Name: "flow-aggregator", Capabilities: []string{"watch"}},
				{Name: "flow-history", Capabilities: []string{"list", "watch"}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := &serverconfig.Config{FlowSources: tc.sources}
			ts.s.frontendSettings = buildFrontendSettingsFromConfig(config)
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/settings", nil))
			require.Equal(t, 200, rr.Code)
			var settings apisv1.FrontendSettings
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &settings))
			require.Equal(t, tc.visible, settings.Features.FlowVisibilityEnabled)
			require.Equal(t, tc.want, settings.Features.FlowSources)
			// An empty list is an empty array, so a client can always iterate over it.
			if len(tc.sources) == 0 {
				require.Contains(t, rr.Body.String(), `"flowSources":[]`)
			}
			// The endpoint is unauthenticated: it must say nothing about where a source is or
			// how it is reached.
			require.NotContains(t, rr.Body.String(), "private")
			require.NotContains(t, rr.Body.String(), "insecureSkipVerify")
		})
	}
}

func TestFlowSourceSettingsDoNotAliasConfig(t *testing.T) {
	config := &serverconfig.Config{FlowSources: []serverconfig.FlowSourceConfig{
		{Name: "a", Capabilities: []string{serverconfig.FlowSourceCapabilityWatch}},
	}}
	settings := buildFrontendSettingsFromConfig(config)
	settings.Features.FlowSources[0].Capabilities[0] = "changed"
	require.Equal(t, serverconfig.FlowSourceCapabilityWatch, config.FlowSources[0].Capabilities[0])
}
