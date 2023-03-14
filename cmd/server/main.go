package main

import (
	"context"
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

	"antrea.io/antrea-ui/pkg/auth"
	"antrea.io/antrea-ui/pkg/env"
	traceflowhandler "antrea.io/antrea-ui/pkg/handlers/traceflow"
	"antrea.io/antrea-ui/pkg/k8s"
	"antrea.io/antrea-ui/pkg/password"
	passwordhasher "antrea.io/antrea-ui/pkg/password/hasher"
	passwordrw "antrea.io/antrea-ui/pkg/password/readwriter"
	"antrea.io/antrea-ui/pkg/server"
	"antrea.io/antrea-ui/pkg/signals"
)

var (
	serverAddr   string
	logger       logr.Logger
	jwtKeyPath   string
	cookieSecure bool
)

func run() error {
	k8sClient, err := k8s.DynamicClient()
	if err != nil {
		return fmt.Errorf("failed to create K8s dynamic client: %w", err)
	}

	traceflowHandler := traceflowhandler.NewRequestsHandler(logger, k8sClient)
	// passwordStore := password.NewStore(passwordrw.NewInMemory(), passwordhasher.NewArgon2id())
	passwordStore := password.NewStore(passwordrw.NewK8sSecret(env.GetNamespace(), "antrea-ui-passwd", k8sClient), passwordhasher.NewArgon2id())
	if err := passwordStore.Init(context.Background()); err != nil {
		return err
	}
	tokenManager := auth.NewTokenManager("key", auth.LoadPrivateKeyOrDie(jwtKeyPath))

	s := server.NewServer(
		logger,
		k8sClient,
		traceflowHandler,
		passwordStore,
		tokenManager,
		server.SetCookieSecure(cookieSecure),
	)
	if env.IsProductionEnv() {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.Default()
	if !env.IsProductionEnv() {
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

func main() {
	flag.StringVar(&serverAddr, "addr", ":8080", "Listening address for server")
	flag.StringVar(&jwtKeyPath, "jwt-key", "", "Path to PEM private key file to generate JWT tokens")
	flag.BoolVar(&cookieSecure, "cookie-secure", false, "Set the Secure attribute for authentication cookie, which requires HTTPS")
	flag.Parse()

	zc := zap.NewProductionConfig()
	if !env.IsProductionEnv() {
		zc.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	zc.DisableStacktrace = true
	zapLog, err := zc.Build()
	if err != nil {
		panic("Cannot initialize Zap logger")
	}
	logger = zapr.NewLogger(zapLog)
	if err := run(); err != nil {
		logger.Error(err, "error in run() function")
		os.Exit(1)
	}
}
