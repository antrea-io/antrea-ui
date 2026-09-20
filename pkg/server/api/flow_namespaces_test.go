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
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	accesshandlertesting "antrea.io/antrea-ui/pkg/handlers/access/testing"
)

const flowNamespacesPath = "/api/v1/flows/namespaces"

// getFlowNamespaces makes one request with the given session cookie and returns the recorder.
func getFlowNamespaces(ts *testServer, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", flowNamespacesPath, nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	return rr
}

// decodeFlowNamespaces asserts a 200 and decodes the body.
func decodeFlowNamespaces(t *testing.T, rr *httptest.ResponseRecorder) apisv1.FlowNamespacesResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp apisv1.FlowNamespacesResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	return resp
}

func TestGetFlowNamespaces(t *testing.T) {
	testCases := []struct {
		name string
		// mode is how the caller authenticated. ModeAdmin is the built-in admin, served by
		// impersonating the antrea-ui-admin ServiceAccount.
		mode session.Mode
		// listNamespacesAllowed is whether the caller may list namespaces, which is what
		// makes the candidate list exhaustive.
		listNamespacesAllowed bool
		namespaceList         []string
		// resolverNamespaces is what the RoleBinding-based resolver answers, for a caller
		// who cannot list namespaces. nil means the resolver must not be consulted.
		resolverNamespaces []string
		flowsAllowed       map[string]bool
		expected           apisv1.FlowNamespacesResponse
		// expectedFlowReviews is how many flow reviews the call should cost, in total.
		expectedFlowReviews int
	}{
		{
			// The motivating case: a user who is a subject of RoleBindings in three
			// namespaces and may observe flows in two of them.
			name:               "namespace scoped user",
			mode:               session.ModeToken,
			resolverNamespaces: []string{"ns-a", "ns-b", "ns-c"},
			flowsAllowed:       map[string]bool{"ns-a": true, "ns-c": true},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces: []apisv1.FlowNamespaceAccess{
					{Namespace: "ns-a", CanObserve: true},
					{Namespace: "ns-b", CanObserve: false},
					{Namespace: "ns-c", CanObserve: true},
				},
				ClusterWide: false,
				// Derived from RoleBinding subjects, which under-reports by
				// construction: a namespace's absence here does not mean the user
				// cannot observe flows in it.
				Incomplete: true,
			},
			expectedFlowReviews: 4,
		},
		{
			// A caller who can list namespaces gets a complete candidate list, so
			// Incomplete is false - the one case where it can be.
			name:                  "namespace lister is complete",
			mode:                  session.ModeToken,
			listNamespacesAllowed: true,
			namespaceList:         []string{"kube-system", "default"},
			flowsAllowed:          map[string]bool{"default": true},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces: []apisv1.FlowNamespaceAccess{
					{Namespace: "default", CanObserve: true},
					{Namespace: "kube-system", CanObserve: false},
				},
				Incomplete: false,
			},
			expectedFlowReviews: 3,
		},
		{
			// A cluster-scoped grant authorizes the verb in every namespace, so every
			// candidate is observable and no per-namespace review is made.
			name:                  "cluster scoped user",
			mode:                  session.ModeToken,
			listNamespacesAllowed: true,
			namespaceList:         []string{"default", "kube-system"},
			flowsAllowed:          map[string]bool{"": true},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces: []apisv1.FlowNamespaceAccess{
					{Namespace: "default", CanObserve: true},
					{Namespace: "kube-system", CanObserve: true},
				},
				ClusterWide: true,
			},
			expectedFlowReviews: 1,
		},
		{
			// The built-in admin is answered without consulting the resolver, the same
			// way the access summary answers it.
			name:          "built-in admin",
			mode:          session.ModeAdmin,
			namespaceList: []string{"default"},
			flowsAllowed:  map[string]bool{"": true},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces:  []apisv1.FlowNamespaceAccess{{Namespace: "default", CanObserve: true}},
				ClusterWide: true,
			},
			expectedFlowReviews: 1,
		},
		{
			// No namespace in reach at all. An empty list is a real answer, not an
			// error: this user is a subject of no RoleBinding.
			name:               "no namespace in reach",
			mode:               session.ModeToken,
			resolverNamespaces: []string{},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces: []apisv1.FlowNamespaceAccess{},
				Incomplete: true,
			},
			expectedFlowReviews: 1,
		},
		{
			// Candidates exist but none is observable: also a 200, with the selector
			// left with nothing to offer.
			name:               "no observable namespace",
			mode:               session.ModeToken,
			resolverNamespaces: []string{"ns-a"},
			expected: apisv1.FlowNamespacesResponse{
				Namespaces: []apisv1.FlowNamespaceAccess{{Namespace: "ns-a", CanObserve: false}},
				Incomplete: true,
			},
			expectedFlowReviews: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			var resolver *accesshandlertesting.MockResolver
			if tc.resolverNamespaces != nil {
				resolver = accesshandlertesting.NewMockResolver(ctrl)
				resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return(tc.resolverNamespaces, nil).AnyTimes()
			}
			ts, fakeAPIServer := newTestServerForAccess(t, resolver)
			fakeAPIServer.listNamespacesAllowed = tc.listNamespacesAllowed
			fakeAPIServer.namespaceList = tc.namespaceList
			fakeAPIServer.flowsAllowed = tc.flowsAllowed

			resp := decodeFlowNamespaces(t, getFlowNamespaces(ts, ts.newSession(tc.mode)))
			assert.Equal(t, tc.expected, resp)
			assert.Equal(t, tc.expectedFlowReviews, fakeAPIServer.flowReviewCount())
		})
	}
}

