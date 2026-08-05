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

package access

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func roleBinding(name, namespace string, subjects []rbacv1.Subject) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Subjects:   subjects,
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "admin"},
	}
}

// startAndWaitSynced starts r.Run in a goroutine and blocks until the RoleBinding cache has
// synced (or the test times out).
func startAndWaitSynced(t *testing.T, r *resolver, stopCh chan struct{}) {
	t.Helper()
	go r.Run(stopCh)
	require.Eventually(t, func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.synced
	}, 5*time.Second, 10*time.Millisecond)
}

func TestNamespacesFor(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset(
		roleBinding("rb-user", "ns-user", []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "alice"}}),
		roleBinding("rb-group", "ns-group", []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "team-a"}}),
		roleBinding("rb-sa", "ns-sa", []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "bot", Namespace: "ns-sa"}}),
		// No Namespace on the subject: Kubernetes defaults it to the RoleBinding's own
		// namespace, and binding a local ServiceAccount unqualified like this is common.
		roleBinding("rb-sa-implicit", "ns-sa-implicit", []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "bot"}}),
		roleBinding("rb-other", "ns-other", []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "bob"}}),
		roleBinding("rb-dup-1", "ns-dup", []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "alice"}}),
		roleBinding("rb-dup-2", "ns-dup", []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "team-a"}}),
	)
	r := NewResolver(testr.New(t), clientset)
	stopCh := make(chan struct{})
	defer close(stopCh)
	startAndWaitSynced(t, r, stopCh)

	namespaces, err := r.NamespacesFor("alice", []string{"team-a"})
	require.NoError(t, err)
	assert.Equal(t, []string{"ns-dup", "ns-group", "ns-user"}, namespaces)

	namespaces, err = r.NamespacesFor("system:serviceaccount:ns-sa:bot", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"ns-sa"}, namespaces)

	namespaces, err = r.NamespacesFor("system:serviceaccount:ns-sa-implicit:bot", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"ns-sa-implicit"}, namespaces)

	// The unqualified subject must not match the same SA name in another namespace.
	namespaces, err = r.NamespacesFor("system:serviceaccount:ns-other:bot", nil)
	require.NoError(t, err)
	assert.Empty(t, namespaces)

	namespaces, err = r.NamespacesFor("carol", nil)
	require.NoError(t, err)
	assert.Empty(t, namespaces)
}

func TestNamespacesForUnsynced(t *testing.T) {
	clientset := k8sfake.NewSimpleClientset()
	r := NewResolver(testr.New(t), clientset)
	_, err := r.NamespacesFor("alice", nil)
	require.Error(t, err)
}

func TestClusterScopeProbeUsable(t *testing.T) {
	t.Run("unsynced defaults to usable", func(t *testing.T) {
		r := NewResolver(testr.New(t), k8sfake.NewSimpleClientset())
		assert.True(t, r.ClusterScopeProbeUsable())
	})

	t.Run("usable when the probe namespace is empty", func(t *testing.T) {
		clientset := k8sfake.NewSimpleClientset(
			roleBinding("rb", "some-other-namespace", []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "alice"}}),
		)
		r := NewResolver(testr.New(t), clientset)
		stopCh := make(chan struct{})
		defer close(stopCh)
		startAndWaitSynced(t, r, stopCh)
		assert.True(t, r.ClusterScopeProbeUsable())
	})

	t.Run("becomes unusable once a RoleBinding is added to the probe namespace", func(t *testing.T) {
		clientset := k8sfake.NewSimpleClientset()
		r := NewResolver(testr.New(t), clientset)
		stopCh := make(chan struct{})
		defer close(stopCh)
		startAndWaitSynced(t, r, stopCh)
		require.True(t, r.ClusterScopeProbeUsable())

		_, err := clientset.RbacV1().RoleBindings(ClusterScopeProbeNamespace).Create(
			context.Background(),
			roleBinding("probe-canary", ClusterScopeProbeNamespace, []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "mallory"}}),
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			return !r.ClusterScopeProbeUsable()
		}, 5*time.Second, 10*time.Millisecond)
	})
}
