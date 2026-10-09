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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/handlers/metricstap"
	metricstaptesting "antrea.io/antrea-ui/pkg/handlers/metricstap/testing"
	"antrea.io/antrea-ui/pkg/k8s"
	k8stesting "antrea.io/antrea-ui/pkg/k8s/testing"
	"antrea.io/antrea-ui/pkg/metrics"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

// fakeMetricsK8sAPIServer answers the SelfSubjectAccessReviews of the metrics routes, and records
// them. Like fakeAccessK8sAPIServer, it uses the in-memory network of httptest.NewTestServer.
type fakeMetricsK8sAPIServer struct {
	*httptest.Server

	mu sync.Mutex
	// allowedTargetGroups holds the names of the target groups the caller may read.
	allowedTargetGroups map[string]bool
	// backendMetricsAllowed is the answer for the /metrics nonResourceURL.
	backendMetricsAllowed bool
	// status, when set, is the status every review is answered with, in place of an answer.
	status int
	// reviews records the spec of every review received, in order.
	reviews []authorizationv1.SelfSubjectAccessReviewSpec
}

func newFakeMetricsK8sAPIServer(t *testing.T) *fakeMetricsK8sAPIServer {
	f := &fakeMetricsK8sAPIServer{allowedTargetGroups: map[string]bool{}}
	f.Server = httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "selfsubjectaccessreviews") {
			t.Errorf("unexpected request to fake K8s API server: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var review authorizationv1.SelfSubjectAccessReview
		_ = json.Unmarshal(body, &review)
		f.mu.Lock()
		f.reviews = append(f.reviews, review.Spec)
		status := f.status
		allowed := false
		switch {
		case review.Spec.ResourceAttributes != nil:
			allowed = f.allowedTargetGroups[review.Spec.ResourceAttributes.Name]
		case review.Spec.NonResourceAttributes != nil:
			allowed = f.backendMetricsAllowed
		}
		f.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(authorizationv1.SelfSubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed},
		})
	}))
	return f
}

func (f *fakeMetricsK8sAPIServer) allow(targetGroups ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, targetGroup := range targetGroups {
		f.allowedTargetGroups[targetGroup] = true
	}
}

func (f *fakeMetricsK8sAPIServer) revoke(targetGroup string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.allowedTargetGroups, targetGroup)
}

func (f *fakeMetricsK8sAPIServer) setStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakeMetricsK8sAPIServer) receivedReviews() []authorizationv1.SelfSubjectAccessReviewSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]authorizationv1.SelfSubjectAccessReviewSpec(nil), f.reviews...)
}

// reviewedTargetGroups returns the name of the target group of every review received, in order.
func (f *fakeMetricsK8sAPIServer) reviewedTargetGroups() []string {
	var targetGroups []string
	for _, review := range f.receivedReviews() {
		if review.ResourceAttributes != nil {
			targetGroups = append(targetGroups, review.ResourceAttributes.Name)
		}
	}
	return targetGroups
}

// newTestServerForMetrics builds a Server with metrics enabled: a mock manager for the taps, a
// real registry for the backend's own metrics, and a ClientFactory which talks to a fake K8s API
// server.
func newTestServerForMetrics(t *testing.T, options ...testServerOptions) (*testServer, *metricstaptesting.MockManager, *fakeMetricsK8sAPIServer) {
	ts := newTestServer(t, options...)
	fakeAPIServer := newFakeMetricsK8sAPIServer(t)
	config, fakeAPIServerTransport := k8stesting.InMemoryServerConfig(t, fakeAPIServer.Server)
	config.ContentConfig = rest.ContentConfig{ContentType: "application/json"}
	clientFactory, err := k8s.NewClientFactory(config, fakeAPIServerTransport, session.TransportKeyK8s)
	require.NoError(t, err)
	ts.s.clientFactory = clientFactory

	manager := metricstaptesting.NewMockManager(gomock.NewController(t))
	ts.s.metricsManager = manager
	ts.s.backendMetricsHandler = metrics.HandlerFor(metrics.New().Gatherer())
	// The routes depend on what is enabled, so they are registered again.
	router := gin.New()
	ts.s.AddRoutes(&router.RouterGroup)
	ts.router = router
	return ts, manager, fakeAPIServer
}

// doMetricsRequest sends a request as a user logged in with their own Kubernetes identity, and
// returns the recorded response.
func doMetricsRequest(ts *testServer, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ts.authorizeRequestAs(req, session.ModeToken)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	return rr
}

// errorMessageOf decodes the body of an error response, which is a JSON string.
func errorMessageOf(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var message string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &message), "body: %s", rr.Body.String())
	return message
}

func TestMetricsDisabled(t *testing.T) {
	// The default test server has no metrics manager and no metrics handler.
	ts := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/metrics/targets"},
		{"GET", "/api/v1/metrics/families?target=antrea-controller"},
		{"POST", "/api/v1/metrics/taps"},
		{"PUT", "/api/v1/metrics/taps/abc/selections"},
	} {
		rr := doMetricsRequest(ts, tc.method, tc.path, "{}")
		assert.Equal(t, http.StatusNotImplemented, rr.Code, "%s %s", tc.method, tc.path)
		assert.Contains(t, errorMessageOf(t, rr), "Metrics are not enabled")
	}

	rr := doMetricsRequest(ts, "GET", "/metrics", "")
	assert.Equal(t, http.StatusNotFound, rr.Code)

	var settings apisv1.FrontendSettings
	rr = doMetricsRequest(ts, "GET", "/api/v1/settings", "")
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &settings))
	assert.False(t, settings.Features.MetricsEnabled)
}

