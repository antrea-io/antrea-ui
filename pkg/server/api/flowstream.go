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
	"context"
	goerrors "errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/client-go/kubernetes"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

const (
	// flowAPIGroup and flowResource name the virtual resource the Flow Aggregator's
	// FlowStreamService authorizes streams against. It backs no real object: the only thing
	// that can be asked about it is whether a subject holds a verb on it, which is exactly
	// what a SelfSubjectAccessReview asks.
	flowAPIGroup = "observability.antrea.io"
	flowResource = "flows"
	// flowObserveVerb is the verb a flow stream needs. watch, not list: the SSE stream always
	// sets Follow, and the Flow Aggregator derives the verb from it (list for a non-follow
	// stream, watch for a follow one) so that granting list alone yields history-only access.
	// Checking list here would offer the user a Namespace whose stream then fails.
	flowObserveVerb = "watch"

	// flowNamespacesTTL is how long one session's answer is reused. It is short because the
	// answer is a selector's option list: a grant added or revoked should show up without the
	// user logging out, and the Flow Aggregator's own authorization cache already bounds how
	// fast a new grant takes effect on a stream. What the TTL is for is collapsing the burst
	// of calls a page load makes, not sparing the API server indefinitely.
	flowNamespacesTTL = 30 * time.Second
	// flowNamespacesCacheSize bounds the per-session cache. Sessions are already capped
	// (session.MaxSessions), so this only has to be comfortably above that cap; entries expire
	// on their own well before they would be evicted.
	flowNamespacesCacheSize = 256
	// maxFlowNamespaceCandidates caps how many candidates one call will review, so that a user
	// who can list every Namespace of a large cluster does not turn one request into thousands
	// of SelfSubjectAccessReviews. A truncated list is reported as Incomplete, which is
	// exactly what that field is for.
	//
	// It is one of the two bounds KubernetesClientForRequestUnthrottled requires its callers to
	// impose, the other being flowNamespaceReviewConcurrency.
	maxFlowNamespaceCandidates = 100
	// flowNamespaceReviewConcurrency is how many reviews are in flight at once. The reviews are
	// independent and the API server answers each from its in-memory authorization state, so
	// the cost is round trips; a handful in parallel keeps a large candidate list from
	// serializing into seconds without making this call look like a load generator.
	flowNamespaceReviewConcurrency = 8
)

// flowNamespacesCache memoizes GET /api/v1/flows/namespaces per session.
//
// This uses singleflight.Group, where the Flow Aggregator version probe deliberately does not,
// and the difference is which credential the collapsed callers present. The probe is process-wide
// and its concurrent callers are different users: a follower inheriting the leader's error would
// be told that its own credential was rejected when it was the leader's that was, so the probe
// has followers wait and then re-read the cache instead. Here the key is the session ID, so every
// caller collapsed onto one flight is the same session presenting the same credential and asking
// the same question. The leader's answer — including its error — is genuinely the follower's
// answer, which is the precondition singleflight needs.
type flowNamespacesCache struct {
	// entries holds *apisv1.FlowNamespacesResponse keyed by session ID. A cached response is
	// shared by every request that reads it and must be treated as immutable.
	entries *cache.LRUExpireCache
	flights singleflight.Group
}

func newFlowNamespacesCache() *flowNamespacesCache {
	return &flowNamespacesCache{entries: cache.NewLRUExpireCache(flowNamespacesCacheSize)}
}

