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
	// namespaceAccessTTL controls how long a successful rules review is cached
	// for a given session and Namespace. A short TTL avoids repeated reviews
	// during a burst of UI requests while allowing RBAC changes to be reflected
	// without requiring the user to log out.
	namespaceAccessTTL = 30 * time.Second
	// namespaceAccessCacheSize bounds the number of cached (session, Namespace) rule reviews.
	// LRUExpireCache does not proactively remove expired entries, so the TTL limits
	// staleness while this cap bounds memory usage.
	// 100 entries cover, for example, 10 sessions querying 10 Namespaces each within
	// the 30-second TTL. At ~50 KiB per large rule set, this is roughly 5 MiB.
	// Eviction only causes an additional review on the next request.
	namespaceAccessCacheSize = 100
	// maxNamespaceAccessNames limits the number of distinct Namespaces in a
	// request to bound the number of SelfSubjectRulesReviews it can trigger.
	maxNamespaceAccessNames = 10
)

// namespaceAccessEvaluationError is the EvaluationError of a Namespace whose review failed. It is
// fixed text: the error itself stays in the log, as it can name the API server's address.
const namespaceAccessEvaluationError = "the access review for this namespace could not be evaluated"

// namespaceAccessCache stores successful rule reviews per (session, Namespace)
// pair, allowing results to be reused across requests regardless of the
// order or combination of Namespaces.
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

// GetNamespaceAccessSummaries returns authorization rules for the requested
// Namespaces, up to maxNamespaceAccessNames per request. The caller provides
// the Namespaces; this endpoint does not discover them.
//
// Cached rules are reused for each (session, Namespace) pair. Only cache
// misses require a SelfSubjectRulesReview. The endpoint does not interpret
// rules for specific resources or verbs; that is the frontend's responsibility.
//
// Like AccessSummary, the response is a UI rendering hint, not an authorization
// decision. A failed review for one Namespace is reported as unknown without
// discarding the results for other Namespaces.
func (s *Server) GetNamespaceAccessSummaries(c *gin.Context) {
	var list *apisv1.NamespaceAccessSummaryList
	if sError := func() *errors.ServerError {
		names, sError := namespaceAccessNames(c.QueryArray("namespace"))
		if sError != nil {
			return sError
		}
		// The authentication middleware sets RequestAuth for every request reaching
		// this handler. Session-based requests use the session ID to scope cache
		// entries, while bearer-token requests have no session ID and bypass the cache.
		// If RequestAuth is missing, KubernetesClientForRequest rejects the request
		// as unauthenticated.
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
					EvaluationError:  namespaceAccessEvaluationError,
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

// isFatalReviewError reports whether a review failure should abort the entire
// request rather than mark one Namespace as unknown. Authentication failures
// (401), authorization failures (403), and context cancellation are not
// specific to an individual Namespace.
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