// The middleware which instruments requests is told about the streaming routes by their full path.
func TestStreamingRouteConstants(t *testing.T) {
	ts, _, _ := newTestServerForMetrics(t)
	routes := map[string]bool{}
	for _, route := range ts.router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	assert.Contains(t, routes, "POST "+MetricsTapsRoute)
	assert.Contains(t, routes, "GET "+FlowStreamRoute)
}

func TestGetMetricsTargets(t *testing.T) {
	statuses := []metricstap.TargetGroupStatus{
		{Name: "antrea-controller", Available: true},
		{Name: "antrea-agent", Available: true},
		{Name: "antrea-ui", Available: true},
		{Name: "flow-aggregator", Available: false, Reason: "Flow Aggregator metrics are not supported yet"},
	}

	t.Run("answers per target group", func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		// antrea-ui is not allowed: nothing is discovered for it.
		fakeAPIServer.allow("antrea-controller", "antrea-agent", "flow-aggregator")
		manager.EXPECT().TargetGroups().Return(statuses)
		manager.EXPECT().Targets(gomock.Any(), "antrea-controller").Return([]apisv1.MetricsTarget{{ID: "antrea-controller"}}, nil)
		manager.EXPECT().Targets(gomock.Any(), "antrea-agent").Return(nil, &metricstap.Error{
			Code:    metricstap.ErrorCodeUnavailable,
			Message: "failed to list Antrea agents",
			Err:     errors.New(`antreaagentinfos.crd.antrea.io is forbidden: User "system:serviceaccount:kube-system:antrea-ui" cannot list`),
		})

		rr := doMetricsRequest(ts, "GET", "/api/v1/metrics/targets", "")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.JSONEq(t, `{"targetGroups": [
			{"name": "antrea-controller", "allowed": true, "available": true, "targets": [{"id": "antrea-controller"}]},
			{"name": "antrea-agent", "allowed": true, "available": false, "reason": "failed to list Antrea agents", "targets": []},
			{"name": "antrea-ui", "allowed": false, "available": true, "targets": []},
			{"name": "flow-aggregator", "allowed": true, "available": false, "reason": "Flow Aggregator metrics are not supported yet", "targets": []}
		]}`, rr.Body.String())

		// One review per target group, for the virtual resource, named after the target group and
		// without a namespace: only a cluster-wide grant satisfies it.
		reviews := fakeAPIServer.receivedReviews()
		require.Len(t, reviews, len(statuses))
		for i, review := range reviews {
			assert.Equal(t, &authorizationv1.ResourceAttributes{
				Group:    "ui.antrea.io",
				Resource: "metrics",
				Verb:     "get",
				Name:     statuses[i].Name,
			}, review.ResourceAttributes)
			assert.Nil(t, review.NonResourceAttributes)
		}
	})

	t.Run("rejected credential ends the session", func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.setStatus(http.StatusUnauthorized)
		manager.EXPECT().TargetGroups().Return(statuses)

		req := httptest.NewRequest("GET", "/api/v1/metrics/targets", nil)
		cookie := ts.newSession(session.ModeToken)
		req.AddCookie(cookie)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		_, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		assert.Error(t, err, "the session must have been invalidated")
	})

	t.Run("review which fails", func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.setStatus(http.StatusInternalServerError)
		manager.EXPECT().TargetGroups().Return(statuses)

		rr := doMetricsRequest(ts, "GET", "/api/v1/metrics/targets", "")
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func TestGetMetricFamilies(t *testing.T) {
	const target = "antrea-agent/node-1"
	const path = "/api/v1/metrics/families?target=" + target
	invalidTarget := &metricstap.RequestError{Kind: metricstap.RequestErrorInvalid, Message: `invalid target "antrea-agent": expected antrea-agent/<instance>`}
	unknownTargetGroup := &metricstap.RequestError{Kind: metricstap.RequestErrorUnknownTargetGroup, Message: `unknown target group "kubelet"`}
	rawError := errors.New("dial tcp 192.0.2.10:10350: connect: connection refused")

	testCases := []struct {
		name string
		path string
		// expect sets what the manager is asked. A manager call which is not expected fails
		// the test, which is how the order of the checks is verified.
		expect func(manager *metricstaptesting.MockManager)
		// allowed is the target group the caller may read, if any.
		allowed string
		// expectedReviews is the number of access reviews the request must cost.
		expectedReviews int
		expectedCode    int
		expectedMessage string
	}{
		{
			name:            "missing target",
			path:            "/api/v1/metrics/families",
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "the target query parameter is required",
		},
		{
			name:            "empty target",
			path:            "/api/v1/metrics/families?target=",
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "the target query parameter is required",
		},
		{
			name:            "repeated target",
			path:            path + "&target=antrea-controller",
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "expected a single target, but 2 were given",
		},
		{
			name: "malformed target",
			path: "/api/v1/metrics/families?target=antrea-agent",
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget("antrea-agent").Return("", invalidTarget)
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: invalidTarget.Message,
		},
		{
			// A 404 which never reaches the review.
			name: "unknown target group",
			path: "/api/v1/metrics/families?target=kubelet",
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget("kubelet").Return("", unknownTargetGroup)
			},
			expectedCode:    http.StatusNotFound,
			expectedMessage: unknownTargetGroup.Message,
		},
		{
			// Refused before any scrape.
			name: "not allowed",
			path: path,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget(target).Return("antrea-agent", nil)
			},
			expectedReviews: 1,
			expectedCode:    http.StatusForbidden,
			expectedMessage: `not allowed to read the metrics of "antrea-agent": it requires "get" on metrics.ui.antrea.io with resource name "antrea-agent", granted cluster-wide`,
		},
		{
			name: "target group which is not available",
			path: "/api/v1/metrics/families?target=flow-aggregator",
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget("flow-aggregator").Return("flow-aggregator", nil)
				manager.EXPECT().Families(gomock.Any(), "flow-aggregator").Return(nil, &metricstap.Error{
					Code:    metricstap.ErrorCodeUnsupported,
					Message: "Flow Aggregator metrics are not supported yet",
				})
			},
			allowed:         "flow-aggregator",
			expectedReviews: 1,
			expectedCode:    http.StatusNotImplemented,
			expectedMessage: "Flow Aggregator metrics are not supported yet",
		},
		{
			name: "no agent on the Node",
			path: path,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget(target).Return("antrea-agent", nil)
				manager.EXPECT().Families(gomock.Any(), target).Return(nil, &metricstap.Error{
					Code:    metricstap.ErrorCodeNotFound,
					Message: `no Antrea agent on Node "node-1"`,
				})
			},
			allowed:         "antrea-agent",
			expectedReviews: 1,
			expectedCode:    http.StatusNotFound,
			expectedMessage: `no Antrea agent on Node "node-1"`,
		},
		{
			// The message names the class of the failure, never the dial error.
			name: "scrape failed",
			path: path,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget(target).Return("antrea-agent", nil)
				manager.EXPECT().Families(gomock.Any(), target).Return(nil, &metricstap.Error{
					Code:    metricstap.ErrorCodeUnreachable,
					Message: "failed to connect to the target",
					Err:     rawError,
				})
			},
			allowed:         "antrea-agent",
			expectedReviews: 1,
			expectedCode:    http.StatusBadGateway,
			expectedMessage: "failed to connect to the target",
		},
		{
			name: "failure which is not classified",
			path: path,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ResolveTarget(target).Return("antrea-agent", nil)
				manager.EXPECT().Families(gomock.Any(), target).Return(nil, rawError)
			},
			allowed:         "antrea-agent",
			expectedReviews: 1,
			expectedCode:    http.StatusBadGateway,
			expectedMessage: "the scrape failed",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, manager, fakeAPIServer := newTestServerForMetrics(t)
			if tc.allowed != "" {
				fakeAPIServer.allow(tc.allowed)
			}
			if tc.expect != nil {
				tc.expect(manager)
			}
			rr := doMetricsRequest(ts, "GET", tc.path, "")
			assert.Equal(t, tc.expectedCode, rr.Code)
			assert.Equal(t, tc.expectedMessage, errorMessageOf(t, rr))
			assert.Len(t, fakeAPIServer.receivedReviews(), tc.expectedReviews)
		})
	}

	t.Run("success", func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent")
		manager.EXPECT().ResolveTarget(target).Return("antrea-agent", nil)
		manager.EXPECT().Families(gomock.Any(), target).Return(&apisv1.MetricFamilyList{
			Target:    target,
			Timestamp: time.Date(2026, 10, 8, 17, 42, 10, 318_000_000, time.UTC),
			Families: []apisv1.MetricFamilyInfo{
				{Name: "antrea_agent_ovs_flow_count", Type: "gauge", Help: "Flow count.", LabelNames: []string{"table_id", "table_name"}, SeriesCount: 34},
				{Name: "go_goroutines", Type: "gauge", Help: "Number of goroutines.", LabelNames: []string{}, SeriesCount: 1},
			},
		}, nil)

		rr := doMetricsRequest(ts, "GET", path, "")
		require.Equal(t, http.StatusOK, rr.Code)
		assert.JSONEq(t, `{
			"target": "antrea-agent/node-1",
			"timestamp": "2026-10-08T17:42:10.318Z",
			"families": [
				{"name": "antrea_agent_ovs_flow_count", "type": "gauge", "help": "Flow count.", "labelNames": ["table_id", "table_name"], "seriesCount": 34},
				{"name": "go_goroutines", "type": "gauge", "help": "Number of goroutines.", "labelNames": [], "seriesCount": 1}
			]
		}`, rr.Body.String())
		assert.Equal(t, []string{"antrea-agent"}, fakeAPIServer.reviewedTargetGroups())
	})
}

