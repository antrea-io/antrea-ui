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

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/handlers/antreasvc"
	"antrea.io/antrea-ui/pkg/handlers/flowstream"
	"antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/k8s"
	"antrea.io/antrea-ui/pkg/password"
	"antrea.io/antrea-ui/pkg/plugins"
	"antrea.io/antrea-ui/pkg/server/api"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

type serverConfig struct {
	// keep all fields exported, so the config struct can be logged
	BasicAuthEnabled      bool
	OIDCAuthEnabled       bool
	OIDCNeedsLogout       bool
	KubeconfigAuthEnabled bool
	SATokenAuthEnabled    bool
	CookieSecure          bool
	MaxLoginsPerSecond    int
	// AdminUserName is the Kubernetes identity impersonated for sessions created with the
	// static admin password.
	AdminUserName string
}

// Options are the dependencies of the top-level HTTP server.
type Options struct {
	Logger                   logr.Logger
	Config                   *serverconfig.Config
	TraceflowRequestsHandler traceflow.RequestsHandler
	K8sProxyHandler          http.Handler
	AntreaSvcRequestsHandler antreasvc.RequestsHandler
	FlowStreamSubscriber     flowstream.FlowStreamSubscriber
	PasswordStore            password.Store
	// SessionStore holds every logged-in user's Kubernetes credential, in memory only.
	SessionStore session.Store
	// ClientFactory builds Kubernetes clients that act as the caller, and validates a
	// credential at login time.
	ClientFactory  *k8s.ClientFactory
	OIDCProvider   *OIDCProvider
	PluginRegistry *plugins.Registry
	// AdminUserName is the Kubernetes identity impersonated for admin-password sessions.
	AdminUserName string
}

type Server struct {
	logger        logr.Logger
	config        serverConfig
	apiServer     *api.Server
	passwordStore password.Store
	sessionStore  session.Store
	clientFactory *k8s.ClientFactory
	authenticator *authn.Authenticator
	oidcProvider  *OIDCProvider
}

func NewServer(o Options) (*Server, error) {
	c := serverConfig{
		BasicAuthEnabled:      o.Config.Auth.Basic.Enabled,
		OIDCAuthEnabled:       o.Config.Auth.OIDC.Enabled,
		OIDCNeedsLogout:       (o.Config.Auth.OIDC.LogoutURL != ""),
		KubeconfigAuthEnabled: o.Config.Auth.Kubeconfig.Enabled,
		SATokenAuthEnabled:    o.Config.Auth.ServiceAccountToken.Enabled,
		CookieSecure:          o.Config.Auth.CookieSecure,
		MaxLoginsPerSecond:    o.Config.Limits.MaxLoginsPerSecond,
		AdminUserName:         o.AdminUserName,
	}
	o.Logger.Info("Created server config", "config", c)

	authenticator, err := authn.NewFromServerConfig(o.Logger, o.Config, o.SessionStore, o.ClientFactory)
	if err != nil {
		return nil, err
	}

	return &Server{
		logger: o.Logger,
		config: c,
		apiServer: api.NewServer(api.Options{
			Logger:                   o.Logger,
			Config:                   o.Config,
			TraceflowRequestsHandler: o.TraceflowRequestsHandler,
			K8sProxyHandler:          o.K8sProxyHandler,
			AntreaSvcRequestsHandler: o.AntreaSvcRequestsHandler,
			FlowStreamSubscriber:     o.FlowStreamSubscriber,
			PasswordStore:            o.PasswordStore,
			PluginRegistry:           o.PluginRegistry,
			Authenticator:            authenticator,
			ClientFactory:            o.ClientFactory,
		}),
		passwordStore: o.PasswordStore,
		sessionStore:  o.SessionStore,
		clientFactory: o.ClientFactory,
		authenticator: authenticator,
		oidcProvider:  o.OIDCProvider,
	}, nil
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
