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
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	accesshandlertesting "antrea.io/antrea-ui/pkg/handlers/access/testing"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

const namespaceAccessPath = "/api/v1/access-summary/namespaces"

// namespaceAccessRequest is a request for the given Namespaces, from the session the cookie
// belongs to.
func namespaceAccessRequest(cookie *http.Cookie, names ...string) *http.Request {
	q := url.Values{"namespace": names}
	req := httptest.NewRequest("GET", namespaceAccessPath+"?"+q.Encode(), nil)
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return req
}

func serveNamespaceAccess(t *testing.T, ts *testServer, req *http.Request) (int, apisv1.NamespaceAccessSummaryList) {
	t.Helper()
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	var list apisv1.NamespaceAccessSummaryList
	if rr.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	}
	return rr.Code, list
}

// getNamespaceAccess asks about names as a new session.
func getNamespaceAccess(t *testing.T, ts *testServer, names ...string) (int, apisv1.NamespaceAccessSummaryList) {
	t.Helper()
	return serveNamespaceAccess(t, ts, namespaceAccessRequest(ts.newSession(session.ModeToken), names...))
}

func namespacesOf(list apisv1.NamespaceAccessSummaryList) []string {
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Namespace)
	}
	return names
}

func flowRules(verbs ...string) authorizationv1.SubjectRulesReviewStatus {
	return authorizationv1.SubjectRulesReviewStatus{
		ResourceRules: []authorizationv1.ResourceRule{{
			Verbs: verbs, APIGroups: []string{"observability.antrea.io"}, Resources: []string{"flows"},
		}},
	}
}

func (f *fakeAccessK8sAPIServer) reviewCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rulesNamespaces)
}

// Each Namespace named gets the rules of its own review, in the order they were named.
func TestGetNamespaceAccessSummariesReviewsEachNamedNamespace(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesByNamespace = map[string]authorizationv1.SubjectRulesReviewStatus{
		"ns-a": flowRules("watch"),
		"ns-b": {Incomplete: true},
	}

	code, list := getNamespaceAccess(t, ts, "ns-b", "ns-a")
	require.Equal(t, http.StatusOK, code)

	require.Equal(t, []string{"ns-b", "ns-a"}, namespacesOf(list))
	assert.True(t, list.Items[0].Rules.Incomplete, "the API server's own verdict is passed through")
	assert.Equal(t, flowRules("watch"), list.Items[1].Rules)
	assert.ElementsMatch(t, []string{"ns-a", "ns-b"}, fakeAPIServer.rulesNamespaces)
}

func TestGetNamespaceAccessSummariesAnswersOnceForANamespaceNamedTwice(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)

	code, list := getNamespaceAccess(t, ts, "ns-b", "ns-a", "ns-b")
	require.Equal(t, http.StatusOK, code)

	assert.Equal(t, []string{"ns-b", "ns-a"}, namespacesOf(list))
	assert.Equal(t, 2, fakeAPIServer.reviewCount())
}

// What is the same for every Namespace is not repeated for each.
func TestGetNamespaceAccessSummariesDoesNotRepeatIdentity(t *testing.T) {
	ts, _ := newTestServerForAccess(t, nil)

	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, namespaceAccessRequest(ts.newSession(session.ModeToken), "ns-a"))
	require.Equal(t, http.StatusOK, rr.Code)

	var raw struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &raw))
	require.Len(t, raw.Items, 1)
	var keys []string
	for k := range raw.Items[0] {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"namespace", "rules"}, keys)
}

// The caller says which Namespaces it is about, and at most as many as a selector has use for: one
// request is that many reviews.
func TestGetNamespaceAccessSummariesValidatesTheNamespaces(t *testing.T) {
	names := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("ns-%d", i))
		}
		return out
	}
	tests := []struct {
		name string
		ns   []string
		want int
	}{
		{"none", nil, http.StatusBadRequest},
		{"an empty one", []string{""}, http.StatusBadRequest},
		{"an invalid one", []string{"ns-a", "Not_Valid!"}, http.StatusBadRequest},
		{"too many", names(maxNamespaceAccessNames + 1), http.StatusBadRequest},
		{"as many as allowed", names(maxNamespaceAccessNames), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, fakeAPIServer := newTestServerForAccess(t, nil)
			code, _ := getNamespaceAccess(t, ts, tt.ns...)
			assert.Equal(t, tt.want, code)
			if tt.want == http.StatusBadRequest {
				assert.Zero(t, fakeAPIServer.reviewCount(), "a request that is refused makes no review")
			}
		})
	}
}

// Repeats do not count against the cap: they are not extra reviews.
func TestGetNamespaceAccessSummariesCapCountsDistinctNamespaces(t *testing.T) {
	ts, _ := newTestServerForAccess(t, nil)
	var ns []string
	for range maxNamespaceAccessNames + 5 {
		ns = append(ns, "ns-a")
	}
	code, list := getNamespaceAccess(t, ts, ns...)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"ns-a"}, namespacesOf(list))
}

