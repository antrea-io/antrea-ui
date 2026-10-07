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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/cache"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

const (
	// namespaceAccessTTL is how long one session's review of a Namespace is reused. It is short
	// because the answer is what a Namespace selector offers: a grant added or revoked should
	// show up without the user logging out. What the TTL is for is collapsing the burst of calls
	// a page load makes, not sparing the API server indefinitely.
	namespaceAccessTTL = 30 * time.Second
	// namespaceAccessCacheSize bounds the cache, at one entry per (session, Namespace). The TTL
	// bounds how stale an answer can be, not how much memory the cache uses: LRUExpireCache has no
	// background expiry, so an expired entry is only removed when its key is read again or when
	// the cap evicts it. Entries of ended sessions, and of Namespaces a session no longer asks
	// about, stay until the LRU pushes them out, so a long-running server settles at this many
	// entries. A request is bounded by maxNamespaceAccessNames, but what a session adds over
	// successive requests is bounded only by the access routes' rate limit. Evicting an entry only
	// costs a review, so the cap is sized for the live working set, a few Namespaces for each of
	// the sessions that are in use (session.DefaultMaxSessions at most), rather than for every
	// session at its limit. An entry holds a rule list: about 2 KiB in memory for a user with a
	// handful of grants, and about 50 KiB for one with 150 rules, so the cache takes a few MiB
	// typically, and tens of MiB if a tenth of the entries are of the second kind.
	namespaceAccessCacheSize = 2000
	// maxNamespaceAccessNames caps how many Namespaces one request asks about, so that one
	// request cannot fan out into more SelfSubjectRulesReviews than a selector has any use for.
	// A user who may use hundreds of Namespaces picks a few from a searchable list, and asks
	// about those.
	maxNamespaceAccessNames = 10
	// namespaceAccessConcurrency is how many reviews are in flight at once. The reviews are
	// independent and the API server answers each from its in-memory authorization state, so the
	// cost is round trips; a handful in parallel keeps them from serializing into a noticeable
	// wait without making this call look like a load generator.
	namespaceAccessConcurrency = 8
)

// namespaceAccessCache memoizes one session's review of one Namespace, so that the same
// Namespaces in another order, or a subset of an earlier request, are answered without a review.
type namespaceAccessCache struct {
	// entries holds authorizationv1.SubjectRulesReviewStatus keyed by namespaceAccessKey. A cached
	// status is shared by every request that reads it and must be treated as immutable.
	entries *cache.LRUExpireCache
}

func newNamespaceAccessCache() *namespaceAccessCache {
	return &namespaceAccessCache{entries: cache.NewLRUExpireCache(namespaceAccessCacheSize)}
}

// namespaceAccessKey is the cache key of one session's review of one Namespace.
func namespaceAccessKey(sessionID, namespace string) string {
	// Neither a session ID nor a Namespace name can contain a NUL.
	return sessionID + "\x00" + namespace
}

// GetNamespaceAccessSummaries handles GET /api/v1/access-summary/namespaces?namespace=<ns>...:
// what GET /api/v1/access-summary?namespace=<ns> answers, for several Namespaces in one request.
//
// The caller names the Namespaces, at most maxNamespaceAccessNames of them. Kubernetes has no
// reverse lookup from a subject to the Namespaces it may access, so this endpoint does not try
// to find them: the frontend already has what antrea-ui can know (AccessSummary.Namespaces, or
// a Namespace list for a caller who may list them) and asks about the ones it is about to offer
// or act on. One SelfSubjectRulesReview is made for each. This endpoint knows nothing about what
// the caller will ask of the rules: which resource and verb a feature needs is the frontend's
// gate, so every feature that needs a per-Namespace answer shares this one request.
//
// Like AccessSummary this is a rendering hint and never an authorization decision. It differs in
// one respect: a Namespace whose review could not be evaluated is reported as unknown rather
// than failing the request, because the answer for the other Namespaces is still worth having
// and a consumer already has to handle an incomplete rule list.
func (s *Server) GetNamespaceAccessSummaries(c *gin.Context) {
	var list *apisv1.NamespaceAccessSummaryList
	if sError := func() *errors.ServerError {
		names, sError := namespaceAccessNames(c.QueryArray("namespace"))
		if sError != nil {
			return sError
		}
		// The authenticate middleware sets this on every request that gets here. If it were ever
		// missing the request is evaluated without the cache, which is correct and only slower,
		// so it is not worth failing over; a bearer request without a session takes the same path.
		var sessionID string
		if ra, ok := authn.RequestAuthFromGin(c); ok {
			sessionID = ra.SessionID()
		}

		result, err := s.resolveNamespaceAccess(c.Request.Context(), sessionID, names)
		if err != nil {
			return s.k8sError(c, err, "error when evaluating namespace access summaries")
		}
		list = result
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get namespace access summaries")
		return
	}
	c.JSON(http.StatusOK, list)
}