const testTapRequest = `{"interval": "10s", "selections": [
	{"target": "antrea-agent/node-1", "metrics": ["antrea_agent_ovs_flow_count"]},
	{"target": "antrea-controller", "metrics": ["antrea_controller_network_policy_processed"]}
]}`

var testTapSelections = []apisv1.MetricsSelection{
	{Target: "antrea-agent/node-1", Metrics: []string{"antrea_agent_ovs_flow_count"}},
	{Target: "antrea-controller", Metrics: []string{"antrea_controller_network_policy_processed"}},
}

// The failures of POST /api/v1/metrics/taps before the stream is committed.
func TestOpenMetricsTapErrors(t *testing.T) {
	invalidSelections := &metricstap.RequestError{Kind: metricstap.RequestErrorInvalid, Message: "too many targets: a tap can select at most 20, but 21 were given"}
	testCases := []struct {
		name            string
		body            string
		expect          func(manager *metricstaptesting.MockManager)
		allowed         []string
		expectedReviews []string
		expectedCode    int
		expectedMessage string
	}{
		{
			name:            "malformed body",
			body:            `{"selections": "all of them"}`,
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "invalid request body",
		},
		{
			name: "interval out of range",
			body: `{"interval": "1s", "selections": []}`,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("1s").Return(time.Duration(0), &metricstap.RequestError{Kind: metricstap.RequestErrorInvalid, Message: "invalid interval 1s: must be between 5s and 5m0s"})
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "invalid interval 1s: must be between 5s and 5m0s",
		},
		{
			name: "invalid selections",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return(nil, invalidSelections)
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: invalidSelections.Message,
		},
		{
			// A 400 here, and not the 404 of GET /families: the target group is one item of the
			// request, not the resource it is about.
			name: "unknown target group in the selections",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return(nil, &metricstap.RequestError{Kind: metricstap.RequestErrorUnknownTargetGroup, Message: `unknown target group "kubelet"`})
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: `unknown target group "kubelet"`,
		},
		{
			name: "unavailable target group in the selections",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return(nil, &metricstap.RequestError{Kind: metricstap.RequestErrorUnavailableTargetGroup, Message: `invalid target "flow-aggregator": Flow Aggregator metrics are not supported yet`})
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: `invalid target "flow-aggregator": Flow Aggregator metrics are not supported yet`,
		},
		{
			// The grant for one target group is not a grant for the other: no tap is opened.
			name: "not allowed for one target group",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
			},
			allowed:         []string{"antrea-agent"},
			expectedReviews: []string{"antrea-agent", "antrea-controller"},
			expectedCode:    http.StatusForbidden,
			expectedMessage: `not allowed to read the metrics of "antrea-controller": it requires "get" on metrics.ui.antrea.io with resource name "antrea-controller", granted cluster-wide`,
		},
		{
			name: "too many taps",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
				manager.EXPECT().Open(gomock.Any(), gomock.Any()).Return("", nil, metricstap.ErrTooManyTaps)
			},
			allowed:         []string{"antrea-agent", "antrea-controller"},
			expectedReviews: []string{"antrea-agent", "antrea-controller"},
			expectedCode:    http.StatusTooManyRequests,
			expectedMessage: "too many metrics taps are open",
		},
		{
			name: "too many taps for the caller",
			body: testTapRequest,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
				manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
				manager.EXPECT().Open(gomock.Any(), gomock.Any()).Return("", nil, metricstap.ErrTooManyTapsForUser)
			},
			allowed:         []string{"antrea-agent", "antrea-controller"},
			expectedReviews: []string{"antrea-agent", "antrea-controller"},
			expectedCode:    http.StatusTooManyRequests,
			expectedMessage: "too many metrics taps are open for this user",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, manager, fakeAPIServer := newTestServerForMetrics(t)
			fakeAPIServer.allow(tc.allowed...)
			if tc.expect != nil {
				tc.expect(manager)
			}
			rr := doMetricsRequest(ts, "POST", "/api/v1/metrics/taps", tc.body)
			assert.Equal(t, tc.expectedCode, rr.Code)
			assert.Equal(t, tc.expectedMessage, errorMessageOf(t, rr))
			assert.Equal(t, tc.expectedReviews, fakeAPIServer.reviewedTargetGroups())
		})
	}
}