// The verb has to be watch rather than list: the SSE stream always follows, and the Flow
// Aggregator grants list alone as history-only access, so a list-only namespace would be offered
// here and then fail to stream.
func TestGetFlowNamespacesReviewsWatchOnVirtualResource(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a"}, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)

	decodeFlowNamespaces(t, getFlowNamespaces(ts, ts.newSession(session.ModeToken)))

	fakeAPIServer.mutex.Lock()
	defer fakeAPIServer.mutex.Unlock()
	assert.Equal(t, "watch", fakeAPIServer.lastFlowReview.Verb)
	assert.Equal(t, "observability.antrea.io", fakeAPIServer.lastFlowReview.Group)
	assert.Equal(t, "flows", fakeAPIServer.lastFlowReview.Resource)
	assert.Empty(t, fakeAPIServer.lastFlowReview.Subresource)
	// The cluster-scoped review is made with an empty namespace, which is how the Flow
	// Aggregator authorizes a cluster-wide stream.
	assert.Equal(t, 1, fakeAPIServer.flowReviews[""])
	assert.Equal(t, 1, fakeAPIServer.flowReviews["ns-a"])
}

// Namespaces marshals as [] rather than null, because the frontend iterates it directly.
func TestGetFlowNamespacesNeverNull(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return(nil, nil).AnyTimes()
	ts, _ := newTestServerForAccess(t, resolver)

	rr := getFlowNamespaces(ts, ts.newSession(session.ModeToken))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, `{"namespaces":[],"clusterWide":false,"incomplete":true}`, rr.Body.String())
}

// A candidate list too long to review in full is truncated and reported as incomplete, rather
// than turning one request into thousands of reviews.
func TestGetFlowNamespacesTruncatesCandidates(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.listNamespacesAllowed = true
	for i := range maxFlowNamespaceCandidates + 10 {
		fakeAPIServer.namespaceList = append(fakeAPIServer.namespaceList, fmt.Sprintf("ns-%04d", i))
	}

	resp := decodeFlowNamespaces(t, getFlowNamespaces(ts, ts.newSession(session.ModeToken)))
	assert.Len(t, resp.Namespaces, maxFlowNamespaceCandidates)
	assert.True(t, resp.Incomplete)
	// Truncated from a sorted list, so the selector's options are stable rather than
	// whichever ones the API server happened to return first.
	assert.Equal(t, "ns-0000", resp.Namespaces[0].Namespace)
}

// A review that cannot be evaluated fails the request. An unevaluable review is not an allow, and
// reporting it as a denial would be indistinguishable, to the user, from a real one.
func TestGetFlowNamespacesFailsClosed(t *testing.T) {
	testCases := []struct {
		name           string
		failedReview   string
		statusOverride map[string]int
		expectedCode   int
	}{
		{
			name:         "cluster scoped review errors",
			failedReview: "",
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:         "namespaced review errors",
			failedReview: "ns-b",
			expectedCode: http.StatusInternalServerError,
		},
		{
			// A credential the API server no longer accepts ends the session rather
			// than being reported as "you may observe nothing".
			name:           "credential rejected",
			statusOverride: map[string]int{"selfsubjectaccessreviews": http.StatusUnauthorized},
			expectedCode:   http.StatusUnauthorized,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			resolver := accesshandlertesting.NewMockResolver(ctrl)
			resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a", "ns-b"}, nil).AnyTimes()
			ts, fakeAPIServer := newTestServerForAccess(t, resolver)
			fakeAPIServer.flowsAllowed = map[string]bool{"ns-a": true}
			if tc.statusOverride != nil {
				fakeAPIServer.statusOverride = tc.statusOverride
			} else {
				fakeAPIServer.flowsReviewError[tc.failedReview] = true
			}

			rr := getFlowNamespaces(ts, ts.newSession(session.ModeToken))
			assert.Equal(t, tc.expectedCode, rr.Code)
		})
	}
}

