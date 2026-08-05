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

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// requestWithQuery is like Request, but path and query are kept separate so query values are
// percent-encoded correctly: Request's path argument becomes url.URL.Path verbatim, so a "?"
// embedded in it is escaped as part of the path rather than starting a query string.
func requestWithQuery(t *testing.T, path string, query url.Values, mutators ...func(req *http.Request)) *http.Response {
	t.Helper()
	u := &url.URL{Scheme: "http", Host: host, Path: path, RawQuery: query.Encode()}
	resp, err := RequestURLWithClient(t.Context(), http.DefaultClient, "GET", u, nil, mutators...)
	require.NoError(t, err)
	return resp
}

func hasResourceRule(rules []authorizationv1.ResourceRule, group, resource, verb string) bool {
	for _, r := range rules {
		if !containsOrWildcard(r.APIGroups, group) {
			continue
		}
		if !containsOrWildcard(r.Resources, resource) {
			continue
		}
		if containsOrWildcard(r.Verbs, verb) {
			return true
		}
	}
	return false
}

func containsOrWildcard(values []string, want string) bool {
	for _, v := range values {
		if v == "*" || v == want {
			return true
		}
	}
	return false
}

// createServiceAccountWithToken creates a ServiceAccount in ns and returns a bearer token for it.
func createServiceAccountWithToken(ctx context.Context, t *testing.T, ns, name string) string {
	t.Helper()
	_, err := k8sClient.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	tr, err := k8sClient.CoreV1().ServiceAccounts(ns).CreateToken(ctx, name, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
	require.NoError(t, err)
	return tr.Status.Token
}

// bindClusterRole creates a ClusterRoleBinding named name to the (possibly built-in)
// clusterRoleName, for subject. Cleaned up on test completion: unlike a namespaced RoleBinding,
// it is not swept up by deleting a test namespace.
func bindClusterRole(ctx context.Context, t *testing.T, name, clusterRoleName string, subject rbacv1.Subject) {
	t.Helper()
	_, err := k8sClient.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRoleName},
		Subjects:   []rbacv1.Subject{subject},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = k8sClient.RbacV1().ClusterRoleBindings().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
}

// createClusterRoleBinding creates a cluster-scoped ClusterRole and a ClusterRoleBinding to it,
// both named name, for subject. Both are cleaned up on test completion.
func createClusterRoleBinding(ctx context.Context, t *testing.T, name string, rules []rbacv1.PolicyRule, subject rbacv1.Subject) {
	t.Helper()
	_, err := k8sClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Rules:      rules,
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = k8sClient.RbacV1().ClusterRoles().Delete(context.Background(), name, metav1.DeleteOptions{})
	})
	bindClusterRole(ctx, t, name, name, subject)
}

// createRoleBinding creates a namespaced RoleBinding to a built-in ClusterRole (e.g. "view" or
// "edit"). No separate cleanup is needed: it lives in a test namespace the caller deletes.
func createRoleBinding(ctx context.Context, t *testing.T, ns, name, clusterRoleName string, subject rbacv1.Subject) {
	t.Helper()
	_, err := k8sClient.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRoleName},
		Subjects:   []rbacv1.Subject{subject},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// fetchAccessSummary performs the request and returns the parsed summary, or an error explaining