// sseEvent is one event of an SSE stream, or one comment: comment is then set and the rest is
// empty.
type sseEvent struct {
	name    string
	data    string
	comment string
}

// tapStream is an open tap stream, as a client sees it.
type tapStream struct {
	resp    *http.Response
	scanner *bufio.Scanner
	// cancel ends the request, as a client which goes away does.
	cancel context.CancelFunc
}

// next reads the next event or comment of the stream. ok is false when the stream has ended.
func (s *tapStream) next(t *testing.T) (sseEvent, bool) {
	t.Helper()
	var event sseEvent
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case line == "":
			if event != (sseEvent{}) {
				return event, true
			}
		case strings.HasPrefix(line, ":"):
			event.comment = strings.TrimSpace(strings.TrimPrefix(line, ":"))
		case strings.HasPrefix(line, "event:"):
			event.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			event.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		default:
			require.FailNow(t, "unexpected line in SSE stream", line)
		}
	}
	return sseEvent{}, false
}

// openTap sends a POST /api/v1/metrics/taps to a real server running ts.router, on the in-memory
// network: see openStreamAs for why. The request has no deadline, so a test must make sure the
// stream ends, or read from it with a tap which keeps sending keepalives.
func openTap(t *testing.T, ts *testServer, body string, authorize func(req *http.Request)) *tapStream {
	t.Helper()
	srv := httptest.NewTestServer(t, ts.router)
	client := srv.Client()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, "POST", srv.URL+"/api/v1/metrics/taps", strings.NewReader(body))
	require.NoError(t, err)
	authorize(req)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		resp.Body.Close()
	})
	return &tapStream{resp: resp, scanner: bufio.NewScanner(resp.Body), cancel: cancel}
}

// tapOpening is what the mock manager was asked when a tap was opened.
type tapOpening struct {
	ctx     context.Context
	options metricstap.TapOptions
}