// GetFlowNamespaces handles GET /api/v1/flows/namespaces: the options for the observed-namespace
// selector, which the Flow Aggregator requires every stream to name.
//
// Kubernetes has no reverse lookup from a subject to the Namespaces it may access, which is why
// the Flow Aggregator reserves an empty scope rather than defaulting it, so the options have to be
// enumerated: candidate Namespaces from the same path that answers AccessSummary.Namespaces, then
// one SelfSubjectAccessReview each.
//
// Like AccessSummary this is a rendering hint and never an authorization decision — the Flow
// Aggregator authorizes each stream itself — but it is a hint that fails closed: a review that
// cannot be evaluated fails the request rather than being reported as an allow. Offering a
// Namespace whose stream is then refused is the one answer worth avoiding, since the user has no
// way to tell that from a broken deployment.
func (s *Server) GetFlowNamespaces(c *gin.Context) {
	var resp *apisv1.FlowNamespacesResponse
	if sError := func() *errors.ServerError {
		ctx := c.Request.Context()
		// The authenticate middleware guarantees this; a zero value would only mean no
		// caching and no static-admin shortcut, so it is not worth failing over.
		ra, _ := authn.RequestAuthFromGin(c)
		var cacheKey string
		if ra != nil {
			cacheKey = ra.SessionID()
		}

		result, err := s.resolveFlowNamespaces(ctx, cacheKey, staticAdminRequest(c))
		if err != nil {
			return s.namespacesError(c, err)
		}
		resp = result
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get observable flow namespaces")
		return
	}
	c.JSON(http.StatusOK, resp)
}

// resolveFlowNamespaces returns the cached answer for this session, or evaluates one.
//
// Only successes are cached, for the same reason the frontend memoizes only successful access
// summaries: a cached failure would keep answering for the whole TTL after the condition that
// caused it cleared.
func (s *Server) resolveFlowNamespaces(ctx context.Context, cacheKey string, staticAdmin bool) (*apisv1.FlowNamespacesResponse, error) {
	if cacheKey == "" {
		// A bearer-authenticated request has no session, so there is nothing to scope a
		// cache entry to — and nothing to key a single flight by either. Sharing one key
		// across session-less requests would collapse callers presenting different
		// credentials onto one answer, which is the thing this cache must never do, so
		// these requests pay for their own reviews.
		return s.evaluateFlowNamespaces(ctx, staticAdmin)
	}
	if v, ok := s.flowNamespaces.entries.Get(cacheKey); ok {
		return v.(*apisv1.FlowNamespacesResponse), nil
	}
	v, err, shared := s.flowNamespaces.flights.Do(cacheKey, func() (interface{}, error) {
		// Re-read under the flight: a request that arrived while the previous leader was
		// storing its answer would otherwise become a leader of its own.
		if v, ok := s.flowNamespaces.entries.Get(cacheKey); ok {
			return v, nil
		}
		result, err := s.evaluateFlowNamespaces(ctx, staticAdmin)
		if err != nil {
			return nil, err
		}
		s.flowNamespaces.entries.Add(cacheKey, result, flowNamespacesTTL)
		return result, nil
	})
	if err != nil {
		// The flight runs on the leader's request context, so a leader whose client
		// disconnected cancels it. That is the leader's own outcome and not the follower's:
		// a follower whose request is still live evaluates for itself rather than reporting
		// a cancellation that had nothing to do with it.
		if shared && goerrors.Is(err, context.Canceled) && ctx.Err() == nil {
			return s.evaluateFlowNamespaces(ctx, staticAdmin)
		}
		return nil, err
	}
	return v.(*apisv1.FlowNamespacesResponse), nil
}

