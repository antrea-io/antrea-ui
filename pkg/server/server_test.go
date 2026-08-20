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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/k8s"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
	"antrea.io/antrea-ui/pkg/plugins"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

// testAdminUserName is the identity mode-4 sessions impersonate.
const testAdminUserName = "system:serviceaccount:kube-system:antrea-ui-admin"

type testServer struct {
	s             *Server
	router        *gin.Engine
	passwordStore *passwordtesting.MockStore
	sessionStore  session.Store
	// k8sAPIServer stands in for the kube-apiserver that credentials are validated against.
	k8sAPIServer *fakeK8sAPIServer
}

// fakeK8sAPIServer answers the SelfSubjectReview call that login uses to validate a credential.
type fakeK8sAPIServer struct {
	*httptest.Server
	// rejectTokens are the bearer tokens the API server should refuse.
	rejectTokens map[string]bool
	// rejectAll refuses every credential, for cases where the test cannot know the token
	// value up front (an id_token minted by the mock OIDC provider).
	rejectAll bool
	username  string
}

func newFakeK8sAPIServer(t *testing.T) *fakeK8sAPIServer {
	f := &fakeK8sAPIServer{rejectTokens: map[string]bool{}, username: "alice"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if auth := r.Header.Get("Authorization"); len(auth) > len("Bearer ") {
			token = auth[len("Bearer "):]
		}
		if f.rejectAll || f.rejectTokens[token] {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview","status":{"userInfo":{"username":"` + f.username + `"}}}`))
	}))
	t.Cleanup(f.Close)
	return f
}

type testServerOptions func(c *serverconfig.Config)

func setMaxLoginsPerSecond(v int) testServerOptions {
	return func(c *serverconfig.Config) {
		c.Limits.MaxLoginsPerSecond = v
	}
}

func enableOIDCAuth() testServerOptions {
	return func(c *serverconfig.Config) {
		c.Auth.OIDC.Enabled = true
		// validateConfig requires URL whenever OIDC is enabled, so a deployment always has
		// one here. It is also what decides which post-login redirect targets are accepted.
		c.URL = testServerAddr
	}
}

func disableBasicAuth() testServerOptions {
	return func(c *serverconfig.Config) {
		c.Auth.Basic.Enabled = false
	}
}

func enableKubeconfigAuth() testServerOptions {
	return func(c *serverconfig.Config) {
		c.Auth.Kubeconfig.Enabled = true
	}
}

func disableSATokenAuth() testServerOptions {
	return func(c *serverconfig.Config) {
		c.Auth.ServiceAccountToken.Enabled = false
	}
}

const testServerAddr = "http://localhost:8080"

func newTestServer(t *testing.T, options ...testServerOptions) *testServer {
	return newTestServerWithLogger(t, testr.New(t), options...)
}

func newTestServerWithLogger(t *testing.T, logger logr.Logger, options ...testServerOptions) *testServer {
	ctrl := gomock.NewController(t)
	passwordStore := passwordtesting.NewMockStore(ctrl)

	config := &serverconfig.Config{}
	// enable basic auth, the token login mode and the bearer API fallback by default
	config.Auth.Basic.Enabled = true
	config.Auth.ServiceAccountToken.Enabled = true
	config.Auth.BearerToken.Enabled = true
	config.Session.IdleTimeout = 30 * time.Minute
	config.Session.MaxLifetime = 12 * time.Hour
	config.Session.MaxSessions = 100
	// disable rate limiting by default
	config.Limits.MaxLoginsPerSecond = -1
	for _, fn := range options {
		fn(config)
	}

	var oidcProvider *OIDCProvider
	if config.Auth.OIDC.Enabled {
		t.Logf("Starting mock OIDC server")
		mockOIDC, err := mockoidc.Run()
		require.NoError(t, err, "failed to start mock OIDC server")
		t.Cleanup(func() { mockOIDC.Shutdown() })
		oidcConfig := mockOIDC.Config()
		provider, err := NewOIDCProvider(
			logger,
			testServerAddr,
			oidcConfig.Issuer,
			"", // discovery URL
			oidcConfig.ClientID,
			oidcConfig.ClientSecret,
			"", // logoutURL
			// mockoidc does not advertise offline_access, so we cannot use the default
			// scopes here.
			[]string{"openid", "groups"},
		)
		require.NoError(t, err, "failed to create OIDC provider")
		err = provider.Init(context.TODO())
		require.NoError(t, err, "failed to initialize OIDC provider")
		oidcProvider = provider
	}

	k8sAPIServer := newFakeK8sAPIServer(t)
	clientFactory, err := k8s.NewClientFactory(&rest.Config{Host: k8sAPIServer.URL}, http.DefaultTransport, session.TransportKeyK8s)
	require.NoError(t, err)

	sessionStore := session.NewStore(logger, session.Options{
		IdleTimeout: config.Session.IdleTimeout,
		MaxLifetime: config.Session.MaxLifetime,
		MaxSessions: config.Session.MaxSessions,
	})

	// we use nil for parameters which are only used by the API server
	s, err := NewServer(Options{
		Logger:         logger,
		Config:         config,
		PasswordStore:  passwordStore,
		SessionStore:   sessionStore,
		ClientFactory:  clientFactory,
		OIDCProvider:   oidcProvider,
		PluginRegistry: plugins.NewRegistry(logger, nil, "", "", 0, 0, 0),
		AdminUserName:  testAdminUserName,
	})
	require.NoError(t, err)
	router := gin.Default()
	s.AddRoutes(router)
	return &testServer{
		s:             s,
		router:        router,
		passwordStore: passwordStore,
		sessionStore:  sessionStore,
		k8sAPIServer:  k8sAPIServer,
	}
}

// sessionCookie returns the antrea-ui-session cookie from a response, or nil if there is none.
func sessionCookie(response *http.Response) *http.Cookie {
	for _, cookie := range response.Cookies() {
		if cookie.Name == cookieutils.SessionCookieName {
			return cookie
		}
	}
	return nil
}
