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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"k8s.io/client-go/dynamic"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
	"antrea.io/antrea-ui/pkg/auth"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/password"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/version"
)

type serverConfig struct {
	// keep all fields exported, so the config struct can be logged
	MaxTraceflowsPerHour int
}

type Server struct {
	logger                   logr.Logger
	k8sClient                dynamic.Interface
	traceflowRequestsHandler traceflow.RequestsHandler
	k8sProxyHandler          http.Handler
	passwordStore            password.Store
	tokenManager             auth.TokenManager
	config                   serverConfig
	frontendSettings         *apisv1alpha1.FrontendSettings
}

func NewServer(
	logger logr.Logger,
	k8sClient dynamic.Interface,
	traceflowRequestsHandler traceflow.RequestsHandler,
	k8sProxyHandler http.Handler,
	passwordStore password.Store,
	tokenManager auth.TokenManager,
	config *serverconfig.Config,
) *Server {
	c := serverConfig{
		MaxTraceflowsPerHour: config.Limits.MaxTraceflowsPerHour,
	}
	logger.Info("Created API server config", "config", c)
	return &Server{
		logger:                   logger,
		k8sClient:                k8sClient,
		traceflowRequestsHandler: traceflowRequestsHandler,
		k8sProxyHandler:          k8sProxyHandler,
		passwordStore:            passwordStore,
		tokenManager:             tokenManager,
		config:                   c,
		frontendSettings:         buildFrontendSettingsFromConfig(config),
	}
}

func (s *Server) checkBearerToken(c *gin.Context) {
	if sError := func() *errors.ServerError {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Missing Authorization header",
			}
		}
		t := strings.Split(auth, " ")
		if len(t) != 2 || t[0] != "Bearer" {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Authorization header does not have valid format",
			}
		}
		if err := s.tokenManager.VerifyToken(t[1]); err != nil {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Invalid Bearer token",
				Err:     err,
			}
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		c.Abort()
		return
	}
}

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
	s.AddTraceflowRoutes(apiv1)
	s.AddInfoRoutes(apiv1)
	s.AddAccountRoutes(apiv1)
	s.AddK8sRoutes(apiv1)
}

func (s *Server) LogError(sError *errors.ServerError, msg string, keysAndValues ...interface{}) {
	errors.LogError(s.logger, sError, msg, keysAndValues...)
}