// evaluateFlowNamespaces makes the reviews: one at cluster scope, then one per candidate
// Namespace.
func (s *Server) evaluateFlowNamespaces(ctx context.Context, staticAdmin bool) (*apisv1.FlowNamespacesResponse, error) {
	// Unthrottled, because client-go's default 5 QPS would turn a long candidate list into a
	// twenty-second wait on a request a user is watching a selector for. The burst is bounded
	// by maxFlowNamespaceCandidates and by how many reviews run at once, below.
	clientset, err := s.clientFactory.KubernetesClientForRequestUnthrottled(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build K8s client for request: %w", err)
	}

	// The cluster-scoped review comes first because an allow makes every per-Namespace review
	// redundant: a cluster-scoped grant authorizes the verb in every Namespace, and the Flow
	// Aggregator itself short-circuits a cluster-wide stream with this single check.
	clusterWide, err := selfSubjectCanObserveFlows(ctx, clientset, "")
	if err != nil {
		return nil, err
	}

	candidates, incomplete, err := s.flowNamespaceCandidates(ctx, clientset, staticAdmin)
	if err != nil {
		return nil, err
	}

	resp := &apisv1.FlowNamespacesResponse{
		// Never null: the frontend iterates this directly, and an empty list is a real
		// answer — this user is a subject of no RoleBinding that puts a Namespace in reach.
		Namespaces:  make([]apisv1.FlowNamespaceAccess, len(candidates)),
		ClusterWide: clusterWide,
		Incomplete:  incomplete,
	}
	for i, ns := range candidates {
		resp.Namespaces[i] = apisv1.FlowNamespaceAccess{Namespace: ns, CanObserve: clusterWide}
	}
	if clusterWide {
		return resp, nil
	}

	// One review per candidate, a few at a time. Any error fails the whole call: an
	// unevaluable review is not an allow, and reporting a Namespace as non-observable because
	// its review errored would be indistinguishable, to the user, from a real denial.
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(flowNamespaceReviewConcurrency)
	for i := range resp.Namespaces {
		g.Go(func() error {
			allowed, err := selfSubjectCanObserveFlows(gCtx, clientset, resp.Namespaces[i].Namespace)
			if err != nil {
				return err
			}
			resp.Namespaces[i].CanObserve = allowed
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return resp, nil
}

// flowNamespaceCandidates returns the Namespaces worth making a review for, and whether that list
// is known not to be exhaustive.
//
// The candidates come from the same place AccessSummary.Namespaces does, so the selector cannot
// offer a Namespace the rest of the UI considers out of reach. Namespaces a user may observe flows
// in but may not discover at all are deliberately not attempted: that is not answerable, and
// Incomplete is how the user is told.
func (s *Server) flowNamespaceCandidates(ctx context.Context, clientset kubernetes.Interface, staticAdmin bool) ([]string, bool, error) {
	var username string
	var groups []string
	if !staticAdmin {
		// Only the RoleBinding-derived path needs the identity, but accessibleNamespaces
		// takes it eagerly and this is one cheap call on the same connection — the same
		// call, in the same order, that the access summary makes.
		ssr, err := clientset.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
		if err != nil {
			return nil, false, err
		}
		username = ssr.Status.UserInfo.Username
		groups = ssr.Status.UserInfo.Groups
	}

	namespaces, err := s.accessibleNamespaces(ctx, clientset, staticAdmin, username, groups)
	if err != nil {
		return nil, false, err
	}

	incomplete := false
	if len(namespaces) == 1 && namespaces[0] == "*" {
		// The caller can list Namespaces, so enumerate them: "*" is not a name a review can
		// be made for, and the enumeration is exhaustive, which is the one case this
		// endpoint can report a complete list for.
		list, err := clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, false, err
		}
		namespaces = make([]string, 0, len(list.Items))
		for i := range list.Items {
			namespaces = append(namespaces, list.Items[i].Name)
		}
	} else {
		// Derived from RoleBinding subjects, which under-reports by construction.
		incomplete = true
	}

	// Sorted so the selector's order is stable across calls; the API server does not promise
	// an order for a list, and the resolver's is its own.
	sort.Strings(namespaces)
	if len(namespaces) > maxFlowNamespaceCandidates {
		namespaces = namespaces[:maxFlowNamespaceCandidates]
		incomplete = true
	}
	return namespaces, incomplete, nil
}

// selfSubjectCanObserveFlows asks whether the caller may observe flows in namespace. An empty
// namespace is a cluster-scoped check, which is how the Flow Aggregator authorizes a cluster-wide
// stream.
func selfSubjectCanObserveFlows(ctx context.Context, clientset kubernetes.Interface, namespace string) (bool, error) {
	review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace: namespace,
				Verb:      flowObserveVerb,
				Group:     flowAPIGroup,
				Resource:  flowResource,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}
