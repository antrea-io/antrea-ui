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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	antreasvchandlertesting "antrea.io/antrea-ui/pkg/handlers/antreasvc/testing"
	traceflowhandlertesting "antrea.io/antrea-ui/pkg/handlers/traceflow/testing"
	"antrea.io/antrea-ui/pkg/k8s"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
	"antrea.io/antrea-ui/pkg/plugins"
	"antrea.io/antrea-ui/pkg/server/authn"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

const testAdminUserName = "system:serviceaccount:kube-system:antrea-ui-admin"

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
	traceflowRequestsHandler *traceflowhandlertesting.MockRequestsHandler
	k8sProxyHandler          *testk8sProxyHandler
	antreaSvcRequestsHandler *antreasvchandlertesting.MockRequestsHandler
	passwordStore            *passwordtesting.MockStore
	sessionStore             session.Store
	pluginsClientset         *k8sfake.Clientset
	credentialValidator      *fakeCredentialValidator
}

// fakeCredentialValidator stands in for the API server's SelfSubjectReview. Bearer tokens have no
// login step, so this is what the authenticator consults on every cache miss.
type fakeCredentialValidator struct {
	username string
	// rejected tokens get the answer a real API server gives for a credential it does not
	// accept; anything in failed gets a transient error instead.
	rejected map[string]bool
	failed   map[string]bool
}

func (v *fakeCredentialValidator) ValidateCredential(_ context.Context, cred *session.Credential) (string, error) {
	token := string(cred.Token)
	if v.failed[token] {
		return "", apierrors.NewServiceUnavailable("API server is having a bad day")
	}
	if v.rejected[token] {
		return "", apierrors.NewUnauthorized("invalid bearer token")
	}
	return v.username, nil
}

type testServerOptions func(c *serverconfig.Config)

func setMaxTraceflowsPerHour(v int) testServerOptions {
	return func(c *serverconfig.Config) {
		c.Limits.MaxTraceflowsPerHour = v
	}
}

func setServerURL(url string) testServerOptions {
	return func(c *serverconfig.Config) {
		c.URL = url
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
	config.Auth.Basic.Enabled = true
	config.Auth.Token.Enabled = true
	config.Auth.BearerToken.Enabled = true
	config.Session.IdleTimeout = 30 * time.Minute
	config.Session.MaxLifetime = 12 * time.Hour
	config.Session.MaxSessions = 100
	for _, fn := range options {
		fn(config)
	}

	pluginsClientset := k8sfake.NewSimpleClientset()
	pluginRegistry := plugins.NewRegistry(logger, pluginsClientset, "antrea-ui", "ui.antrea.io/plugin=true", 0, 0, 0)
	t.Cleanup(pluginRegistry.Close)
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

	sessionStore := session.NewStore(logger, session.Options{
		IdleTimeout: config.Session.IdleTimeout,
		MaxLifetime: config.Session.MaxLifetime,
		MaxSessions: config.Session.MaxSessions,
	})
	credentialValidator := &fakeCredentialValidator{
		username: "alice",
		rejected: map[string]bool{},
		failed:   map[string]bool{},
	}
	authenticator, err := authn.NewFromServerConfig(logger, config, sessionStore, credentialValidator)
	require.NoError(t, err)
	clientFactory, err := k8s.NewClientFactory(&rest.Config{Host: "https://127.0.0.1:6443"}, http.DefaultTransport, session.TransportKeyK8s)
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
		ClientFactory:            clientFactory,
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
		credentialValidator:      credentialValidator,
	}
}

// newSession registers a session in the store and returns its cookie.
func (ts *testServer) newSession(mode session.Mode) *http.Cookie {
	spec := &session.Spec{Mode: mode, Username: "tester"}
	if mode == session.ModeAdmin {
		spec.Credential = session.Credential{Kind: session.KindImpersonate, UserName: testAdminUserName}
	} else {
		spec.Credential = session.Credential{Kind: session.KindBearer, Token: []byte("user-token")}
	}
	sess, err := ts.sessionStore.Create(spec)
	if err != nil {
		panic(err)
	}
	return &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()}
}