// namespaceAccessNames validates the Namespaces a request names, and drops repeats: the answer
// has one entry per Namespace, in the order first named.
func namespaceAccessNames(requested []string) ([]string, *errors.ServerError) {
	seen := make(map[string]struct{}, len(requested))
	var names []string
	for _, ns := range requested {
		if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
			return nil, &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("invalid namespace %q: %s", ns, strings.Join(errs, "; ")),
			}
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		names = append(names, ns)
	}
	if len(names) == 0 {
		return nil, &errors.ServerError{
			Code:    http.StatusBadRequest,
			Message: "at least one namespace must be given, as the namespace query parameter",
		}
	}
	if len(names) > maxNamespaceAccessNames {
		return nil, &errors.ServerError{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("at most %d namespaces can be given, got %d", maxNamespaceAccessNames, len(names)),
		}
	}
	return names, nil
}

// resolveNamespaceAccess answers for each Namespace from the session's cache, and reviews the
// ones it has none for. An empty sessionID evaluates without the cache.
//
// Only successful reviews are cached, for the same reason the frontend memoizes only successful
// access summaries: a cached failure would keep answering for the whole TTL after the condition
// that caused it cleared. A Namespace whose review failed is reported as unknown and reviewed
// again by the next request, while the others stay cached.
func (s *Server) resolveNamespaceAccess(ctx context.Context, sessionID string, names []string) (*apisv1.NamespaceAccessSummaryList, error) {
	list := &apisv1.NamespaceAccessSummaryList{
		Items: make([]apisv1.NamespaceAccessSummary, len(names)),
	}
	// A bearer-authenticated request has no session, so there is nothing to scope a cache entry
	// to. Sharing one key across session-less requests would hand one credential's answer to
	// another, which this cache must never do, so these requests pay for their own reviews.
	var misses []int
	for i, ns := range names {
		list.Items[i].Namespace = ns
		if sessionID != "" {
			if v, ok := s.namespaceAccess.entries.Get(namespaceAccessKey(sessionID, ns)); ok {
				list.Items[i].Rules = v.(authorizationv1.SubjectRulesReviewStatus)
				continue
			}
		}
		misses = append(misses, i)
	}
	if len(misses) == 0 {
		return list, nil
	}

	clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build K8s client for request: %w", err)
	}
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(namespaceAccessConcurrency)
	for _, i := range misses {
		item := &list.Items[i]
		g.Go(func() error {
			rules, err := selfSubjectRules(gCtx, clientset, item.Namespace)
			switch {
			case err == nil:
				item.Rules = rules
				if sessionID != "" {
					s.namespaceAccess.entries.Add(namespaceAccessKey(sessionID, item.Namespace), rules, namespaceAccessTTL)
				}
			case isFatalReviewError(gCtx, err):
				return err
			default:
				// Neither an allow nor a denial: unknown, which is what Incomplete says. The
				// error itself stays in the log, as it can name the API server's address.
				s.logger.Error(err, "Failed to review access in namespace", "namespace", item.Namespace)
				item.EvaluationFailed = true
				item.Rules = authorizationv1.SubjectRulesReviewStatus{
					ResourceRules:    []authorizationv1.ResourceRule{},
					NonResourceRules: []authorizationv1.NonResourceRule{},
					Incomplete:       true,
					EvaluationError:  "the access review for this namespace could not be evaluated",
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return list, nil
}

// isFatalReviewError reports whether a failed review means the whole request has no answer, rather
// than that one Namespace is unknown. An invalid credential ends the session, a 403 means the
// cluster stripped the self-review grant that every authenticated identity has by default, and a
// done context means nobody is waiting; none of those is specific to one Namespace.
func isFatalReviewError(ctx context.Context, err error) bool {
	return apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err) || ctx.Err() != nil
}

// selfSubjectRules is the SelfSubjectRulesReview for one Namespace.
func selfSubjectRules(ctx context.Context, clientset kubernetes.Interface, namespace string) (authorizationv1.SubjectRulesReviewStatus, error) {
	review, err := clientset.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authorizationv1.SelfSubjectRulesReview{
		Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: namespace},
	}, metav1.CreateOptions{})
	if err != nil {
		return authorizationv1.SubjectRulesReviewStatus{}, err
	}
	return review.Status, nil
}