// expectOpen makes the mock manager accept one tap, whose events are whatever the test sends on
// the returned channel. The other returned channel delivers what Open was called with.
func expectOpen(manager *metricstaptesting.MockManager, targetGroups ...string) (chan<- metricstap.Event, <-chan tapOpening) {
	events := make(chan metricstap.Event)
	opened := make(chan tapOpening, 1)
	manager.EXPECT().ParseInterval("10s").Return(10*time.Second, nil)
	manager.EXPECT().ValidateSelections(testTapSelections).Return(targetGroups, nil)
	manager.EXPECT().Open(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, options metricstap.TapOptions) (string, <-chan metricstap.Event, error) {
			opened <- tapOpening{ctx: ctx, options: options}
			return "9f2c41d07a3b4e18a6c05d7e91b2f344", events, nil
		})
	return events, opened
}

func bearer(token string) func(req *http.Request) {
	return func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func TestOpenMetricsTapStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent", "antrea-controller")
		events, opened := expectOpen(manager, "antrea-agent", "antrea-controller")

		stream := openTap(t, ts, testTapRequest, bearer("good"))
		require.Equal(t, http.StatusOK, stream.resp.StatusCode)
		assert.Equal(t, "text/event-stream", stream.resp.Header.Get("Content-Type"))
		assert.Equal(t, "no", stream.resp.Header.Get("X-Accel-Buffering"))

		opening := <-opened
		// A bearer request has no session: the tap belongs to the user.
		assert.Equal(t, "user:alice", opening.options.Owner)
		assert.Equal(t, "user:alice", opening.options.User)
		assert.Equal(t, 10*time.Second, opening.options.Interval)
		assert.Equal(t, testTapSelections, opening.options.Selections)
		require.NotNil(t, opening.options.Authorizer)
		// The gate, once per target group of the selection.
		assert.Equal(t, []string{"antrea-agent", "antrea-controller"}, fakeAPIServer.reviewedTargetGroups())

		// Events are relayed in order, under their own name.
		timestamp := time.Date(2026, 10, 8, 17, 43, 0, 4_000_000, time.UTC)
		events <- metricstap.Event{Name: metricstap.EventTap, Data: apisv1.MetricsTapEvent{
			ID: "9f2c41d07a3b4e18a6c05d7e91b2f344", Interval: "10s", Selections: testTapSelections,
		}}
		events <- metricstap.Event{Name: metricstap.EventScrape, Data: apisv1.MetricsScrapeEvent{
			Target:    "antrea-agent/node-1",
			Timestamp: timestamp,
			Families: []apisv1.MetricFamilySamples{{Name: "antrea_agent_ovs_flow_count", Type: "gauge", Samples: []apisv1.MetricSample{
				{Labels: map[string]string{"table_id": "0"}, Value: "5"},
			}}},
		}}
		events <- metricstap.Event{Name: metricstap.EventScrapeError, Data: apisv1.MetricsScrapeErrorEvent{
			Target: "antrea-controller", Timestamp: timestamp, Code: metricstap.ErrorCodeUnreachable, Message: "failed to connect to the target",
		}}

		event, ok := stream.next(t)
		require.True(t, ok)
		assert.Equal(t, "tap", event.name)
		assert.JSONEq(t, `{"id": "9f2c41d07a3b4e18a6c05d7e91b2f344", "interval": "10s", "selections": [
			{"target": "antrea-agent/node-1", "metrics": ["antrea_agent_ovs_flow_count"]},
			{"target": "antrea-controller", "metrics": ["antrea_controller_network_policy_processed"]}
		]}`, event.data)

		event, ok = stream.next(t)
		require.True(t, ok)
		assert.Equal(t, "scrape", event.name)
		// No "truncated", and no "name" for a sample which carries the name of its family.
		assert.JSONEq(t, `{"target": "antrea-agent/node-1", "timestamp": "2026-10-08T17:43:00.004Z", "families": [
			{"name": "antrea_agent_ovs_flow_count", "type": "gauge", "samples": [{"labels": {"table_id": "0"}, "value": "5"}]}
		]}`, event.data)

		event, ok = stream.next(t)
		require.True(t, ok)
		assert.Equal(t, "scrape_error", event.name)
		assert.JSONEq(t, `{"target": "antrea-controller", "timestamp": "2026-10-08T17:43:00.004Z", "code": "unreachable", "message": "failed to connect to the target"}`, event.data)

		// With nothing to send, the stream is kept alive.
		start := time.Now()
		event, ok = stream.next(t)
		require.True(t, ok)
		assert.Equal(t, sseEvent{comment: "keepalive"}, event)
		assert.Equal(t, metricsKeepAliveInterval, time.Since(start))

		// A terminal error is the last thing sent: the manager then ends the tap, and the
		// stream ends with it.
		events <- metricstap.Event{Name: metricstap.EventError, Data: apisv1.MetricsTapErrorEvent{
			Code: metricstap.TapErrorCodeForbidden, Message: "access to the selected metrics was revoked",
		}}
		close(events)
		event, ok = stream.next(t)
		require.True(t, ok)
		assert.Equal(t, "error", event.name)
		assert.JSONEq(t, `{"code": "forbidden", "message": "access to the selected metrics was revoked", "retryable": false}`, event.data)
		_, ok = stream.next(t)
		assert.False(t, ok, "the stream must end after a terminal error")

		// The context the tap was opened with ends with the request.
		synctest.Wait()
		assert.Error(t, opening.ctx.Err())
	})
}

func TestOpenMetricsTapEndsWithClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent", "antrea-controller")
		_, opened := expectOpen(manager, "antrea-agent", "antrea-controller")

		stream := openTap(t, ts, testTapRequest, bearer("good"))
		require.Equal(t, http.StatusOK, stream.resp.StatusCode)
		opening := <-opened
		assert.NoError(t, opening.ctx.Err())

		stream.cancel()
		synctest.Wait()
		assert.Error(t, opening.ctx.Err(), "a client which goes away must end its tap")
	})
}

// A tap keeps its session alive for as long as it is open, and ends with it.
func TestOpenMetricsTapSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent", "antrea-controller")
		_, opened := expectOpen(manager, "antrea-agent", "antrea-controller")

		cookie := ts.newSession(session.ModeToken)
		stream := openTap(t, ts, testTapRequest, func(req *http.Request) {
			req.AddCookie(cookie)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		})
		require.Equal(t, http.StatusOK, stream.resp.StatusCode)
		opening := <-opened
		assert.Equal(t, "session:"+cookie.Value+"/tester", opening.options.Owner)

		// Well past the idle timeout of the session, with no other request.
		for range int(2 * 30 * time.Minute / metricsKeepAliveInterval) {
			event, ok := stream.next(t)
			require.True(t, ok)
			require.Equal(t, sseEvent{comment: "keepalive"}, event)
		}
		_, err := ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err, "an open tap must keep its session alive")

		// The user logs out in another tab.
		ts.sessionStore.Delete(cookie.Value)
		start := time.Now()
		_, ok := stream.next(t)
		assert.False(t, ok, "the stream must end with the session")
		assert.Equal(t, metricsKeepAliveInterval, time.Since(start))
		synctest.Wait()
		assert.Error(t, opening.ctx.Err())
	})
}

// The Authorizer handed to the manager repeats the gate as the user who opened the tap.
func TestMetricsTapAuthorizer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent", "antrea-controller")
		_, opened := expectOpen(manager, "antrea-agent", "antrea-controller")

		cookie := ts.newSession(session.ModeToken)
		stream := openTap(t, ts, testTapRequest, func(req *http.Request) {
			req.AddCookie(cookie)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		})
		require.Equal(t, http.StatusOK, stream.resp.StatusCode)
		opening := <-opened
		authorize := opening.options.Authorizer
		targetGroups := []string{"antrea-agent", "antrea-controller"}

		require.NoError(t, authorize(opening.ctx, targetGroups))
		// Two reviews for the gate, two for the check.
		assert.Equal(t, []string{"antrea-agent", "antrea-controller", "antrea-agent", "antrea-controller"}, fakeAPIServer.reviewedTargetGroups())

		// No answer is not a denial.
		fakeAPIServer.setStatus(http.StatusInternalServerError)
		err := authorize(opening.ctx, targetGroups)
		require.Error(t, err)
		assert.NotErrorIs(t, err, metricstap.ErrForbidden)
		assert.NotErrorIs(t, err, metricstap.ErrUnauthenticated)
		fakeAPIServer.setStatus(0)

		fakeAPIServer.revoke("antrea-controller")
		assert.ErrorIs(t, authorize(opening.ctx, targetGroups), metricstap.ErrForbidden)
		// What is still granted is still allowed.
		assert.NoError(t, authorize(opening.ctx, []string{"antrea-agent"}))

		_, err = ts.sessionStore.Get(t.Context(), cookie.Value)
		require.NoError(t, err, "a denial must not end the session")

		// A rejected credential does.
		fakeAPIServer.setStatus(http.StatusUnauthorized)
		assert.ErrorIs(t, authorize(opening.ctx, targetGroups), metricstap.ErrUnauthenticated)
		_, err = ts.sessionStore.Get(t.Context(), cookie.Value)
		assert.Error(t, err, "the session must have been invalidated")
	})
}

