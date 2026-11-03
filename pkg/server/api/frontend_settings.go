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

package api

import (
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/version"
)

// buildFrontendFlowSources exposes each flow source's name and capabilities only. The settings
// are served without authentication, so nothing about where a source is or how it is reached
// (address, namespace, CA, server name) may be added here.
func buildFrontendFlowSources(sources []serverconfig.FlowSourceConfig) []apisv1.FrontendFlowSource {
	result := make([]apisv1.FrontendFlowSource, 0, len(sources))
	for _, source := range sources {
		result = append(result, apisv1.FrontendFlowSource{
			Name:         source.Name,
			Capabilities: slices.Clone(source.Capabilities),
		})
	}
	return result
}

func buildFrontendSettingsFromConfig(config *serverconfig.Config) *apisv1.FrontendSettings {
	return &apisv1.FrontendSettings{
		Version: version.GetFullVersion(),
		Auth: apisv1.FrontendAuthSettings{
			BasicEnabled:      config.Auth.Basic.Enabled,
			OIDCEnabled:       config.Auth.OIDC.Enabled,
			OIDCProviderName:  config.Auth.OIDC.ProviderName,
			KubeconfigEnabled: config.Auth.Kubeconfig.Enabled,
			TokenEnabled:      config.Auth.Token.Enabled,
		},
		Features: apisv1.FrontendFeatureSettings{
			FlowVisibilityEnabled: len(config.FlowSources) > 0,
			FlowSources:           buildFrontendFlowSources(config.FlowSources),
			MetricsEnabled:        config.Metrics.Enabled,
		},
	}
}

func (s *Server) FrontendSettings(c *gin.Context) {
	c.JSON(http.StatusOK, s.frontendSettings)
}
