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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/rest"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	supportbundlehandler "antrea.io/antrea-ui/pkg/handlers/supportbundle"
	supportbundlehandlertesting "antrea.io/antrea-ui/pkg/handlers/supportbundle/testing"
	"antrea.io/antrea-ui/pkg/k8s"
)

// fakeSSARServer answers SelfSubjectAccessReviews, allowing the verbs in allowed, and records the
// resource attributes of every review.
type fakeSSARServer struct {
	*httptest.Server
	mutex   sync.Mutex
	allowed map[string]bool
	reviews []authorizationv1.ResourceAttributes
}

func newFakeSSARServer(t *testing.T, allowedVerbs ...string) *fakeSSARServer {
	f := &fakeSSARServer{allowed: map[string]bool{}}
	for _, verb := range allowedVerbs {
		f.allowed[verb] = true
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var review authorizationv1.SelfSubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil || review.Spec.ResourceAttributes == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		attrs := *review.Spec.ResourceAttributes
		f.mutex.Lock()
		f.reviews = append(f.reviews, attrs)
		allowed := f.allowed[attrs.Verb]
		f.mutex.Unlock()
		review.Status.Allowed = allowed
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSSARServer) lastReview() authorizationv1.ResourceAttributes {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if len(f.reviews) == 0 {
		return authorizationv1.ResourceAttributes{}
	}
	return f.reviews[len(f.reviews)-1]
}

type supportBundleTestServer struct {
	*testServer
	manager *supportbundlehandlertesting.MockManager
	ssar    *fakeSSARServer
}

// newSupportBundleTestServer builds a server with the support bundle API enabled. maxPerHour < 0
// disables rate limiting.
func newSupportBundleTestServer(t *testing.T, maxPerHour int, allowedVerbs ...string) *supportBundleTestServer {
	ts := newTestServer(t)
	ssar := newFakeSSARServer(t, allowedVerbs...)
	clientFactory, err := k8s.NewClientFactory(&rest.Config{
		Host:          ssar.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	}, http.DefaultTransport, session.TransportKeyK8s)
	require.NoError(t, err)
	ts.s.clientFactory = clientFactory
	manager := supportbundlehandlertesting.NewMockManager(gomock.NewController(t))
	ts.s.supportBundleManager = manager
	ts.s.config.MaxSupportBundlesPerHour = maxPerHour
	// The routes were registered by newTestServer with the feature disabled.
	ts.router = gin.New()
	ts.s.AddRoutes(&ts.router.RouterGroup)
	return &supportBundleTestServer{testServer: ts, manager: manager, ssar: ssar}
}

func (ts *supportBundleTestServer) do(method, path string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	return rr
}

var allSupportBundleVerbs = []string{"create", "list", "get", "delete"}

func testBundle(status apisv1.SupportBundleStatus) *apisv1.SupportBundle {
	createdAt := time.Date(2026, 10, 2, 13, 4, 5, 0, time.UTC)
	return &apisv1.SupportBundle{
		ID:        "abc",
		Status:    status,
		CreatedBy: "tester",
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(6 * time.Hour),
	}
}

func TestSupportBundleDisabled(t *testing.T) {
	ts := newTestServer(t)
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/supportbundle"},
		{"GET", "/api/v1/supportbundle"},
		{"GET", "/api/v1/supportbundle/abc"},
		{"GET", "/api/v1/supportbundle/abc/status"},
		{"GET", "/api/v1/supportbundle/abc/download"},
		{"DELETE", "/api/v1/supportbundle/abc"},
	} {
		req := httptest.NewRequest(route.method, route.path, nil)
		ts.authorizeRequest(req)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotImplemented, rr.Code, "%s %s", route.method, route.path)
	}
}

