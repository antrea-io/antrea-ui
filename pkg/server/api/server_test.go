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
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	authtesting "antrea.io/antrea-ui/pkg/auth/testing"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	traceflowhandlertesting "antrea.io/antrea-ui/pkg/handlers/traceflow/testing"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
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

type testServer struct {
	s                        *Server
	router                   *gin.Engine
	k8sClient                *dynamicfake.FakeDynamicClient
	traceflowRequestsHandler *traceflowhandlertesting.MockRequestsHandler
	k8sProxyHandler          *testk8sProxyHandler
	passwordStore            *passwordtesting.MockStore
	tokenManager             *authtesting.MockTokenManager
}

type testServerOptions func(c *serverconfig.Config)

func setMaxTraceflowsPerHour(v int) testServerOptions {
	return func(c *serverconfig.Config) {
		c.Limits.MaxTraceflowsPerHour = v
	}
}

func newTestServer(t *testing.T, options ...testServerOptions) *testServer {
	logger := testr.New(t)
	scheme := runtime.NewScheme()
	k8sClient := dynamicfake.NewSimpleDynamicClient(scheme)
	ctrl := gomock.NewController(t)
	traceflowRequestsHandler := traceflowhandlertesting.NewMockRequestsHandler(ctrl)
	k8sProxyHandler := &testk8sProxyHandler{}
	passwordStore := passwordtesting.NewMockStore(ctrl)
	tokenManager := authtesting.NewMockTokenManager(ctrl)

	config := &serverconfig.Config{}
	// disable rate limiting by default
	config.Limits.MaxTraceflowsPerHour = -1
	for _, fn := range options {
		fn(config)
	}

	s := NewServer(
		logger,
		k8sClient,
		traceflowRequestsHandler,
		k8sProxyHandler,
		passwordStore,
		tokenManager,
		config,
	)
	router := gin.Default()
	s.AddRoutes(&router.RouterGroup)
	return &testServer{
		s:                        s,
		router:                   router,
		k8sClient:                k8sClient,
		traceflowRequestsHandler: traceflowRequestsHandler,
		k8sProxyHandler:          k8sProxyHandler,
		passwordStore:            passwordStore,
		tokenManager:             tokenManager,
	}
}

func (ts *testServer) authorizeRequest(req *http.Request) {
	token := fmt.Sprintf("token-%s", uuid.NewString())
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	ts.tokenManager.EXPECT().VerifyToken(token).Return(nil)
}

// TestAuthorization ensures that all routes that are meant to be protected (i.e., can only be
// accessed with a valid JWT token) are indeed protected. If a route does not require an access
// token, it needs to be manually added to the unprotectedRoutes map below.
func TestAuthorization(t *testing.T) {
	unprotectedRoutes := map[string]bool{
		"GET /api/v1/version":  true,
		"GET /api/v1/settings": true,
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