// One Namespace that cannot be evaluated is unknown, not a reason to withhold the others.
func TestGetNamespaceAccessSummariesFailedReviewIsUnknown(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesByNamespace = map[string]authorizationv1.SubjectRulesReviewStatus{
		"ns-a": flowRules("watch"),
	}
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-b": http.StatusInternalServerError}

	code, list := getNamespaceAccess(t, ts, "ns-a", "ns-b")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, list.Items, 2)
	assert.Equal(t, flowRules("watch"), list.Items[0].Rules)
	assert.True(t, list.Items[1].Rules.Incomplete)
	assert.NotEmpty(t, list.Items[1].Rules.EvaluationError)
	assert.Empty(t, list.Items[1].Rules.ResourceRules)
}

// A 403 is not about one Namespace: the cluster stripped the self-review grant every authenticated
// identity has by default, which AccessSummary also reports as an error.
func TestGetNamespaceAccessSummariesForbiddenReviewFailsTheRequest(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-b": http.StatusForbidden}

	code, _ := getNamespaceAccess(t, ts, "ns-a", "ns-b")
	assert.Equal(t, http.StatusForbidden, code)
}

func TestGetNamespaceAccessSummariesUnauthorizedFailsTheRequest(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-a": http.StatusUnauthorized}

	code, _ := getNamespaceAccess(t, ts, "ns-a")
	assert.Equal(t, http.StatusUnauthorized, code)
}

// The first request of a burst pays for the reviews, and what follows within the TTL is answered
// from the cache. The test sleeps the real TTL, in virtual time: still cached just before it,
// evaluated again just after.
func TestGetNamespaceAccessSummariesIsCachedForTheTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts, fakeAPIServer := newTestServerForAccess(t, nil)
		cookie := ts.newSession(session.ModeToken)
		do := func() {
			code, _ := serveNamespaceAccess(t, ts, namespaceAccessRequest(cookie, "ns-a"))
			require.Equal(t, http.StatusOK, code)
		}

		do()
		require.Equal(t, 1, fakeAPIServer.reviewCount())

		time.Sleep(namespaceAccessTTL - time.Second)
		do()
		assert.Equal(t, 1, fakeAPIServer.reviewCount(), "still cached one second before the TTL")

		// An entry is still served at its expiry, and gone just after.
		time.Sleep(time.Second + time.Nanosecond)
		do()
		assert.Equal(t, 2, fakeAPIServer.reviewCount(), "evaluated again once the TTL has passed")
	})
}

// A cached review is one session's review of one Namespace: the same Namespaces in another order,
// or a subset of an earlier request, are answered from it, and only the misses are reviewed.
// Another session never reads it.
func TestGetNamespaceAccessSummariesCacheKeyIsSessionAndNamespace(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	mine := ts.newSession(session.ModeToken)
	other := ts.newSession(session.ModeToken)
	ask := func(cookie *http.Cookie, names ...string) []string {
		code, list := serveNamespaceAccess(t, ts, namespaceAccessRequest(cookie, names...))
		require.Equal(t, http.StatusOK, code)
		return namespacesOf(list)
	}

	assert.Equal(t, []string{"ns-a", "ns-b"}, ask(mine, "ns-a", "ns-b"))
	assert.Equal(t, 2, fakeAPIServer.reviewCount())

	assert.Equal(t, []string{"ns-b", "ns-a"}, ask(mine, "ns-b", "ns-a"))
	assert.Equal(t, 2, fakeAPIServer.reviewCount(), "another order is answered from the cache")

	assert.Equal(t, []string{"ns-a"}, ask(mine, "ns-a"))
	assert.Equal(t, 2, fakeAPIServer.reviewCount(), "a subset is answered from the cache")

	assert.Equal(t, []string{"ns-c", "ns-a"}, ask(mine, "ns-c", "ns-a"))
	assert.Equal(t, 3, fakeAPIServer.reviewCount(), "only the Namespace not seen before is reviewed")

	assert.Equal(t, []string{"ns-a", "ns-b"}, ask(other, "ns-a", "ns-b"))
	assert.Equal(t, 5, fakeAPIServer.reviewCount(), "another session never reads this session's answer")
}