func TestSupportBundleRBACGate(t *testing.T) {
	for _, route := range []struct {
		method, path, verb string
		expect             func(m *supportbundlehandlertesting.MockManager)
		expectedCode       int
	}{
		{
			method: "POST", path: "/api/v1/supportbundle", verb: "create",
			expect: func(m *supportbundlehandlertesting.MockManager) {
				m.EXPECT().Create(gomock.Any(), gomock.Any()).Return(testBundle(apisv1.SupportBundleStatusCollecting), nil)
			},
			expectedCode: http.StatusAccepted,
		},
		{
			method: "GET", path: "/api/v1/supportbundle", verb: "list",
			expect:       func(m *supportbundlehandlertesting.MockManager) { m.EXPECT().List().Return(nil) },
			expectedCode: http.StatusOK,
		},
		{
			method: "GET", path: "/api/v1/supportbundle/abc", verb: "get",
			expect:       func(m *supportbundlehandlertesting.MockManager) {},
			expectedCode: http.StatusSeeOther,
		},
		{
			method: "GET", path: "/api/v1/supportbundle/abc/status", verb: "get",
			expect: func(m *supportbundlehandlertesting.MockManager) {
				m.EXPECT().Get("abc").Return(testBundle(apisv1.SupportBundleStatusCollecting), nil)
			},
			expectedCode: http.StatusOK,
		},
		{
			method: "GET", path: "/api/v1/supportbundle/abc/download", verb: "get",
			expect: func(m *supportbundlehandlertesting.MockManager) {
				m.EXPECT().Open("abc").Return(nil, nil, supportbundlehandler.ErrNotFound)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			method: "DELETE", path: "/api/v1/supportbundle/abc", verb: "delete",
			expect:       func(m *supportbundlehandlertesting.MockManager) { m.EXPECT().Delete("abc").Return(nil) },
			expectedCode: http.StatusOK,
		},
	} {
		t.Run(fmt.Sprintf("%s %s", route.method, route.path), func(t *testing.T) {
			// Every verb but the one this route needs: the manager must not be reached.
			var otherVerbs []string
			for _, verb := range allSupportBundleVerbs {
				if verb != route.verb {
					otherVerbs = append(otherVerbs, verb)
				}
			}
			ts := newSupportBundleTestServer(t, -1, otherVerbs...)
			rr := ts.do(route.method, route.path, nil)
			assert.Equal(t, http.StatusForbidden, rr.Code)
			assert.Equal(t, authorizationv1.ResourceAttributes{Verb: route.verb, Group: "ui.antrea.io", Resource: "supportbundles"}, ts.ssar.lastReview())

			ts = newSupportBundleTestServer(t, -1, route.verb)
			route.expect(ts.manager)
			rr = ts.do(route.method, route.path, nil)
			assert.Equal(t, route.expectedCode, rr.Code)
		})
	}
}

func TestCreateSupportBundle(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	bundle := testBundle(apisv1.SupportBundleStatusCollecting)
	ts.manager.EXPECT().Create("tester", &apisv1.SupportBundleRequest{Since: "1h"}).Return(bundle, nil)
	rr := ts.do("POST", "/api/v1/supportbundle", strings.NewReader(`{"since":"1h"}`))
	require.Equal(t, http.StatusAccepted, rr.Code)
	assert.Equal(t, "/api/v1/supportbundle/abc/status", rr.Header().Get("Location"))
	assert.Equal(t, "2", rr.Header().Get("Retry-After"))
	assert.Equal(t, "Location, Retry-After", rr.Header().Get("Access-Control-Expose-Headers"))
	var got apisv1.SupportBundle
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	assert.Equal(t, *bundle, got)

	// The body is optional.
	ts.manager.EXPECT().Create(gomock.Any(), &apisv1.SupportBundleRequest{}).Return(bundle, nil)
	rr = ts.do("POST", "/api/v1/supportbundle", nil)
	assert.Equal(t, http.StatusAccepted, rr.Code)

	rr = ts.do("POST", "/api/v1/supportbundle", strings.NewReader(`not json`))
	assert.Equal(t, http.StatusBadRequest, rr.Code)

	ts.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("%w: bad since", supportbundlehandler.ErrInvalidRequest))
	rr = ts.do("POST", "/api/v1/supportbundle", strings.NewReader(`{"since":"x"}`))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "bad since")

	ts.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("%w: too many", supportbundlehandler.ErrLimitReached))
	rr = ts.do("POST", "/api/v1/supportbundle", nil)
	assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	assert.Contains(t, rr.Body.String(), "too many")
}

func TestCreateSupportBundleRateLimit(t *testing.T) {
	ts := newSupportBundleTestServer(t, 1, "create")
	ts.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(testBundle(apisv1.SupportBundleStatusCollecting), nil)
	assert.Equal(t, http.StatusAccepted, ts.do("POST", "/api/v1/supportbundle", nil).Code)
	assert.Equal(t, http.StatusTooManyRequests, ts.do("POST", "/api/v1/supportbundle", nil).Code)
}

func TestCreateSupportBundleZeroRateLimitRefusesEveryRequest(t *testing.T) {
	ts := newSupportBundleTestServer(t, 0, "create")
	assert.Equal(t, http.StatusTooManyRequests, ts.do("POST", "/api/v1/supportbundle", nil).Code)
}

// The RBAC gate runs before the rate limiter: a caller who may not create bundles cannot use up
// the budget of those who may.
func TestCreateSupportBundleRateLimitIgnoresDeniedCallers(t *testing.T) {
	ts := newSupportBundleTestServer(t, 1)
	assert.Equal(t, http.StatusForbidden, ts.do("POST", "/api/v1/supportbundle", nil).Code)
	ts.ssar.mutex.Lock()
	ts.ssar.allowed["create"] = true
	ts.ssar.mutex.Unlock()
	ts.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(testBundle(apisv1.SupportBundleStatusCollecting), nil)
	assert.Equal(t, http.StatusAccepted, ts.do("POST", "/api/v1/supportbundle", nil).Code)
}

