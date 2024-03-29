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

	"github.com/gin-gonic/gin"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/version"
)

func buildFrontendSettingsFromConfig(config *serverconfig.Config) *apisv1.FrontendSettings {
	return &apisv1.FrontendSettings{
		Version: version.GetFullVersion(),
		Auth: apisv1.FrontendAuthSettings{
			BasicEnabled:     config.Auth.Basic.Enabled,
			OIDCEnabled:      config.Auth.OIDC.Enabled,
			OIDCProviderName: config.Auth.OIDC.ProviderName,
		},
	}
}

func (s *Server) FrontendSettings(c *gin.Context) {
	c.JSON(http.StatusOK, s.frontendSettings)
}