// A Namespace reported as unknown is not cached: it would stay unknown for the whole TTL after a
// blip of a second. The next request reviews it again, while the Namespaces that were answered
// stay cached.
func TestGetNamespaceAccessSummariesDoesNotCacheUnknownNamespaces(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesByNamespace = map[string]authorizationv1.SubjectRulesReviewStatus{
		"ns-b": flowRules("watch"),
	}
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-b": http.StatusInternalServerError}
	cookie := ts.newSession(session.ModeToken)
	do := func() apisv1.NamespaceAccessSummaryList {
		code, list := serveNamespaceAccess(t, ts, namespaceAccessRequest(cookie, "ns-a", "ns-b"))
		require.Equal(t, http.StatusOK, code)
		return list
	}

	first := do()
	require.Len(t, first.Items, 2)
	assert.True(t, first.Items[1].Rules.Incomplete)
	assert.True(t, first.Items[1].EvaluationFailed)
	assert.False(t, first.Items[0].EvaluationFailed)
	assert.Equal(t, 2, fakeAPIServer.reviewCount())

	fakeAPIServer.mu.Lock()
	fakeAPIServer.rulesStatusByNamespace = nil
	fakeAPIServer.mu.Unlock()
	second := do()
	require.Len(t, second.Items, 2)
	assert.Equal(t, flowRules("watch"), second.Items[1].Rules)
	assert.False(t, second.Items[1].EvaluationFailed)
	assert.Equal(t, 3, fakeAPIServer.reviewCount(), "only the failed Namespace is reviewed again")

	// The complete answer is cached: no review for a third request.
	do()
	assert.Equal(t, 3, fakeAPIServer.reviewCount())
}

// A failed review is reported with empty rule lists, not null ones, as the type says.
func TestGetNamespaceAccessSummariesFailedReviewHasEmptyRuleLists(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-a": http.StatusInternalServerError}

	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, namespaceAccessRequest(ts.newSession(session.ModeToken), "ns-a"))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), `"resourceRules":[]`)
	assert.Contains(t, rr.Body.String(), `"nonResourceRules":[]`)
}

// The access routes share one budget per user, whatever the session: another session of the same
// user spends it too, another user has a budget of their own, and static-admin sessions, which all
// authenticate as the same user, each have theirs.
func TestAccessRoutesAreRateLimitedPerUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolver := accesshandlertesting.NewMockResolver(gomock.NewController(t))
		resolver.EXPECT().ClusterScopeProbeUsable().Return(true).AnyTimes()
		resolver.EXPECT().NamespacesFor(gomock.Any(), gomock.Any()).Return([]string{}, nil).AnyTimes()
		ts, _ := newTestServerForAccess(t, resolver)
		newSessionFor := func(username string) *http.Cookie {
			sess, err := ts.sessionStore.Create(&session.Spec{
				Mode:       session.ModeToken,
				Username:   username,
				Credential: session.Credential{Kind: session.KindBearer, Token: []byte("user-token")},
			})
			require.NoError(t, err)
			return &http.Cookie{Name: cookieutils.SessionCookieName, Value: sess.ID()}
		}
		const summaryPath = "/api/v1/access-summary?namespace=ns-a"
		get := func(cookie *http.Cookie, path string) int {
			req := httptest.NewRequest("GET", path, nil)
			req.AddCookie(cookie)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			rr := httptest.NewRecorder()
			ts.router.ServeHTTP(rr, req)
			return rr.Code
		}
		spendBudget := func(cookie *http.Cookie) {
			for i := range accessRequestsBurst {
				path := summaryPath
				if i%2 == 1 {
					path = namespaceAccessPath + "?namespace=ns-a"
				}
				require.Equal(t, http.StatusOK, get(cookie, path), "request %d is within the burst", i)
			}
		}

		alice := newSessionFor("alice")
		spendBudget(alice)
		assert.Equal(t, http.StatusTooManyRequests, get(alice, summaryPath))
		assert.Equal(t, http.StatusTooManyRequests, get(alice, namespaceAccessPath+"?namespace=ns-a"), "the budget is shared")
		assert.Equal(t, http.StatusTooManyRequests, get(newSessionFor("alice"), summaryPath), "another session of the same user spends the same budget")
		assert.Equal(t, http.StatusOK, get(newSessionFor("bob"), summaryPath), "another user has a budget of their own")

		admin1 := ts.newSession(session.ModeAdmin)
		admin2 := ts.newSession(session.ModeAdmin)
		spendBudget(admin1)
		assert.Equal(t, http.StatusTooManyRequests, get(admin1, summaryPath))
		assert.Equal(t, http.StatusOK, get(admin2, summaryPath), "static-admin sessions do not share a budget")

		time.Sleep(time.Second)
		assert.Equal(t, http.StatusOK, get(alice, summaryPath), "the budget refills")
	})
}

// Failures are not cached either: the next request after the condition clears must not be answered
// with the failure.
func TestGetNamespaceAccessSummariesDoesNotCacheFailure(t *testing.T) {
	ts, fakeAPIServer := newTestServerForAccess(t, nil)
	fakeAPIServer.rulesStatusByNamespace = map[string]int{"ns-a": http.StatusForbidden}
	cookie := ts.newSession(session.ModeToken)

	code, _ := serveNamespaceAccess(t, ts, namespaceAccessRequest(cookie, "ns-a"))
	assert.Equal(t, http.StatusForbidden, code)

	fakeAPIServer.mu.Lock()
	fakeAPIServer.rulesStatusByNamespace = nil
	fakeAPIServer.mu.Unlock()
	code, _ = serveNamespaceAccess(t, ts, namespaceAccessRequest(cookie, "ns-a"))
	assert.Equal(t, http.StatusOK, code)
}