func TestListSupportBundles(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	ts.manager.EXPECT().List().Return(nil)
	rr := ts.do("GET", "/api/v1/supportbundle", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	// Never null.
	assert.JSONEq(t, `{"items":[]}`, rr.Body.String())

	ts.manager.EXPECT().List().Return([]apisv1.SupportBundle{*testBundle(apisv1.SupportBundleStatusCollected)})
	rr = ts.do("GET", "/api/v1/supportbundle", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	var list apisv1.SupportBundleList
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	assert.Equal(t, []apisv1.SupportBundle{*testBundle(apisv1.SupportBundleStatusCollected)}, list.Items)
}

func TestGetSupportBundleRedirectsToStatus(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	rr := ts.do("GET", "/api/v1/supportbundle/abc", nil)
	assert.Equal(t, http.StatusSeeOther, rr.Code)
	assert.Equal(t, "/api/v1/supportbundle/abc/status", rr.Header().Get("Location"))
}

func TestGetSupportBundleStatus(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	for _, tc := range []struct {
		status             apisv1.SupportBundleStatus
		expectedLocation   string
		expectedRetryAfter string
	}{
		{status: apisv1.SupportBundleStatusCollecting, expectedLocation: "/api/v1/supportbundle/abc/status", expectedRetryAfter: "2"},
		{status: apisv1.SupportBundleStatusCollected, expectedLocation: "/api/v1/supportbundle/abc/download"},
		{status: apisv1.SupportBundleStatusFailed},
	} {
		bundle := testBundle(tc.status)
		ts.manager.EXPECT().Get("abc").Return(bundle, nil)
		rr := ts.do("GET", "/api/v1/supportbundle/abc/status", nil)
		// Never a redirect, whatever the state.
		require.Equal(t, http.StatusOK, rr.Code, tc.status)
		assert.Equal(t, tc.expectedLocation, rr.Header().Get("Location"), tc.status)
		assert.Equal(t, tc.expectedRetryAfter, rr.Header().Get("Retry-After"), tc.status)
		var got apisv1.SupportBundle
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
		assert.Equal(t, *bundle, got)
	}

	ts.manager.EXPECT().Get("missing").Return(nil, supportbundlehandler.ErrNotFound)
	assert.Equal(t, http.StatusNotFound, ts.do("GET", "/api/v1/supportbundle/missing/status", nil).Code)
}

type fakeTarball struct {
	*bytes.Reader
	closed bool
}

func (f *fakeTarball) Close() error {
	f.closed = true
	return nil
}

func TestDownloadSupportBundle(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	content := []byte("0123456789")
	bundle := testBundle(apisv1.SupportBundleStatusCollected)

	tarball := &fakeTarball{Reader: bytes.NewReader(content)}
	ts.manager.EXPECT().Open("abc").Return(tarball, bundle, nil)
	rr := ts.do("GET", "/api/v1/supportbundle/abc/download", nil)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, content, rr.Body.Bytes())
	assert.Equal(t, "application/gzip", rr.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename="antrea-ui-supportbundle-20261002T130405Z.tar.gz"`, rr.Header().Get("Content-Disposition"))
	assert.Equal(t, "Content-Disposition", rr.Header().Get("Access-Control-Expose-Headers"))
	assert.True(t, tarball.closed)

	// Downloads can be resumed.
	ts.manager.EXPECT().Open("abc").Return(&fakeTarball{Reader: bytes.NewReader(content)}, bundle, nil)
	req := httptest.NewRequest("GET", "/api/v1/supportbundle/abc/download", nil)
	ts.authorizeRequest(req)
	req.Header.Set("Range", "bytes=4-")
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusPartialContent, rr.Code)
	assert.Equal(t, "456789", rr.Body.String())

	ts.manager.EXPECT().Open("abc").Return(nil, nil, supportbundlehandler.ErrNotCollected)
	rr = ts.do("GET", "/api/v1/supportbundle/abc/download", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), "/status")

	ts.manager.EXPECT().Open("missing").Return(nil, nil, supportbundlehandler.ErrNotFound)
	assert.Equal(t, http.StatusNotFound, ts.do("GET", "/api/v1/supportbundle/missing/download", nil).Code)
}

func TestDeleteSupportBundle(t *testing.T) {
	ts := newSupportBundleTestServer(t, -1, allSupportBundleVerbs...)
	ts.manager.EXPECT().Delete("abc").Return(nil)
	assert.Equal(t, http.StatusOK, ts.do("DELETE", "/api/v1/supportbundle/abc", nil).Code)
	ts.manager.EXPECT().Delete("missing").Return(supportbundlehandler.ErrNotFound)
	assert.Equal(t, http.StatusNotFound, ts.do("DELETE", "/api/v1/supportbundle/missing", nil).Code)
}
