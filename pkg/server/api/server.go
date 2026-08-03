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
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/handlers/antreasvc"
	"antrea.io/antrea-ui/pkg/handlers/flowstream"
	"antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/k8s"
	"antrea.io/antrea-ui/pkg/password"
	"antrea.io/antrea-ui/pkg/plugins"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/version"
)

type serverConfig struct {
	// keep all fields exported, so the config struct can be logged
	MaxTraceflowsPerHour int
}

// Options are the dependencies of the API server.
type Options struct {
	Logger                   logr.Logger
	Config                   *serverconfig.Config
	TraceflowRequestsHandler traceflow.RequestsHandler
	K8sProxyHandler          http.Handler
	AntreaSvcRequestsHandler antreasvc.RequestsHandler
	FlowStreamSubscriber     flowstream.FlowStreamSubscriber
	PasswordStore            password.Store
	PluginRegistry           *plugins.Registry
	// Authenticator resolves the caller's identity for every protected route.
	Authenticator *authn.Authenticator
	// ClientFactory builds Kubernetes clients that act as the caller.
	ClientFactory *k8s.ClientFactory
}

type Server struct {
	logger                   logr.Logger
	traceflowRequestsHandler traceflow.RequestsHandler
	k8sProxyHandler          http.Handler
	antreaSvcRequestsHandler antreasvc.RequestsHandler
	flowStreamSSEHandler     *flowstream.SSEHandler
	passwordStore            password.Store
	authenticator            *authn.Authenticator
	clientFactory            *k8s.ClientFactory
	config                   serverConfig
	frontendSettings         *apisv1.FrontendSettings
	pluginRegistry           *plugins.Registry
}

func NewServer(o Options) *Server {
	c := serverConfig{
		MaxTraceflowsPerHour: o.Config.Limits.MaxTraceflowsPerHour,
	}
	o.Logger.Info("Created API server config", "config", c)
	var flowSSEHandler *flowstream.SSEHandler
	if o.FlowStreamSubscriber != nil {
		flowSSEHandler = flowstream.NewSSEHandler(o.Logger, o.FlowStreamSubscriber)
	}
	return &Server{
		logger:                   o.Logger,
		traceflowRequestsHandler: o.TraceflowRequestsHandler,
		k8sProxyHandler:          o.K8sProxyHandler,
		antreaSvcRequestsHandler: o.AntreaSvcRequestsHandler,
		flowStreamSSEHandler:     flowSSEHandler,
		passwordStore:            o.PasswordStore,
		authenticator:            o.Authenticator,
		clientFactory:            o.ClientFactory,
		config:                   c,
		frontendSettings:         buildFrontendSettingsFromConfig(o.Config),
		pluginRegistry:           o.PluginRegistry,
	}
}

// authenticate is the middleware protecting every route that acts on the user's behalf. It
// resolves the session cookie (or the Authorization: Bearer fallback) into the credential that
// downstream handlers present to Kubernetes.
func (s *Server) authenticate() gin.HandlerFunc {
	return s.authenticator.Middleware()
}

//nolint:unused
func announceDeprecationMiddleware(removalDate time.Time, message string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Warning", fmt.Sprintf(`299 - "Deprecated API: %s"`, message))
		c.Header("Sunset", removalDate.UTC().Format(http.TimeFormat))
	}
}

func (s *Server) AddRoutes(r *gin.RouterGroup) {
	apiv1 := r.Group("/api/v1")
	apiv1.GET("/version", func(c *gin.Context) {
		c.String(http.StatusOK, version.GetFullVersionWithRuntimeInfo())
	})
	apiv1.GET("/settings", s.FrontendSettings)
	s.AddPluginsRoutes(apiv1)
	s.AddTraceflowRoutes(apiv1)
	s.AddAccountRoutes(apiv1)
	s.AddK8sRoutes(apiv1)
	apiv1.GET("/featuregates", s.authenticate(), s.GetFeatureGates)
	s.AddFlowStreamRoutes(apiv1)
}

func (s *Server) AddFlowStreamRoutes(r *gin.RouterGroup) {
	flows := r.Group("/flows")
	flows.Use(s.authenticate())
	if s.flowStreamSSEHandler == nil {
		flows.GET("/stream", s.flowStreamDisabled)
		return
	}
	flows.GET("/stream", s.flowStreamSSEHandler.StreamFlows)
}

// flowStreamDisabled handles GET /api/v1/flows/stream when Flow Aggregator integration is off.
//
// 501, not 503: this is a static per-deployment configuration choice, not a transient condition
// that a retry could resolve. The frontend treats 501 on this endpoint as terminal.
func (s *Server) flowStreamDisabled(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": "Flow Aggregator integration is not enabled for this Antrea UI instance (set flowAggregator.enabled in the Helm chart).",
	})
}

func (s *Server) LogError(sError *errors.ServerError, msg string, keysAndValues ...interface{}) {
	errors.LogError(s.logger, sError, msg, keysAndValues...)
}