func TestUpdateMetricsTapSelections(t *testing.T) {
	const tapID = "9f2c41d07a3b4e18a6c05d7e91b2f344"
	const path = "/api/v1/metrics/taps/" + tapID + "/selections"
	const body = `{"selections": [
		{"target": "antrea-agent/node-1", "metrics": ["antrea_agent_ovs_flow_count"]},
		{"target": "antrea-controller", "metrics": ["antrea_controller_network_policy_processed"]}
	]}`
	// The owner of a tap opened by the same caller: see doMetricsRequest, which logs in as
	// "tester" with a new session each time, hence gomock.Any() for the session ID.
	isOwner := gomock.Any()

	testCases := []struct {
		name            string
		body            string
		expect          func(manager *metricstaptesting.MockManager)
		allowed         []string
		expectedReviews []string
		expectedCode    int
		expectedMessage string
	}{
		{
			// Before anything else: what the request asks for must not change the answer for
			// a tap which is not the caller's.
			name: "tap which does not exist, with a malformed body",
			body: `not json`,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().HasTap(tapID, isOwner).Return(false)
			},
			expectedCode:    http.StatusNotFound,
			expectedMessage: "metrics tap not found",
		},
		{
			name: "malformed body",
			body: `not json`,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().HasTap(tapID, isOwner).Return(true)
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: "invalid request body",
		},
		{
			name: "invalid selections",
			body: body,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().HasTap(tapID, isOwner).Return(true)
				manager.EXPECT().ValidateSelections(testTapSelections).Return(nil, &metricstap.RequestError{Kind: metricstap.RequestErrorUnknownTargetGroup, Message: `unknown target group "kubelet"`})
			},
			expectedCode:    http.StatusBadRequest,
			expectedMessage: `unknown target group "kubelet"`,
		},
		{
			// An update goes through the gate again, for the new selection.
			name: "not allowed",
			body: body,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().HasTap(tapID, isOwner).Return(true)
				manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
			},
			allowed:         []string{"antrea-controller"},
			expectedReviews: []string{"antrea-agent"},
			expectedCode:    http.StatusForbidden,
			expectedMessage: `not allowed to read the metrics of "antrea-agent": it requires "get" on metrics.ui.antrea.io with resource name "antrea-agent", granted cluster-wide`,
		},
		{
			name: "tap which ended in the meantime",
			body: body,
			expect: func(manager *metricstaptesting.MockManager) {
				manager.EXPECT().HasTap(tapID, isOwner).Return(true)
				manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
				manager.EXPECT().UpdateSelections(tapID, isOwner, testTapSelections).Return(metricstap.ErrTapNotFound)
			},
			allowed:         []string{"antrea-agent", "antrea-controller"},
			expectedReviews: []string{"antrea-agent", "antrea-controller"},
			expectedCode:    http.StatusNotFound,
			expectedMessage: "metrics tap not found",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts, manager, fakeAPIServer := newTestServerForMetrics(t)
			fakeAPIServer.allow(tc.allowed...)
			tc.expect(manager)
			rr := doMetricsRequest(ts, "PUT", path, tc.body)
			assert.Equal(t, tc.expectedCode, rr.Code)
			assert.Equal(t, tc.expectedMessage, errorMessageOf(t, rr))
			assert.Equal(t, tc.expectedReviews, fakeAPIServer.reviewedTargetGroups())
		})
	}

	t.Run("success", func(t *testing.T) {
		ts, manager, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.allow("antrea-agent", "antrea-controller")
		// The owner must be the one a tap opened by the same caller has, and the same for the
		// lookup and for the update.
		var lookedUp string
		manager.EXPECT().HasTap(tapID, gomock.Any()).DoAndReturn(func(_, owner string) bool {
			lookedUp = owner
			return true
		})
		manager.EXPECT().ValidateSelections(testTapSelections).Return([]string{"antrea-agent", "antrea-controller"}, nil)
		manager.EXPECT().UpdateSelections(tapID, gomock.Any(), testTapSelections).DoAndReturn(func(_, owner string, _ []apisv1.MetricsSelection) error {
			assert.Equal(t, lookedUp, owner)
			return nil
		})

		req := httptest.NewRequest("PUT", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNoContent, rr.Code)
		assert.Empty(t, rr.Body.String())
		assert.Equal(t, "user:alice", lookedUp)
	})
}

// The routes which list targets, list families and open a tap share one budget per user, whatever
// the session, so that one user cannot make antrea-ui scrape at will with its own credentials:
// another user has a budget of their own, and static-admin sessions, which all authenticate as the
// same user, each have theirs.
func TestMetricsRoutesAreRateLimitedPerUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, manager, _ := newTestServerForMetrics(t, func(c *serverconfig.Config) {
			c.Metrics.MaxRequestsPerSecond = serverconfig.DefaultMetricsMaxRequestsPerSecond
		})
		manager.EXPECT().TargetGroups().Return(nil).AnyTimes()
		newSessionFor := func(username string) *http.Cookie {
			sess, err := ts.sessionStore.Create(&session.Spec{
				Mode:       session.ModeToken,
				Username:   username,
				Credential: session.Credential{Kind: session.KindBearer, Token: []byte("user-token")},
			})
			require.NoError(t, err)
			return &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()}
		}
		const targetsPath = "/api/v1/metrics/targets"
		// A request without a target is answered by the handler, without asking anything.
		const familiesPath = "/api/v1/metrics/families"
		do := func(cookie *http.Cookie, method, path, body string) int {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.AddCookie(cookie)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			return rr.Code
		}
		get := func(cookie *http.Cookie, path string) int {
			return do(cookie, "GET", path, "")
		}
		// A body which is not JSON is refused by the handler, without asking anything either.
		openTap := func(cookie *http.Cookie) int {
			return do(cookie, "POST", MetricsTapsRoute, "not JSON")
		}
		spendBudget := func(cookie *http.Cookie) {
			for i := range metricsRequestsBurst {
				switch i % 3 {
				case 0:
					require.Equal(t, http.StatusOK, get(cookie, targetsPath), "request %d is within the burst", i)
				case 1:
					require.Equal(t, http.StatusBadRequest, get(cookie, familiesPath), "request %d is within the burst", i)
				case 2:
					require.Equal(t, http.StatusBadRequest, openTap(cookie), "request %d is within the burst", i)
				}
			}
		}

		alice := newSessionFor("alice")
		spendBudget(alice)
		assert.Equal(t, http.StatusTooManyRequests, get(alice, targetsPath))
		assert.Equal(t, http.StatusTooManyRequests, get(alice, familiesPath), "the budget is shared")
		assert.Equal(t, http.StatusTooManyRequests, openTap(alice), "the budget is shared")
		assert.Equal(t, http.StatusTooManyRequests, get(newSessionFor("alice"), targetsPath), "another session of the same user spends the same budget")
		assert.Equal(t, http.StatusOK, get(newSessionFor("bob"), targetsPath), "another user has a budget of their own")

		admin1 := ts.newSession(session.ModeAdmin)
		admin2 := ts.newSession(session.ModeAdmin)
		spendBudget(admin1)
		assert.Equal(t, http.StatusTooManyRequests, get(admin1, targetsPath))
		assert.Equal(t, http.StatusOK, get(admin2, targetsPath), "static-admin sessions do not share a budget")

		time.Sleep(time.Second)
		assert.Equal(t, http.StatusOK, get(alice, targetsPath), "the budget refills")
	})
}

