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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/auth/session"
	accesshandler "antrea.io/antrea-ui/pkg/handlers/access"
	accesshandlertesting "antrea.io/antrea-ui/pkg/handlers/access/testing"
	"antrea.io/antrea-ui/pkg/k8s"
)

// fakeAccessK8sAPIServer answers the four self-review calls GetAccessSummary makes.
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

	// flowsAllowed maps a namespace to whether watch on flows.observability.antrea.io is
	// allowed there. The empty key is the cluster-scoped review.
	flowsAllowed map[string]bool
	// flowsReviewError names namespaces whose flow review fails instead of answering. The
	// empty key is the cluster-scoped review.
	flowsReviewError map[string]bool
	// flowsReviewDelay is applied to the cluster-scoped flow review, which is the first call
	// an evaluation makes, so a test can hold a single flight open.
	flowsReviewDelay time.Duration
	// namespaceList is what a namespace list returns.
	namespaceList []string

	// mutex guards the counters below, which concurrent flow reviews write to.
	mutex sync.Mutex
	// flowReviews counts flow reviews by namespace, and lastFlowReview records the resource
	// attributes of the most recent one.
	flowReviews     map[string]int
	lastFlowReview  authorizationv1.ResourceAttributes
	namespaceLists  int
	selfSubjectRevs int
}

// flowReviewCount reports how many flow reviews were made in total.
func (f *fakeAccessK8sAPIServer) flowReviewCount() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	total := 0
	for _, n := range f.flowReviews {
		total += n
	}
	return total
}

func newFakeAccessK8sAPIServer(t *testing.T) *fakeAccessK8sAPIServer {
	f := &fakeAccessK8sAPIServer{
		username:              "alice",
		groups:                []string{"system:authenticated"},
		listNamespacesAllowed: false,
		statusOverride:        map[string]int{},
		flowsAllowed:          map[string]bool{},
		flowsReviewError:      map[string]bool{},
		flowReviews:           map[string]int{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for substr, code := range f.statusOverride {
			if strings.Contains(r.URL.Path, substr) {
				w.WriteHeader(code)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "selfsubjectreviews"):
			f.mutex.Lock()
			f.selfSubjectRevs++
			f.mutex.Unlock()
			w.WriteHeader(http.StatusCreated)
			resp := map[string]interface{}{
				"apiVersion": "authentication.k8s.io/v1",
				"kind":       "SelfSubjectReview",
				"status": map[string]interface{}{
					"userInfo": map[string]interface{}{
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
			if attrs := review.Spec.ResourceAttributes; attrs != nil {
				switch attrs.Resource {
				case "flows":
					f.mutex.Lock()
					f.flowReviews[attrs.Namespace]++
					f.lastFlowReview = *attrs
					f.mutex.Unlock()
					if attrs.Namespace == "" && f.flowsReviewDelay > 0 {
						time.Sleep(f.flowsReviewDelay)
					}
					if f.flowsReviewError[attrs.Namespace] {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					allowed = f.flowsAllowed[attrs.Namespace]
				case "namespaces":
					allowed = f.listNamespacesAllowed
				case "*":
					allowed = f.clusterAdmin
				}
			}
			w.WriteHeader(http.StatusCreated)
			resp := authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api/v1/namespaces"):
			f.mutex.Lock()
			f.namespaceLists++
			f.mutex.Unlock()
			list := corev1.NamespaceList{}
			for _, ns := range f.namespaceList {
				list.Items = append(list.Items, corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(list)
		default:
			b, _ := httputil.DumpRequest(r, true)
			t.Logf("unexpected request to fake K8s API server: %s", b)
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// newTestServerForAccess builds a Server whose ClientFactory talks to a fake K8s API server, so
// GetAccessSummary's self-review calls can be tested end to end. Pass a nil accessResolver only
// for requests that fail before namespace discovery.
func newTestServerForAccess(t *testing.T, accessResolver *accesshandlertesting.MockResolver) (*testServer, *fakeAccessK8sAPIServer) {
	ts := newTestServer(t)
	fakeAPIServer := newFakeAccessK8sAPIServer(t)
	clientFactory, err := k8s.NewClientFactory(&rest.Config{
		Host:          fakeAPIServer.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	}, http.DefaultTransport, session.TransportKeyK8s)
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []interface{}{"*"}, summary["namespaces"])
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []interface{}{"ns-a", "ns-b"}, summary["namespaces"])
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []interface{}{"*"}, summary["namespaces"])
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

	var summary map[string]interface{}
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	// Not assert.Empty: that passes for null too.
	assert.Equal(t, []interface{}{}, summary["namespaces"])
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	assert.Equal(t, []interface{}{"*"}, summary["namespaces"])
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	rules := summary["rules"].(map[string]interface{})
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

	var summary map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &summary))
	rules := summary["rules"].(map[string]interface{})
	assert.NotEqual(t, true, rules["incomplete"])
}
