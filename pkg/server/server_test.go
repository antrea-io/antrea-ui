package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"antrea.io/antrea-ui/pkg/auth"
	authtesting "antrea.io/antrea-ui/pkg/auth/testing"
	traceflowhandlertesting "antrea.io/antrea-ui/pkg/handlers/traceflow/testing"
	passwordtesting "antrea.io/antrea-ui/pkg/password/testing"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

type testServer struct {
	s                        *server
	router                   *gin.Engine
	k8sClient                *dynamicfake.FakeDynamicClient
	traceflowRequestsHandler *traceflowhandlertesting.MockRequestsHandler
	passwordStore            *passwordtesting.MockStore
	tokenManager             *authtesting.MockTokenManager
}

func newTestServer(t *testing.T) *testServer {
	logger := testr.New(t)
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(agentInfoGVR.GroupVersion().WithKind("AntreaAgentInfoList"), &unstructured.UnstructuredList{})
	k8sClient := dynamicfake.NewSimpleDynamicClient(scheme)
	ctrl := gomock.NewController(t)
	traceflowRequestsHandler := traceflowhandlertesting.NewMockRequestsHandler(ctrl)
	passwordStore := passwordtesting.NewMockStore(ctrl)
	tokenManager := authtesting.NewMockTokenManager(ctrl)
	s := NewServer(logger, k8sClient, traceflowRequestsHandler, passwordStore, tokenManager)
	router := gin.Default()
	s.AddRoutes(router)
	return &testServer{
		s:                        s,
		router:                   router,
		k8sClient:                k8sClient,
		traceflowRequestsHandler: traceflowRequestsHandler,
		passwordStore:            passwordStore,
		tokenManager:             tokenManager,
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

func (ts *testServer) authorizeRequest(req *http.Request) {
	token := getTestToken().Raw
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	ts.tokenManager.EXPECT().VerifyToken(token).Return(nil)
}

// TestAuthorization ensures that all routes that are meant to be protected (i.e., can only be
// accessed with a valid JWT token) are indeed protected. If a route does not require an access
// token, it needs to be manually added to the unprotectedRoutes map below.
func TestAuthorization(t *testing.T) {
	unprotectedRoutes := map[string]bool{
		"GET /healthz":                   true,
		"GET /api/v1/auth/login":         true,
		"GET /api/v1/auth/refresh_token": true,
		"GET /api/v1/auth/logout":        true,
	}
	ts := newTestServer(t)
	for _, routeInfo := range ts.router.Routes() {
		routeStr := fmt.Sprintf("%s %s", routeInfo.Method, routeInfo.Path)
		if _, ok := unprotectedRoutes[routeStr]; ok {
			continue
		}
		req, err := http.NewRequest(routeInfo.Method, routeInfo.Path, nil)
		require.NoError(t, err)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equalf(t, http.StatusUnauthorized, rr.Code, "route (%s) should be protected by token but it not", routeStr)
	}
}
