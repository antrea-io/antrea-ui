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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	antreasvchandlertesting "antrea.io/antrea-ui/pkg/handlers/antreasvc/testing"
	traceflowhandlertesting "antrea.io/antrea-ui/pkg/handlers/traceflow/testing"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
	"antrea.io/antrea-ui/pkg/plugins"
	"antrea.io/antrea-ui/pkg/server/authn"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

type testk8sProxyHandler struct {
	request *http.Request
}

func (h *testk8sProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.request = r
	b, err := httputil.DumpRequest(r, false)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	} else {
		w.Write(b)
	}
}

// fakeValidator stands in for the API server's SelfSubjectReview: every request that
// ts.authorizeRequest signs is accepted, since these tests are exercising route wiring, not
// credential validation (see pkg/server/authn for that).
type fakeValidator struct {
	username string
}

func (v *fakeValidator) ValidateCredential(_ context.Context, _ *session.Credential) (string, error) {
	return v.username, nil
}

type testServer struct {
	s                        *Server
	router                   *gin.Engine
	traceflowRequestsHandler *traceflowhandlertesting.MockRequestsHandler
	k8sProxyHandler          *testk8sProxyHandler
	antreaSvcRequestsHandler *antreasvchandlertesting.MockRequestsHandler
	passwordStore            *passwordtesting.MockStore
	sessionStore             session.Store
	pluginsClientset         *k8sfake.Clientset
}

type testServerOptions func(c *serverconfig.Config)

func setMaxTraceflowsPerHour(v int) testServerOptions {
	return func(c *serverconfig.Config) {
		c.Limits.MaxTraceflowsPerHour = v
	}
}

func newTestServer(t *testing.T, options ...testServerOptions) *testServer {
	logger := testr.New(t)
	ctrl := gomock.NewController(t)
	traceflowRequestsHandler := traceflowhandlertesting.NewMockRequestsHandler(ctrl)
	k8sProxyHandler := &testk8sProxyHandler{}
	antreaSvcRequestsHandler := antreasvchandlertesting.NewMockRequestsHandler(ctrl)
	passwordStore := passwordtesting.NewMockStore(ctrl)

	config := &serverconfig.Config{}
	// disable rate limiting by default
	config.Limits.MaxTraceflowsPerHour = -1
	for _, fn := range options {
		fn(config)
	}

	pluginsClientset := k8sfake.NewSimpleClientset()
	pluginRegistry := plugins.NewRegistry(logger, pluginsClientset, "antrea-ui", "ui.antrea.io/plugin=true")
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pluginRegistry.Run(stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		wg.Wait()
	})

	sessionStore := session.NewStore(logger, session.Options{})
	authenticator, err := authn.New(logger, authn.Config{
		Store:                 sessionStore,
		BearerFallbackEnabled: true,
		BearerValidator:       &fakeValidator{username: "alice"},
	})
	require.NoError(t, err)

	s := NewServer(Options{
		Logger:                   logger,
		Config:                   config,
		TraceflowRequestsHandler: traceflowRequestsHandler,
		K8sProxyHandler:          k8sProxyHandler,
		AntreaSvcRequestsHandler: antreaSvcRequestsHandler,
		FlowStreamSubscriber:     nil,
		PasswordStore:            passwordStore,
		PluginRegistry:           pluginRegistry,
		Authenticator:            authenticator,
	})
	router := gin.Default()
	s.AddRoutes(&router.RouterGroup)
	return &testServer{
		s:                        s,
		router:                   router,
		traceflowRequestsHandler: traceflowRequestsHandler,
		k8sProxyHandler:          k8sProxyHandler,
		antreaSvcRequestsHandler: antreaSvcRequestsHandler,
		pluginsClientset:         pluginsClientset,
		passwordStore:            passwordStore,
		sessionStore:             sessionStore,
	}
}

func (ts *testServer) authorizeRequest(req *http.Request) {
	token := fmt.Sprintf("token-%s", uuid.NewString())
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
}

// authorizeAdminRequest attaches a session cookie for a mode-4 (static admin password) session,
// for the one route (UpdatePassword) that is restricted to that mode specifically rather than to
// any authenticated caller.
func (ts *testServer) authorizeAdminRequest(t *testing.T, req *http.Request) {
	t.Helper()
	sess, err := ts.sessionStore.Create(&session.Spec{
		Mode:     session.ModeAdmin,
		Username: "admin",
		Credential: session.Credential{
			Kind:     session.KindImpersonate,
			UserName: "system:serviceaccount:kube-system:antrea-ui-admin",
		},
	})
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()})
}

// TestAuthorization ensures that all routes that are meant to be protected (i.e., can only be
// accessed with a valid credential) are indeed protected. If a route does not require one, it
// needs to be manually added to the unprotectedRoutes map below.
func TestAuthorization(t *testing.T) {
	unprotectedRoutes := map[string]bool{
		"GET /api/v1/version":                 true,
		"GET /api/v1/settings":                true,
		"GET /api/v1/plugins/index.json":      true,
		"GET /api/v1/plugins/:name/*filepath": true,
	}
	ts := newTestServer(t)
	for _, routeInfo := range ts.router.Routes() {
		routeStr := fmt.Sprintf("%s %s", routeInfo.Method, routeInfo.Path)
		if _, ok := unprotectedRoutes[routeStr]; ok {
			continue
		}
		req := httptest.NewRequest(routeInfo.Method, routeInfo.Path, nil)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equalf(t, http.StatusUnauthorized, rr.Code, "route (%s) should be protected by token but it is not", routeStr)
	}
}
