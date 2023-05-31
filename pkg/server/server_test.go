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
	"fmt"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"

	"antrea.io/antrea-ui/pkg/auth"
	authtesting "antrea.io/antrea-ui/pkg/auth/testing"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

type testServer struct {
	s             *Server
	router        *gin.Engine
	passwordStore *passwordtesting.MockStore
	tokenManager  *authtesting.MockTokenManager
}

type testServerOptions func(c *serverconfig.Config)

func setMaxLoginsPerSecond(v int) testServerOptions {
	return func(c *serverconfig.Config) {
		c.Limits.MaxLoginsPerSecond = v
	}
}

func newTestServer(t *testing.T, options ...testServerOptions) *testServer {
	logger := testr.New(t)
	ctrl := gomock.NewController(t)
	passwordStore := passwordtesting.NewMockStore(ctrl)
	tokenManager := authtesting.NewMockTokenManager(ctrl)

	config := &serverconfig.Config{}
	// disable rate limiting by default
	config.Limits.MaxLoginsPerSecond = -1
	for _, fn := range options {
		fn(config)
	}

	// we use nil for parameters which are only used by the API server
	s := NewServer(logger, nil, nil, nil, passwordStore, tokenManager, config)
	router := gin.Default()
	s.AddRoutes(router)
	return &testServer{
		s:             s,
		router:        router,
		passwordStore: passwordStore,
		tokenManager:  tokenManager,
	}
}

const testTokenValidity = 1 * time.Hour

func getTestToken() *auth.Token {
	return &auth.Token{
		Raw:       fmt.Sprintf("token-%s", uuid.NewString()),
		ExpiresIn: testTokenValidity,
		ExpiresAt: time.Now().Add(testTokenValidity),
	}
}