// A negative rate disables the limit, for the environments which need to: the e2e tests are one
// user.
func TestMetricsRoutesRateLimitDisabled(t *testing.T) {
	ts, manager, _ := newTestServerForMetrics(t, func(c *serverconfig.Config) {
		c.Metrics.MaxRequestsPerSecond = -1
	})
	manager.EXPECT().TargetGroups().Return(nil).AnyTimes()
	req := httptest.NewRequest("GET", "/api/v1/metrics/targets", nil)
	ts.authorizeRequestAs(req, session.ModeToken)
	for i := range 2 * metricsRequestsBurst {
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "request %d", i)
	}
}

func TestMetricsTapOwner(t *testing.T) {
	store := session.NewStore(testr.New(t), session.Options{IdleTimeout: time.Minute, MaxLifetime: time.Hour, MaxSessions: 10})
	newSession := func(mode session.Mode, username string) *session.RequestAuth {
		sess, err := store.Create(&session.Spec{
			Mode:       mode,
			Username:   username,
			Credential: session.Credential{Kind: session.KindBearer, Token: []byte("token")},
		})
		require.NoError(t, err)
		return session.NewSessionAuth(store, sess)
	}
	first, second := newSession(session.ModeToken, "alice"), newSession(session.ModeToken, "alice")
	// Two sessions of one user do not share taps, and neither do a session and a bearer
	// request.
	assert.NotEqual(t, metricsTapOwner(first), metricsTapOwner(second))
	assert.Equal(t, metricsTapOwner(first), metricsTapOwner(first))
	bearerAuth := session.NewEphemeralAuth(session.Credential{Kind: session.KindBearer, Token: []byte("token")}, "alice")
	assert.Equal(t, "user:alice", metricsTapOwner(bearerAuth))
	assert.NotEqual(t, metricsTapOwner(first), metricsTapOwner(bearerAuth))

	// They all count against the same user, for the cap on open taps.
	assert.Equal(t, "user:alice", metricsTapUser(first))
	assert.Equal(t, "user:alice", metricsTapUser(second))
	assert.Equal(t, "user:alice", metricsTapUser(bearerAuth))
	// Every static-admin session has the same username: each one counts on its own.
	admin1, admin2 := newSession(session.ModeAdmin, "admin"), newSession(session.ModeAdmin, "admin")
	assert.Equal(t, metricsTapOwner(admin1), metricsTapUser(admin1))
	assert.NotEqual(t, metricsTapUser(admin1), metricsTapUser(admin2))
	// So does a session without a username.
	anonymous := newSession(session.ModeToken, "")
	assert.Equal(t, metricsTapOwner(anonymous), metricsTapUser(anonymous))
}

// The backend's own metrics, for a Prometheus server.
func TestGetBackendMetrics(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		ts, _, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.backendMetricsAllowed = true
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		assert.Empty(t, fakeAPIServer.receivedReviews())
	})

	t.Run("not allowed", func(t *testing.T) {
		ts, _, fakeAPIServer := newTestServerForMetrics(t)
		// Being allowed to tap every target group through the UI is a different grant.
		fakeAPIServer.allow("antrea-ui", "antrea-controller", "antrea-agent")
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, errorMessageOf(t, rr), "/metrics nonResourceURL")
		assert.NotContains(t, rr.Body.String(), "antrea_ui_build_info")

		// The contract of the Antrea components themselves.
		reviews := fakeAPIServer.receivedReviews()
		require.Len(t, reviews, 1)
		assert.Equal(t, &authorizationv1.NonResourceAttributes{Path: "/metrics", Verb: "get"}, reviews[0].NonResourceAttributes)
		assert.Nil(t, reviews[0].ResourceAttributes)
	})

	t.Run("allowed", func(t *testing.T) {
		ts, _, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.backendMetricsAllowed = true
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
		assert.Contains(t, rr.Body.String(), "antrea_ui_build_info{")
		assert.Contains(t, rr.Body.String(), "go_goroutines ")
	})

	t.Run("review which fails", func(t *testing.T) {
		ts, _, fakeAPIServer := newTestServerForMetrics(t)
		fakeAPIServer.backendMetricsAllowed = true
		fakeAPIServer.setStatus(http.StatusInternalServerError)
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.Header.Set("Authorization", "Bearer good")
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.NotContains(t, rr.Body.String(), "antrea_ui_build_info")
	})

}

// Exposing the backend's metrics takes the setting: a gatherer alone is not enough, since the
// backend always has one. See TestMetricsDisabled for the 404 without the handler.
func TestBackendMetricsFollowConfiguration(t *testing.T) {
	gatherer := metrics.New().Gatherer()
	for _, enabled := range []bool{false, true} {
		config := &serverconfig.Config{}
		config.Metrics.Enabled = enabled
		s := NewServer(Options{Logger: testr.New(t), Config: config, MetricsGatherer: gatherer})
		assert.Equal(t, enabled, s.backendMetricsHandler != nil)
		assert.Equal(t, enabled, s.frontendSettings.Features.MetricsEnabled)
	}
}
