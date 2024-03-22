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
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"k8s.io/client-go/dynamic"

	"antrea.io/antrea-ui/pkg/auth"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/handlers/antreasvc"
	"antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/password"
	"antrea.io/antrea-ui/pkg/server/api"
	"antrea.io/antrea-ui/pkg/server/errors"
)

type serverConfig struct {
	// keep all fields exported, so the config struct can be logged
	BasicAuthEnabled   bool
	OIDCAuthEnabled    bool
	OIDCNeedsLogout    bool
	CookieSecure       bool
	MaxLoginsPerSecond int
}

type Server struct {
	logger        logr.Logger
	config        serverConfig
	apiServer     *api.Server
	passwordStore password.Store
	tokenManager  auth.TokenManager
	oidcProvider  *OIDCProvider
}

func NewServer(
	logger logr.Logger,
	k8sClient dynamic.Interface,
	traceflowRequestsHandler traceflow.RequestsHandler,
	k8sProxyHandler http.Handler,
	antreaSvcRequestsHandler antreasvc.RequestsHandler,
	passwordStore password.Store,
	tokenManager auth.TokenManager,
	oidcProvider *OIDCProvider,
	config *serverconfig.Config,
) *Server {
	c := serverConfig{
		BasicAuthEnabled:   config.Auth.Basic.Enabled,
		OIDCAuthEnabled:    config.Auth.OIDC.Enabled,
		OIDCNeedsLogout:    (config.Auth.OIDC.LogoutURL != ""),
		CookieSecure:       config.Auth.CookieSecure,
		MaxLoginsPerSecond: config.Limits.MaxLoginsPerSecond,
	}
	logger.Info("Created server config", "config", c)
	return &Server{
		logger: logger,
		config: c,
		apiServer: api.NewServer(
			logger,
			k8sClient,
			traceflowRequestsHandler,
			k8sProxyHandler,
			antreaSvcRequestsHandler,
			passwordStore,
			tokenManager,
			config,
		),
		passwordStore: passwordStore,
		tokenManager:  tokenManager,
		oidcProvider:  oidcProvider,
	}
}

func (s *Server) AddRoutes(router *gin.Engine) {
	router.GET("/healthz", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	s.apiServer.AddRoutes(&router.RouterGroup)
	s.AddAuthRoutes(&router.RouterGroup)
}

func (s *Server) LogError(sError *errors.ServerError, msg string, keysAndValues ...interface{}) {
	errors.LogError(s.logger, sError, msg, keysAndValues...)
}
