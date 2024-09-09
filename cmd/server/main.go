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

package main

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"antrea.io/antrea-ui/pkg/auth"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/env"
	antreasvchandler "antrea.io/antrea-ui/pkg/handlers/antreasvc"
	"antrea.io/antrea-ui/pkg/handlers/k8sproxy"
	traceflowhandler "antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/k8s"
	"antrea.io/antrea-ui/pkg/password"
	passwordhasher "antrea.io/antrea-ui/pkg/password/hasher"
	passwordrw "antrea.io/antrea-ui/pkg/password/readwriter"
	"antrea.io/antrea-ui/pkg/server"
	"antrea.io/antrea-ui/pkg/signals"
	"antrea.io/antrea-ui/pkg/version"
)

var (
	config *serverconfig.Config
	logger logr.Logger
)

func ginLogger(logger logr.Logger, level int) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Start timer
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// Process request
		c.Next()

		stop := time.Now()
		latency := stop.Sub(start)
		if latency > time.Minute {
			latency = latency.Truncate(time.Second)
		}

		clientIP := c.ClientIP()
		method := c.Request.Method
		if raw != "" {
			path = path + "?" + raw
		}
		statusCode := c.Writer.Status()
		lastError := c.Errors.ByType(gin.ErrorTypePrivate).Last()
		errorMessage := ""
		if lastError != nil {
			errorMessage = lastError.Error()
		}

		keysAndValues := []interface{}{
			"code", statusCode,
			"client", clientIP,
			"method", method,
			"path", path,
			"latency", latency.String(),
		}
		if errorMessage != "" {
			keysAndValues = append(keysAndValues, "error", errorMessage)
		}

		logger.V(level).Info("GIN request", keysAndValues...)
	}
}

func run() error {
	logger.Info("Starting Antrea UI backend", "version", version.GetFullVersionWithRuntimeInfo())

	k8sRESTConfig, k8sHTTPClient, k8sDynamicClient, err := k8s.Client()
	if err != nil {
		return fmt.Errorf("failed to create K8s clients: %w", err)
	}
	k8sServerURL, err := url.Parse(k8sRESTConfig.Host)
	if err != nil {
		return fmt.Errorf("failed to parse K8s server URL '%s': %w", k8sRESTConfig.Host, err)
	}

	traceflowHandler := traceflowhandler.NewRequestsHandler(logger, k8sDynamicClient)
	k8sProxyHandler := k8sproxy.NewK8sProxyHandler(logger, k8sServerURL, k8sHTTPClient.Transport)

	antreaSvcHandler, err := antreasvchandler.NewRequestsHandler(logger, k8sRESTConfig, config.AntreaNamespace)
	if err != nil {
		return fmt.Errorf("failed to create handler for Antrea Service requests: %w", err)
	}

	var passwordStore password.Store
	if config.Auth.Basic.Enabled {
		store := password.NewStore(passwordrw.NewK8sSecret(env.GetNamespace(), "antrea-ui-passwd", k8sDynamicClient), passwordhasher.NewArgon2id())
		if err := store.Init(context.Background()); err != nil {
			return err
		}
		passwordStore = store
	}

	var jwtKey *rsa.PrivateKey
	if config.Auth.JWTKeyPath != "" {
		var err error
		if jwtKey, err = auth.LoadPrivateKeyFromFile(config.Auth.JWTKeyPath); err != nil {
			return fmt.Errorf("failed to load JWT key from file: %w", err)
		}
	} else {
		logger.Info("Generating RSA key for JWT")
		var err error
		if jwtKey, err = auth.GeneratePrivateKey(); err != nil {
			return fmt.Errorf("failed to generate JWT key: %w", err)
		}
	}
	tokenManager := auth.NewTokenManager("jwt-key", jwtKey)

	var oidcProvider *server.OIDCProvider
	if config.Auth.OIDC.Enabled {
		var err error
		oidcProvider, err = func() (*server.OIDCProvider, error) {
			provider, err := server.NewOIDCProvider(
				logger,
				config.URL,
				config.Auth.OIDC.IssuerURL,
				config.Auth.OIDC.DiscoveryURL,
				config.Auth.OIDC.ClientID,
				config.Auth.OIDC.ClientSecret,
				config.Auth.OIDC.LogoutURL,
			)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := provider.Init(ctx); err != nil {
				return nil, err
			}
			return provider, nil
		}()
		if err != nil {
			return err
		}
	}

	s := server.NewServer(
		logger,
		k8sDynamicClient,
		traceflowHandler,
		k8sProxyHandler,
		antreaSvcHandler,
		passwordStore,
		tokenManager,
		oidcProvider,
		config,
	)

	var router *gin.Engine
	if env.IsDevelopmentEnv() {
		router = gin.Default()
	} else {
		gin.SetMode(gin.ReleaseMode)
		router = gin.New()
		router.Use(ginLogger(logger, 2), gin.Recovery())
	}
	if env.IsDevelopmentEnv() {
		corsConfig := cors.DefaultConfig()
		corsConfig.AllowOrigins = []string{"http://localhost:3000"}
		corsConfig.AddAllowHeaders("Authorization")
		corsConfig.AllowCredentials = true
		router.Use(cors.New(corsConfig))
	}
	s.AddRoutes(router)

	srv := &http.Server{
		Addr:              config.Addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	stopCh := signals.RegisterSignalHandlers()

	go traceflowHandler.Run(stopCh)
	go antreaSvcHandler.Run(stopCh)
	go tokenManager.Run(stopCh)

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		logger.Info("Starting server", "address", config.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "Server error")
			os.Exit(1)
		}
	}()

	<-stopCh

	// The context is used to inform the server it has 5 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("server forced to shutdown: %w", err)
	}

	return nil
}

func main() {
	var err error
	config, err = serverconfig.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	zc := zap.NewProductionConfig()
	// #nosec G115: when parsing config, we ensure that LogVerbosity is >= 0 and < 128.
	zc.Level = zap.NewAtomicLevelAt(zapcore.Level(-1 * int8(config.LogVerbosity)))
	zc.DisableStacktrace = true
	zapLog, err := zc.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot initialize Zap logger: %v\n", err)
		os.Exit(1)
	}
	logger = zapr.NewLogger(zapLog)

	logger.V(2).Info("Config loaded", "config", config)

	if err := run(); err != nil {
		logger.Error(err, "error in run() function")
		os.Exit(1)
	}
}
