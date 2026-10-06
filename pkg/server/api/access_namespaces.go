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
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
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
	// namespaceAccessTTL is how long one session's answer is reused. It is short because the
	// answer is what a Namespace selector offers: a grant added or revoked should show up without
	// the user logging out. What the TTL is for is collapsing the burst of calls a page load
	// makes, not sparing the API server indefinitely.
	namespaceAccessTTL = 30 * time.Second
	// namespaceAccessCacheSize bounds the cache. Sessions are already capped
	// (session.MaxSessions) and a session asks about a few sets of names at a time; entries
	// expire on their own well before they would be evicted.
	namespaceAccessCacheSize = 1024
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

// namespaceAccessCache memoizes GET /api/v1/access-summary/namespaces per session and set of
// Namespaces.
//
// It uses singleflight.Group, and the key is what makes that sound: the session ID, so every
// caller collapsed onto one flight is the same session presenting the same credential, and the
// Namespaces asked about, so they are all asking the same question. The leader's answer,
// including its error, is genuinely the follower's.
type namespaceAccessCache struct {
	// entries holds *apisv1.NamespaceAccessSummaryList keyed by namespaceAccessKey. A cached
	// response is shared by every request that reads it and must be treated as immutable.
	entries *cache.LRUExpireCache
	flights singleflight.Group
}

func newNamespaceAccessCache() *namespaceAccessCache {
	return &namespaceAccessCache{entries: cache.NewLRUExpireCache(namespaceAccessCacheSize)}
}

// namespaceAccessKey is the cache key of one session's question. The names are in request order,
// which is the order of the answer, so a question in a different order is a different one.
func namespaceAccessKey(sessionID string, names []string) string {
	// Neither a session ID nor a Namespace name can contain a NUL.
	return sessionID + "\x00" + strings.Join(names, "\x00")
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
		ctx := c.Request.Context()
		// The authenticate middleware sets this on every request that gets here. If it were ever
		// missing the request is evaluated without the cache, which is correct and only slower,
		// so it is not worth failing over; a bearer request without a session takes the same path.
		var cacheKey string
		if ra, ok := authn.RequestAuthFromGin(c); ok && ra.SessionID() != "" {
			cacheKey = namespaceAccessKey(ra.SessionID(), names)
		}

		result, err := s.resolveNamespaceAccess(ctx, cacheKey, names)
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

// resolveNamespaceAccess returns the cached answer for this question, or evaluates one. An empty
// cacheKey evaluates without the cache.
//
// Only complete successes are cached, for the same reason the frontend memoizes only successful
// access summaries: a cached failure would keep answering for the whole TTL after the condition
// that caused it cleared. An answer in which a Namespace's review failed is not complete: that
// Namespace would stay unknown for the whole TTL after a blip of a second.
func (s *Server) resolveNamespaceAccess(ctx context.Context, cacheKey string, names []string) (*apisv1.NamespaceAccessSummaryList, error) {
	if cacheKey == "" {
		// A bearer-authenticated request has no session, so there is nothing to scope a
		// cache entry to, and nothing to key a single flight by either. Sharing one key
		// across session-less requests would collapse callers presenting different
		// credentials onto one answer, which is the thing this cache must never do, so
		// these requests pay for their own reviews.
		list, _, err := s.evaluateNamespaceAccess(ctx, names)
		return list, err
	}
	if v, ok := s.namespaceAccess.entries.Get(cacheKey); ok {
		return v.(*apisv1.NamespaceAccessSummaryList), nil
	}
	v, err, shared := s.namespaceAccess.flights.Do(cacheKey, func() (any, error) {
		// Re-read under the flight: a request that arrived while the previous leader was
		// storing its answer would otherwise become a leader of its own.
		if v, ok := s.namespaceAccess.entries.Get(cacheKey); ok {
			return v, nil
		}
		result, complete, err := s.evaluateNamespaceAccess(ctx, names)
		if err != nil {
			return nil, err
		}
		if complete {
			s.namespaceAccess.entries.Add(cacheKey, result, namespaceAccessTTL)
		}
		return result, nil
	})
	if err != nil {
		// The flight runs on the leader's request context, so a leader whose client
		// disconnected cancels it, and one that ran out of time (the HTTP server's own timeout)
		// ends it with a deadline. Either is the leader's own outcome and not the follower's: a
		// follower whose request is still live evaluates for itself rather than reporting an
		// error that had nothing to do with it.
		if shared && (goerrors.Is(err, context.Canceled) || goerrors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
			list, _, err := s.evaluateNamespaceAccess(ctx, names)
			return list, err
		}
		return nil, err
	}
	return v.(*apisv1.NamespaceAccessSummaryList), nil
}

// evaluateNamespaceAccess reviews each Namespace. The bool is false when the review of some
// Namespace failed and was reported as unknown, which a caller must not cache.
func (s *Server) evaluateNamespaceAccess(ctx context.Context, names []string) (*apisv1.NamespaceAccessSummaryList, bool, error) {
	clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to build K8s client for request: %w", err)
	}

	list := &apisv1.NamespaceAccessSummaryList{
		Items: make([]apisv1.NamespaceAccessSummary, len(names)),
	}
	var downgraded atomic.Bool
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(namespaceAccessConcurrency)
	for i, ns := range names {
		list.Items[i].Namespace = ns
		g.Go(func() error {
			rules, err := selfSubjectRules(gCtx, clientset, ns)
			switch {
			case err == nil:
				list.Items[i].Rules = rules
			case isFatalReviewError(gCtx, err):
				return err
			default:
				// Neither an allow nor a denial: unknown, which is what Incomplete says. The
				// error itself stays in the log, as it can name the API server's address.
				s.logger.Error(err, "Failed to review access in namespace", "namespace", ns)
				downgraded.Store(true)
				list.Items[i].Rules = authorizationv1.SubjectRulesReviewStatus{
					Incomplete:      true,
					EvaluationError: "the access review for this namespace could not be evaluated",
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, false, err
	}
	return list, !downgraded.Load(), nil
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
