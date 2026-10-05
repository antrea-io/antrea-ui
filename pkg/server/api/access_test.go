// Copyright 2026 Antrea Authors.
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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/auth/session"
	accesshandler "antrea.io/antrea-ui/pkg/handlers/access"
	accesshandlertesting "antrea.io/antrea-ui/pkg/handlers/access/testing"
	"antrea.io/antrea-ui/pkg/k8s"
)

// fakeAccessK8sAPIServer answers the four self-review calls GetAccessSummary makes. It uses the
// in-memory network of httptest.NewTestServer and not a loopback socket, so that it can also be
// created in a testing/synctest bubble: see newTestServerForAccess for how clients reach it.
type fakeAccessK8sAPIServer struct {
	*httptest.Server
	username              string
	groups                []string
	rules                 authorizationv1.SubjectRulesReviewStatus
	clusterAdmin          bool
	listNamespacesAllowed bool
	// statusOverride forces a status code for calls whose path contains the given substring,
	// instead of the normal 201 response.
	statusOverride map[string]int
	// lastRulesNamespace records the namespace the SelfSubjectRulesReview was evaluated
	// against, for assertions.
	lastRulesNamespace string
	// accessReviews counts the SelfSubjectAccessReview calls received, including the ones
	// that statusOverride answers.
	accessReviews atomic.Int32
}

func newFakeAccessK8sAPIServer(t *testing.T) *fakeAccessK8sAPIServer {
	f := &fakeAccessK8sAPIServer{
		username:              "alice",
		groups:                []string{"system:authenticated"},
		listNamespacesAllowed: false,
		statusOverride:        map[string]int{},
	}
	f.Server = httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "selfsubjectaccessreviews") {
			f.accessReviews.Add(1)
		}
		for substr, code := range f.statusOverride {
			if strings.Contains(r.URL.Path, substr) {
				w.WriteHeader(code)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "selfsubjectreviews"):
			w.WriteHeader(http.StatusCreated)
			resp := map[string]any{
				"apiVersion": "authentication.k8s.io/v1",
				"kind":       "SelfSubjectReview",
				"status": map[string]any{
					"userInfo": map[string]any{
						"username": f.username,
						"groups":   f.groups,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.Contains(r.URL.Path, "selfsubjectrulesreviews"):
			body, _ := io.ReadAll(r.Body)
			var review authorizationv1.SelfSubjectRulesReview
			_ = json.Unmarshal(body, &review)
			f.lastRulesNamespace = review.Spec.Namespace
			w.WriteHeader(http.StatusCreated)
			resp := authorizationv1.SelfSubjectRulesReview{Status: f.rules}
			_ = json.NewEncoder(w).Encode(resp)
		case strings.Contains(r.URL.Path, "selfsubjectaccessreviews"):
			body, _ := io.ReadAll(r.Body)
			var review authorizationv1.SelfSubjectAccessReview
			_ = json.Unmarshal(body, &review)
			allowed := false
			if review.Spec.ResourceAttributes != nil {
				switch review.Spec.ResourceAttributes.Resource {
				case "namespaces":
					allowed = f.listNamespacesAllowed
				case "*":
					allowed = f.clusterAdmin
				}
			}
			w.WriteHeader(http.StatusCreated)
			resp := authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			b, _ := httputil.DumpRequest(r, true)
			t.Logf("unexpected request to fake K8s API server: %s", b)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	return f
}

// newTestServerForAccess builds a Server whose ClientFactory talks to a fake K8s API server, so
// GetAccessSummary's self-review calls can be tested end to end. Pass a nil accessResolver only
// for requests that fail before namespace discovery.
func newTestServerForAccess(t *testing.T, accessResolver *accesshandlertesting.MockResolver) (*testServer, *fakeAccessK8sAPIServer) {
	ts := newTestServer(t)
	fakeAPIServer := newFakeAccessK8sAPIServer(t)
	// Only the fake API server's own client can reach it, as it is not listening on a real
	// address. Its transport is used as is for impersonated requests, and the transports the
	// ClientFactory builds from the config have to dial the same way. That dial function
	// ignores the address, so Host can be a name that never resolves: if a transport ever
	// stopped dialing the fake API server, its requests would fail instead of reaching a real
	// host. Unlike a loopback address, such a Host is subject to the proxy configured in the
	// environment, if any, hence Proxy.
	fakeAPIServerTransport, ok := fakeAPIServer.Client().Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, fakeAPIServerTransport.DialContext)
	clientFactory, err := k8s.NewClientFactory(&rest.Config{
		Host:          "http://fake-apiserver.invalid",
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
		Dial:          fakeAPIServerTransport.DialContext,
		Proxy:         func(*http.Request) (*url.URL, error) { return nil, nil },
	}, fakeAPIServerTransport, session.TransportKeyK8s)
	require.NoError(t, err)
	ts.s.clientFactory = clientFactory
	if accessResolver != nil {
		ts.s.accessResolver = accessResolver
	}
	return ts, fakeAPIServer
}

func TestGetAccessSummaryClusterWideViewer(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = true
	fakeAPIServer.rules = authorizationv1.SubjectRulesReviewStatus{
		ResourceRules: []authorizationv1.ResourceRule{{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{""}, Resources: []string{"namespaces"}}},
	}

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []any{"*"}, summary["namespaces"])
	assert.Equal(t, "alice", summary["username"])
	assert.Equal(t, accesshandler.ClusterScopeProbeNamespace, fakeAPIServer.lastRulesNamespace)
	assert.NotContains(t, summary, "namespace")
}

func TestGetAccessSummaryNamespaceScoped(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	resolver.EXPECT().NamespacesFor("alice", []string{"system:authenticated"}).Return([]string{"ns-a", "ns-b"}, nil)
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = false

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []any{"ns-a", "ns-b"}, summary["namespaces"])
}