// why there is none. It never fails the test itself, which is what makes it usable from inside a
// require.Eventually condition: testify runs those in a separate goroutine, where a require failure
// is a bare runtime.Goexit — the condition would never report back, turning a transient failure into
// a full 30-second timeout and a misleading "Condition never satisfied".
//
// The status code is checked before the body is decoded: the documented failure mode here is a 503
// ("namespace discovery is not ready") whose body is an error payload, and decoding first would
// report a JSON type error instead of the status that explains it.
func fetchAccessSummary(ctx context.Context, token string, query url.Values) (apisv1.AccessSummary, error) {
	var summary apisv1.AccessSummary
	u := &url.URL{Scheme: "http", Host: host, Path: "api/v1/access-summary", RawQuery: query.Encode()}
	resp, err := RequestURLWithClient(ctx, http.DefaultClient, "GET", u, nil, setAccessTokenMutator(token))
	if err != nil {
		return summary, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return summary, fmt.Errorf("expected status %d, got %d: %s", http.StatusOK, resp.StatusCode, string(body))
	}
	// getResponseBody closes the body.
	if err := getResponseBody(resp, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func getAccessSummary(t *testing.T, token string, query url.Values) apisv1.AccessSummary {
	t.Helper()
	summary, err := fetchAccessSummary(t.Context(), token, query)
	require.NoError(t, err)
	return summary
}

// TestAccessSummary exercises GET /api/v1/access-summary against several identities with
// distinct permission shapes, so the assertions depend on RBAC rather than just "the endpoint
// answers something". Each subtest creates only the RBAC it needs and cleans it up.
func TestAccessSummary(t *testing.T) {
	t.Run("antrea-ui-admin", func(t *testing.T) {
		ctx := t.Context()
		// Bound to the real antrea-ui-admin ClusterRole (aggregated from
		// antrea-ui-admin-core): get antreacontrollerinfos, list/get antreaagentinfos, CRUD on
		// traceflows(/status), get on /featuregates, list namespaces. Its cluster-admin status
		// is not asserted either way, to avoid the test being brittle if a plugin's aggregated
		// ClusterRole ever adds more; what matters here is that its floor of permissions is
		// present.
		token, err := GetAccessToken(ctx, host)
		require.NoError(t, err)

		summary := getAccessSummary(t, token, nil)
		assert.True(t, strings.HasSuffix(summary.Username, authProviderSAName), "username %q should end with %q", summary.Username, authProviderSAName)
		assert.NotEmpty(t, summary.Groups)
		assert.Empty(t, summary.Namespace)
		assert.False(t, summary.Rules.Incomplete)
		assert.True(t, hasResourceRule(summary.Rules.ResourceRules, "crd.antrea.io", "antreaagentinfos", "list"))
		assert.True(t, hasResourceRule(summary.Rules.ResourceRules, "crd.antrea.io", "antreacontrollerinfos", "get"))
		assert.True(t, hasResourceRule(summary.Rules.ResourceRules, "crd.antrea.io", "traceflows", "create"))
		// This identity is bound to antrea-ui-admin with a ClusterRoleBinding, so it is the
		// subject of no RoleBinding and the resolver's scan would report nothing for it. It
		// gets ["*"] because antrea-ui-admin-core grants list on namespaces. A user bound to
		// that same role with their own identity must get the same answer by the same path —
		// this pins that, and would fail if the rule were dropped from the chart.
		assert.Equal(t, []string{"*"}, summary.Namespaces)

		t.Run("namespace scope", func(t *testing.T) {
			ctx := t.Context()
			ns, err := createTestNamespace(ctx)
			require.NoError(t, err)
			defer deleteNamespace(ctx, ns)

			scoped := getAccessSummary(t, token, url.Values{"namespace": {ns}})
			assert.Equal(t, ns, scoped.Namespace)
		})

		t.Run("invalid namespace", func(t *testing.T) {
			resp := requestWithQuery(t, "api/v1/access-summary", url.Values{"namespace": {"Not_Valid!"}}, setAccessTokenMutator(token))
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	})

	// A baseline identity with no RoleBinding or ClusterRoleBinding at all: nothing beyond the
	// default system:basic-user / system:discovery floor every authenticated identity gets, and
	// no accessible namespaces. This is the "sees nothing" case the whole feature exists to hide
	// UI for.
	t.Run("no permissions", func(t *testing.T) {
		ctx := t.Context()
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, ns)
		token := createServiceAccountWithToken(ctx, t, ns, "no-perms")

		summary := getAccessSummary(t, token, nil)
		assert.False(t, summary.ClusterAdmin)
		assert.Empty(t, summary.Namespaces)
		assert.False(t, hasResourceRule(summary.Rules.ResourceRules, "crd.antrea.io", "antreaagentinfos", "list"))
		assert.False(t, hasResourceRule(summary.Rules.ResourceRules, "crd.antrea.io", "traceflows", "create"))
	})

	// The correction the prototype got wrong: a wildcard ClusterRole not literally named
	// "cluster-admin" must still be reported as clusterAdmin. GetAccessSummary computes this
	// from a SelfSubjectAccessReview (verb/group/resource all "*"), not by matching the
	// ClusterRoleBinding's RoleRef.Name.
	t.Run("cluster-admin via wildcard ClusterRole", func(t *testing.T) {
		ctx := t.Context()
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, ns)
		token := createServiceAccountWithToken(ctx, t, ns, "wildcard-admin")

		name := randName("e2e-access-summary-wildcard-")
		createClusterRoleBinding(ctx, t, name,
			[]rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}},
			rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "wildcard-admin", Namespace: ns},
		)

		require.Eventually(t, func() bool {
			summary, err := fetchAccessSummary(ctx, token, nil)
			if err != nil {
				t.Logf("access-summary request failed, retrying: %v", err)
				return false
			}
			return summary.ClusterAdmin
		}, 30*time.Second, time.Second, "clusterAdmin should become true once RBAC propagates")
	})

	// Namespace discovery falls back to a cluster-wide RoleBinding watch when the caller cannot
	// list namespaces itself. This exercises that watch against a real apiserver (the Go unit
	// tests only exercise it against a fake clientset): two namespaced RoleBindings, in two
	// different namespaces, naming the same ServiceAccount subject.
	t.Run("namespaces via RoleBinding subjects", func(t *testing.T) {
		ctx := t.Context()
		homeNs, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, homeNs)
		otherNs, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, otherNs)

		token := createServiceAccountWithToken(ctx, t, homeNs, "edit-two-ns")
		subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "edit-two-ns", Namespace: homeNs}
		createRoleBinding(ctx, t, homeNs, "e2e-access-summary-edit", "edit", subject)
		createRoleBinding(ctx, t, otherNs, "e2e-access-summary-edit", "edit", subject)

		require.Eventually(t, func() bool {
			summary, err := fetchAccessSummary(ctx, token, nil)
			if err != nil {
				t.Logf("access-summary request failed, retrying: %v", err)
				return false
			}
			namespaces := summary.Namespaces
			sort.Strings(namespaces)
			expected := []string{homeNs, otherNs}
			sort.Strings(expected)
			return assert.ObjectsAreEqual(expected, namespaces)
		}, 30*time.Second, time.Second, "namespaces should list both RoleBinding namespaces once the watch syncs")
	})

	// The same namespace-discovery fallback, but through a Group subject rather than a direct
	// ServiceAccount subject — every ServiceAccount in a namespace automatically belongs to
	// "system:serviceaccounts:<namespace>", so binding that group is a common way to grant
	// access to "everyone in this namespace" without naming individual identities.
	t.Run("namespaces via group subject", func(t *testing.T) {
		ctx := t.Context()
		homeNs, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, homeNs)
		targetNs, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, targetNs)

		token := createServiceAccountWithToken(ctx, t, homeNs, "group-bound")
		createRoleBinding(ctx, t, targetNs, "e2e-access-summary-group", "view",
			rbacv1.Subject{Kind: rbacv1.GroupKind, Name: "system:serviceaccounts:" + homeNs})

		require.Eventually(t, func() bool {
			summary, err := fetchAccessSummary(ctx, token, nil)
			if err != nil {
				t.Logf("access-summary request failed, retrying: %v", err)
				return false
			}
			return assert.ObjectsAreEqual([]string{targetNs}, summary.Namespaces)
		}, 30*time.Second, time.Second, "namespaces should list the group-bound namespace once the watch syncs")
	})

	// A caller that can list namespaces cluster-wide (e.g. bound to the built-in "view"
	// ClusterRole) gets namespaces: ["*"] from a SelfSubjectAccessReview, without the
	// RoleBinding-scan fallback ever being consulted.
	t.Run("namespaces wildcard via list-namespaces permission", func(t *testing.T) {
		ctx := t.Context()
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(ctx, ns)
		token := createServiceAccountWithToken(ctx, t, ns, "cluster-viewer")

		name := randName("e2e-access-summary-clusterview-")
		bindClusterRole(ctx, t, name, "view",
			rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: "cluster-viewer", Namespace: ns},
		)

		require.Eventually(t, func() bool {
			summary, err := fetchAccessSummary(ctx, token, nil)
			if err != nil {
				t.Logf("access-summary request failed, retrying: %v", err)
				return false
			}
			return assert.ObjectsAreEqual([]string{"*"}, summary.Namespaces)
		}, 30*time.Second, time.Second, "namespaces should become [\"*\"] once RBAC propagates")
	})
}