// A resolver that cannot answer is a 503, not an empty list: an empty list is a real answer, so
// substituting one for "I could not tell" would report something false.
func TestGetFlowNamespacesResolverUnavailable(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return(nil, assert.AnError).AnyTimes()
	ts, _ := newTestServerForAccess(t, resolver)

	rr := getFlowNamespaces(ts, ts.newSession(session.ModeToken))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// The answer is cached for the session's TTL, so the selector's option list does not cost a burst
// of reviews on every render.
func TestGetFlowNamespacesIsCachedPerSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a", "ns-b"}, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.flowsAllowed = map[string]bool{"ns-a": true}

	cookie := ts.newSession(session.ModeToken)
	first := decodeFlowNamespaces(t, getFlowNamespaces(ts, cookie))
	require.Equal(t, 3, fakeAPIServer.flowReviewCount())

	second := decodeFlowNamespaces(t, getFlowNamespaces(ts, cookie))
	assert.Equal(t, first, second)
	assert.Equal(t, 3, fakeAPIServer.flowReviewCount(), "the second call should have been served from the cache")

	// The cache is scoped to the session: another session is another set of permissions and
	// must never read this one's answer.
	decodeFlowNamespaces(t, getFlowNamespaces(ts, ts.newSession(session.ModeToken)))
	assert.Equal(t, 6, fakeAPIServer.flowReviewCount())
}

// An expired entry is re-evaluated rather than answering for the rest of the session: a grant
// added or revoked has to show up without logging out.
func TestGetFlowNamespacesCacheExpires(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a"}, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)

	cookie := ts.newSession(session.ModeToken)
	resp := decodeFlowNamespaces(t, getFlowNamespaces(ts, cookie))
	require.False(t, resp.Namespaces[0].CanObserve)

	// Expire the entry the way the TTL would, then grant the permission.
	ts.s.flowNamespaces.entries.Add(cookie.Value, resp, time.Nanosecond)
	time.Sleep(time.Millisecond)
	fakeAPIServer.flowsAllowed = map[string]bool{"ns-a": true}

	resp = decodeFlowNamespaces(t, getFlowNamespaces(ts, cookie))
	assert.True(t, resp.Namespaces[0].CanObserve)
}

// Concurrent first calls for one session collapse onto a single evaluation. Every caller here
// presents the same session's credential and asks the same question, which is what makes sharing
// one answer - and one failure - correct.
func TestGetFlowNamespacesSingleFlight(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a", "ns-b"}, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)
	fakeAPIServer.flowsAllowed = map[string]bool{"ns-a": true}
	// Hold the leader's first review open long enough for every follower to arrive and find
	// the flight in progress.
	fakeAPIServer.flowsReviewDelay = 200 * time.Millisecond

	cookie := ts.newSession(session.ModeToken)
	const callers = 8
	responses := make([]apisv1.FlowNamespacesResponse, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			responses[i] = decodeFlowNamespaces(t, getFlowNamespaces(ts, cookie))
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 3, fakeAPIServer.flowReviewCount(), "concurrent calls should have cost one evaluation")
	// One SelfSubjectReview too: the whole evaluation is shared, not just its reviews.
	fakeAPIServer.mutex.Lock()
	assert.Equal(t, 1, fakeAPIServer.selfSubjectRevs)
	fakeAPIServer.mutex.Unlock()
	for i := range callers {
		assert.Equal(t, responses[0], responses[i])
	}
}

// A request with no session has nothing to key a cache entry - or a single flight - by, so it pays
// for its own reviews. Sharing one key across session-less requests would collapse callers
// presenting different credentials onto one answer.
func TestGetFlowNamespacesBearerRequestIsNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a"}, nil).AnyTimes()
	ts, fakeAPIServer := newTestServerForAccess(t, resolver)

	for i := range 2 {
		req := httptest.NewRequest("GET", flowNamespacesPath, nil)
		req.Header.Set("Authorization", "Bearer some-k8s-token")
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		decodeFlowNamespaces(t, rr)
		assert.Equal(t, 2*(i+1), fakeAPIServer.flowReviewCount())
	}
}

// The endpoint answers whether or not Flow Aggregator integration is enabled: it is a question
// about the caller's Kubernetes RBAC and never talks to the Flow Aggregator.
func TestGetFlowNamespacesWithFlowStreamDisabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	resolver := accesshandlertesting.NewMockResolver(ctrl)
	resolver.EXPECT().NamespacesFor("alice", gomock.Any()).Return([]string{"ns-a"}, nil).AnyTimes()
	ts, _ := newTestServerForAccess(t, resolver)
	require.Nil(t, ts.s.flowStreamSSEHandler)

	decodeFlowNamespaces(t, getFlowNamespaces(ts, ts.newSession(session.ModeToken)))

	// And the stream itself still reports the integration as off.
	req := httptest.NewRequest("GET", "/api/v1/flows/stream", nil)
	req.AddCookie(ts.newSession(session.ModeToken))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusNotImplemented, rr.Code)
}
