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

// Package access watches RoleBindings cluster-wide, using antrea-ui's own credential, to answer
// questions the Kubernetes self-review APIs cannot: which namespaces a user may have access to, and
// whether the sentinel namespace GET /api/v1/access-summary evaluates cluster-scoped rules against
// is still safe to use for that purpose.
package access

import (
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/go-logr/logr"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
)

// ClusterScopeProbeNamespace is the namespace GET /api/v1/access-summary passes to
// SelfSubjectRulesReview when the caller asks for cluster-scoped rules, and the namespace this
// resolver watches to enforce the invariant that doing so relies on: it must hold no RoleBindings.
//
// SelfSubjectRulesReview requires a namespace ("no namespace on request" otherwise) and returns
// cluster-scoped rules and that namespace's rules flattened into one list, with nothing marking
// which is which. Evaluating against a namespace that holds no RoleBindings makes the answer purely
// cluster-scoped. Using kube-system instead would report a user whose only RoleBinding is in
// kube-system as having cluster-wide rights.
//
// The "holds no RoleBindings" part is an invariant, not an assumption: this resolver watches for a
// RoleBinding appearing here and reports it through ClusterScopeProbeUsable. Both the watch and the
// review must name the same namespace, hence the single exported constant: were they to diverge,
// the watch would guard a namespace nobody evaluates against, and cluster-scoped results would
// silently fold in a namespace's RoleBinding-derived rules while still reporting incomplete: false.
const ClusterScopeProbeNamespace = "antrea-ui-cluster-scope-probe"

type resolver struct {
	logger    logr.Logger
	clientset kubernetes.Interface

	mu          sync.RWMutex
	lister      rbaclisters.RoleBindingLister
	synced      bool
	probeUsable bool
}

// NewResolver builds a Resolver. Call Run in a goroutine to start the RoleBinding watch; until the
// cache syncs, NamespacesFor returns an error and ClusterScopeProbeUsable returns true (the
// invariant is unverifiable, not violated).
func NewResolver(logger logr.Logger, clientset kubernetes.Interface) *resolver {
	return &resolver{
		logger:      logger,
		clientset:   clientset,
		probeUsable: true,
	}
}

// trimRoleBinding clears fields the resolver never reads, to keep the informer cache lean. Only
// .metadata.namespace and .subjects are ever used.
func trimRoleBinding(obj interface{}) (interface{}, error) {
	rb, ok := obj.(*rbacv1.RoleBinding)
	if !ok {
		return obj, nil
	}
	rb.ManagedFields = nil
	rb.Annotations = nil
	return rb, nil
}

// Run watches RoleBindings cluster-wide until stopCh is closed. It blocks and should be called from
// a goroutine.
func (r *resolver) Run(stopCh <-chan struct{}) {
	factory := informers.NewSharedInformerFactoryWithOptions(
		r.clientset,
		10*time.Minute,
		// Only .metadata.namespace and .subjects are ever read, and this caches every
		// RoleBinding in the cluster.
		informers.WithTransform(trimRoleBinding),
	)
	lister := factory.Rbac().V1().RoleBindings().Lister()
	informer := factory.Rbac().V1().RoleBindings().Informer()
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { r.handleRoleBindingEvent(obj) },
		UpdateFunc: func(_, newObj interface{}) { r.handleRoleBindingEvent(newObj) },
		DeleteFunc: func(obj interface{}) { r.handleRoleBindingEvent(obj) },
	}); err != nil {
		r.logger.Error(err, "failed to register RoleBinding event handler")
		return
	}
	go informer.Run(stopCh)
	if !cache.WaitForCacheSync(stopCh, informer.HasSynced) {
		r.logger.Info("RoleBinding cache did not sync; namespace discovery is unavailable")
		return
	}
	r.mu.Lock()
	r.lister = lister
	r.synced = true
	r.mu.Unlock()
	r.recheckClusterScopeProbe()
	<-stopCh
}

// handleRoleBindingEvent is the informer event handler. It only needs to react when the event
// concerns the cluster-scope probe namespace; everything else affects NamespacesFor via the shared
// lister with no bookkeeping required here.
func (r *resolver) handleRoleBindingEvent(obj interface{}) {
	rb, ok := obj.(*rbacv1.RoleBinding)
	if !ok {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			rb, ok = tombstone.Obj.(*rbacv1.RoleBinding)
			if !ok {
				return
			}
		} else {
			return
		}
	}
	if rb.Namespace == ClusterScopeProbeNamespace {
		r.recheckClusterScopeProbe()
	}
}