func TestGetAccessSummaryModeAdminSeesAllNamespaces(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = false

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeAdmin)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []any{"*"}, summary["namespaces"])
}

func TestGetAccessSummaryQueryNamespaceEchoedAndPassedToSSRR(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)

	req := httptest.NewRequest("GET", "/api/v1/access-summary?namespace=rbac-test-alpha", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, "rbac-test-alpha", summary["namespace"])
	assert.Equal(t, "rbac-test-alpha", fakeAPIServer.lastRulesNamespace)
}

func TestGetAccessSummaryInvalidQueryNamespace(t *testing.T) {
	ts, _ := newTestServerForAccess(t, nil)

	req := httptest.NewRequest("GET", "/api/v1/access-summary?namespace=Not_Valid!", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// The self-review APIs are granted to every authenticated identity by the default
// system:basic-user ClusterRole, so a 403 means the cluster stripped that default. There is no
// partial-success response for it: the endpoint fails, and the frontend fails open and retries.
func TestGetAccessSummaryForbiddenPropagates(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.statusOverride["selfsubjectrulesreviews"] = http.StatusForbidden

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestGetAccessSummaryUnauthorizedInvalidatesSession(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.statusOverride["selfsubjectreviews"] = http.StatusUnauthorized

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// An unresolvable namespace list is a 503, not a 200 carrying an empty list: [] is a legitimate
// answer ("subject of no RoleBinding"), so reporting it here would be false, and the frontend
// memoizes the summary for the session.
func TestGetAccessSummaryResolverErrorReturns503(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, assertError{})
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = false

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// A resolver that resolves to nothing is a real answer, and must still be an empty array rather
// than null: the frontend indexes into this field directly.
func TestGetAccessSummaryNoNamespacesIsEmptyArrayNotNull(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, nil)
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = false

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	// Not assert.Empty: that passes for null too.
	assert.Equal(t, []any{}, summary["namespaces"])
}

// The static-admin answer never comes from the resolver, so a broken resolver must not fail the
// request: the ModeAdmin session is the one used to recover a cluster whose RBAC is misconfigured.
func TestGetAccessSummaryModeAdminUnaffectedByResolverFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
	// Times(0): the point is that the resolver is never consulted at all, not merely that a
	// failure is tolerated.
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, assertError{}).Times(0)
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.listNamespacesAllowed = false

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeAdmin)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []any{"*"}, summary["namespaces"])
}

type assertError struct{}

func (assertError) Error() string { return "boom" }

func TestGetAccessSummaryClusterScopeProbeUnusable(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().ClusterScopeProbeUsable().Return(false)
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	ts, _ := newTestServerForAccess(t, resolver)

	req := httptest.NewRequest("GET", "/api/v1/access-summary", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	rules := summary["rules"].(map[string]any)
	assert.Equal(t, true, rules["incomplete"])
}

func TestGetAccessSummaryClusterScopeProbeIrrelevantForNamespacedQuery(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	// ClusterScopeProbeUsable must not even be consulted for a ?namespace= query.
	ts, _ := newTestServerForAccess(t, resolver)

	req := httptest.NewRequest("GET", "/api/v1/access-summary?namespace=rbac-test-alpha", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var summary map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	rules := summary["rules"].(map[string]any)
	assert.NotEqual(t, true, rules["incomplete"])
}