// authorizeRequest makes req look like a request from a logged-in browser: a session cookie plus
// the same-origin Sec-Fetch-Site header a browser would attach.
func (ts *testServer) authorizeRequest(req *http.Request) {
	ts.authorizeRequestAs(req, session.ModeAdmin)
}

func (ts *testServer) authorizeRequestAs(req *http.Request, mode session.Mode) {
	req.AddCookie(ts.newSession(mode))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}

// TestAuthorization ensures that all routes that are meant to be protected are indeed protected.
// If a route does not require authentication, it needs to be manually added to the
// unprotectedRoutes map below.
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
		assert.Equalf(t, http.StatusUnauthorized, rr.Code, "route (%s) should require authentication but it does not", routeStr)
	}
}

// The session cookie is SameSite=Strict, but a cookie-authenticated request also has to pass an
// origin check: a page on another origin must not be able to drive the UI's API with the user's
// cookie.
func TestCSRFProtection(t *testing.T) {
	const path = "/api/v1/k8s/api/v1/pods"

	testCases := []struct {
		name         string
		serverURL    string
		headers      map[string]string
		expectedCode int
	}{
		{
			name:         "same-origin fetch",
			headers:      map[string]string{"Sec-Fetch-Site": "same-origin"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "user-initiated navigation",
			headers:      map[string]string{"Sec-Fetch-Site": "none"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "cross-site fetch",
			headers:      map[string]string{"Sec-Fetch-Site": "cross-site"},
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "same-site fetch from another port",
			headers:      map[string]string{"Sec-Fetch-Site": "same-site"},
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "foreign Origin, no Sec-Fetch-Site",
			headers:      map[string]string{"Origin": "https://evil.example.com"},
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "matching Origin, no Sec-Fetch-Site",
			headers:      map[string]string{"Origin": "http://example.com"},
			expectedCode: http.StatusOK,
		},
		{
			name:         "foreign Origin against a configured server URL",
			serverURL:    "https://antrea-ui.example.org",
			headers:      map[string]string{"Origin": "http://example.com"},
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "matching Origin against a configured server URL",
			serverURL:    "https://antrea-ui.example.org",
			headers:      map[string]string{"Origin": "https://antrea-ui.example.org"},
			expectedCode: http.StatusOK,
		},
		{
			// curl and friends send neither header, and are not a CSRF vector: a browser
			// is what would attach the cookie automatically.
			name:         "non-browser client",
			expectedCode: http.StatusOK,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var options []testServerOptions
			if tc.serverURL != "" {
				options = append(options, setServerURL(tc.serverURL))
			}
			ts := newTestServer(t, options...)
			req := httptest.NewRequest("GET", path, nil)
			// httptest.NewRequest defaults Host to example.com.
			req.AddCookie(ts.newSession(session.ModeAdmin))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			assert.Equal(t, tc.expectedCode, rr.Code)
		})
	}
}

// A Bearer header must never let a caller skip the CSRF gate: when a cookie is present, it wins.
func TestCookieWinsOverBearerHeader(t *testing.T) {
	ts := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/k8s/api/v1/pods", nil)
	req.AddCookie(ts.newSession(session.ModeAdmin))
	req.Header.Set("Authorization", "Bearer some-k8s-token")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

// A request with only an Authorization header is exempt from the origin check: a browser cannot
// attach that header cross-origin without the target approving a CORS preflight.
func TestBearerFallback(t *testing.T) {
	ts := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/v1/k8s/api/v1/pods", nil)
	req.Header.Set("Authorization", "Bearer some-k8s-token")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// The bearer fallback and the paste-a-token login mode accept the same credential but are separate
// exposures, and therefore separate flags: the login mode is a page a human uses, while the
// fallback is an authentication path on every API route, taken by clients that are not browsers and
// so are not covered by the cross-origin gate. Turning the fallback off must not require giving up
// the login mode.
func TestBearerFallbackCanBeDisabledIndependentlyOfTokenLogin(t *testing.T) {
	ts := newTestServer(t, func(c *serverconfig.Config) {
		c.Auth.BearerToken.Enabled = false
		// The login mode stays on.
		c.Auth.Token.Enabled = true
	})
	req := httptest.NewRequest("GET", "/api/v1/k8s/api/v1/pods", nil)
	req.Header.Set("Authorization", "Bearer some-k8s-token")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
