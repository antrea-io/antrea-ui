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
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"antrea.io/antrea-ui/pkg/auth"
	"antrea.io/antrea-ui/pkg/env"
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
	serverAddr           string
	logger               logr.Logger
	jwtKeyPath           string
	cookieSecure         bool
	maxTraceflowsPerHour int
	maxLoginsPerSecond   int
	verbosity            int
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

	k8sClient, err := k8s.DynamicClient()
	if err != nil {
		return fmt.Errorf("failed to create K8s dynamic client: %w", err)
	}

	traceflowHandler := traceflowhandler.NewRequestsHandler(logger, k8sClient)
	passwordStore := password.NewStore(passwordrw.NewK8sSecret(env.GetNamespace(), "antrea-ui-passwd", k8sClient), passwordhasher.NewArgon2id())
	if err := passwordStore.Init(context.Background()); err != nil {
		return err
	}
	var jwtKey *rsa.PrivateKey
	if jwtKeyPath != "" {
		var err error
		if jwtKey, err = auth.LoadPrivateKeyFromFile(jwtKeyPath); err != nil {
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

	s := server.NewServer(
		logger,
		k8sClient,
		traceflowHandler,
		passwordStore,
		tokenManager,
		server.SetCookieSecure(cookieSecure),
		server.SetMaxLoginsPerSecond(maxLoginsPerSecond),
		server.SetMaxTraceflowsPerHour(maxTraceflowsPerHour),
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
		Addr:              serverAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	stopCh := signals.RegisterSignalHandlers()

	go traceflowHandler.Run(stopCh)
	go tokenManager.Run(stopCh)

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		logger.Info("Starting server", "address", serverAddr)
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

func validateArgs() error {
	if verbosity < 0 || verbosity >= 128 {
		return fmt.Errorf("invalid verbosity level %d: it should be >= 0 and < 128", verbosity)
	}
	return nil
}

func main() {
	flag.StringVar(&serverAddr, "addr", ":8080", "Listening address for server")
	flag.StringVar(&jwtKeyPath, "jwt-key", "", "Path to PEM private key file to generate JWT tokens; if omitted one will be automatically generated")
	flag.BoolVar(&cookieSecure, "cookie-secure", false, "Set the Secure attribute for authentication cookie, which requires HTTPS")
	flag.IntVar(&maxTraceflowsPerHour, "max-traceflows-per-hour", 100, "Rate limit the number of Traceflow requests (across all clients); use -1 to remove rate-limiting")
	flag.IntVar(&maxLoginsPerSecond, "max-logins-per-second", 1, "Rate limit the number of login attempts (per client IP); use -1 to remove rate-limiting")
	flag.IntVar(&verbosity, "v", 0, "Log verbosity")
	flag.Parse()

	if err := validateArgs(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid args: %v\n", err.Error())
		os.Exit(1)
	}

	zc := zap.NewProductionConfig()
	zc.Level = zap.NewAtomicLevelAt(zapcore.Level(-1 * verbosity))
	zc.DisableStacktrace = true
	zapLog, err := zc.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot initialize Zap logger: %v\n", err)
		os.Exit(1)
	}
	logger = zapr.NewLogger(zapLog)
	if err := run(); err != nil {
		logger.Error(err, "error in run() function")
		os.Exit(1)
	}
}