// recheckClusterScopeProbe lists the probe namespace via the lister and stores whether it still
// holds no RoleBindings. It logs once, at error level, on the false transition: silent degradation
// here would be very hard to diagnose.
func (r *resolver) recheckClusterScopeProbe() {
	// Informer event handlers are serialized, but Run also calls this directly after
	// WaitForCacheSync, so two invocations can overlap. The list and the store have to happen
	// under one write lock: with the list outside it, an older invocation can list an empty
	// namespace, be overtaken by a newer one that sees a RoleBinding and stores false, then
	// store true over it. Nothing re-triggers a recheck until the next event in that namespace,
	// so the resolver would keep reporting the invariant as held while it is broken — which is
	// the one failure this probe exists to catch. The lister reads a thread-safe indexer and
	// never calls back into the resolver, so holding the lock across it cannot deadlock. The
	// logging happens after the unlock.
	r.mu.Lock()
	if r.lister == nil {
		r.mu.Unlock()
		return
	}
	rbs, err := r.lister.RoleBindings(ClusterScopeProbeNamespace).List(labels.Everything())
	if err != nil {
		r.mu.Unlock()
		r.logger.Error(err, "failed to list RoleBindings in the cluster-scope probe namespace")
		return
	}
	usable := len(rbs) == 0
	wasUsable := r.probeUsable
	r.probeUsable = usable
	r.mu.Unlock()
	if wasUsable && !usable {
		names := make([]string, 0, len(rbs))
		for _, rb := range rbs {
			names = append(names, rb.Name)
		}
		r.logger.Error(nil, "RoleBinding(s) found in the cluster-scope probe namespace; "+
			"cluster-scoped access-summary results are now marked incomplete",
			"namespace", ClusterScopeProbeNamespace, "roleBindings", names)
	}
}

// ClusterScopeProbeUsable implements Resolver.
func (r *resolver) ClusterScopeProbeUsable() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.synced {
		return true
	}
	return r.probeUsable
}

// NamespacesFor implements Resolver.
func (r *resolver) NamespacesFor(username string, groups []string) ([]string, error) {
	r.mu.RLock()
	lister := r.lister
	synced := r.synced
	r.mu.RUnlock()
	if !synced || lister == nil {
		return nil, fmt.Errorf("RoleBinding cache is not synced")
	}
	rbs, err := lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("failed to list RoleBindings: %w", err)
	}
	groupSet := make(map[string]bool, len(groups))
	for _, g := range groups {
		groupSet[g] = true
	}
	nsSet := make(map[string]bool)
	for _, rb := range rbs {
		if matchesSubject(rb.Subjects, rb.Namespace, username, groupSet) {
			nsSet[rb.Namespace] = true
		}
	}
	namespaces := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)
	return namespaces, nil
}

// matchesSubject reports whether any of subjects names username or one of the groups in groupSet.
// rbNamespace is the namespace of the RoleBinding the subjects come from, needed to resolve
// ServiceAccount subjects that omit their own namespace.
//
// It deliberately does not short-circuit on groups like system:authenticated: a RoleBinding to that
// group genuinely grants everyone access to that namespace, and reporting it is correct.
func matchesSubject(subjects []rbacv1.Subject, rbNamespace, username string, groupSet map[string]bool) bool {
	for _, sub := range subjects {
		switch sub.Kind {
		case rbacv1.UserKind:
			if sub.Name == username {
				return true
			}
		case rbacv1.GroupKind:
			if groupSet[sub.Name] {
				return true
			}
		case rbacv1.ServiceAccountKind:
			// A ServiceAccount subject in a RoleBinding may legally omit its namespace,
			// in which case Kubernetes defaults it to the RoleBinding's own namespace.
			// Match the RBAC authorizer here (appliesToUserInNamespace in
			// k8s.io/kubernetes/pkg/registry/rbac/validation): binding a local
			// ServiceAccount without qualifying it is common.
			saNamespace := sub.Namespace
			if saNamespace == "" {
				saNamespace = rbNamespace
			}
			if fmt.Sprintf("system:serviceaccount:%s:%s", saNamespace, sub.Name) == username {
				return true
			}
		}
	}
	return false
}
