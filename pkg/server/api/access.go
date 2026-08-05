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
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	accesshandler "antrea.io/antrea-ui/pkg/handlers/access"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

// GetAccessSummary handles GET /api/v1/access-summary. AccessSummary is a rendering hint, never an
// authorization decision, so the contract is simply: 200 means every field is a real answer,
// anything else means the frontend should fail open and retry later.
//
// There is deliberately no partial success. Every field the frontend actually consumes is either
// authoritative or absent, and a response carrying "I could not work this out" would be
// indistinguishable, to a caller that fails open, from an error — while still being cached as a
// success. The self-review calls used here are granted to every authenticated identity by the
// default system:basic-user ClusterRole, so a 403 from one of them means the cluster stripped
// that default: an error, not a mode worth modelling.
func (s *Server) GetAccessSummary(c *gin.Context) {
	var summary *apisv1.AccessSummary
	if sError := func() *errors.ServerError {
		ctx := c.Request.Context()

		ns := c.Query("namespace")
		rulesNamespace := ns
		result := &apisv1.AccessSummary{}
		if ns == "" {
			rulesNamespace = accesshandler.ClusterScopeProbeNamespace
		} else {
			if errs := validation.IsDNS1123Label(ns); len(errs) > 0 {
				return &errors.ServerError{
					Code:    http.StatusBadRequest,
					Message: fmt.Sprintf("invalid namespace %q: %s", ns, strings.Join(errs, "; ")),
				}
			}
			result.Namespace = ns
		}

		clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("failed to build K8s client for request: %w", err),
			}
		}

		// Step 1: identity.
		ssr, err := clientset.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
		if err != nil {
			return s.k8sError(c, err, "error when evaluating access summary")
		}
		result.Username = ssr.Status.UserInfo.Username
		result.Groups = ssr.Status.UserInfo.Groups

		// Step 2: rules.
		ssrr, err := clientset.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authorizationv1.SelfSubjectRulesReview{
			Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: rulesNamespace},
		}, metav1.CreateOptions{})
		if err != nil {
			return s.k8sError(c, err, "error when evaluating access summary")
		}
		result.Rules = ssrr.Status
		// Incomplete is normally the API server's own verdict (a webhook authorizer it cannot
		// enumerate). It is also set here when the cluster-scope probe is unusable, because the
		// rules then conflate cluster-scoped and namespaced grants.
		if ns == "" && !s.accessResolver.ClusterScopeProbeUsable() {
			result.Rules.Incomplete = true
		}

		// Step 3: cluster-admin. A literal "*" in the request matches only rules that
		// themselves hold "*", which is exactly the wildcard-ClusterRole case.
		review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb:     "*",
					Group:    "*",
					Resource: "*",
				},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return s.k8sError(c, err, "error when evaluating access summary")
		}
		result.ClusterAdmin = review.Status.Allowed

		// Step 4: namespaces. Static admin is answered without consulting the resolver at all,
		// so that a resolver failure cannot fail the one session an operator uses to fix a
		// cluster whose RBAC is broken.
		//
		// This is a fallback, not what makes static admin work: the impersonated
		// antrea-ui-admin ServiceAccount can list namespaces (antrea-ui-admin-core grants it),
		// so the SelfSubjectAccessReview below would answer ["*"] for it anyway — the same
		// answer, by the same path, as for a user bound to that role with their own identity.
		// Those two are meant to have the same permissions, so they must not diverge here.
		if ra, ok := authn.RequestAuthFromGin(c); ok && ra.Mode == session.ModeAdmin {
			result.Namespaces = []string{"*"}
		} else {
			review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Verb:     "list",
						Resource: "namespaces",
					},
				},
			}, metav1.CreateOptions{})
			if err != nil {
				return s.k8sError(c, err, "error when evaluating access summary")
			}
			if review.Status.Allowed {
				result.Namespaces = []string{"*"}
			} else {
				namespaces, sErr := s.namespacesFor(result.Username, result.Groups)
				if sErr != nil {
					return sErr
				}
				result.Namespaces = namespaces
			}
		}

		summary = result
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get access summary")
		return
	}
	c.JSON(http.StatusOK, summary)
}

// namespacesFor resolves the namespaces the caller may access from RoleBinding subjects, for a
// caller who cannot list namespaces cluster-wide.
//
// A resolver failure is a 503 rather than an empty list. An empty list is a legitimate answer
// ("you are a subject of no RoleBinding"), so substituting one for "I could not tell" would report
// something false, and the frontend memoizes the summary for the whole session: it would keep the
// wrong answer until the next full page load. Failing the request instead is honest, matches the
// documented fallback of behaving "as if the endpoint were unavailable", and lets the frontend
// retry on its next call — the failure modes here (informer cache not yet synced, or the chart's
// rolebindings grant not applied) are exactly the ones that resolve themselves or need an operator
// to notice.
func (s *Server) namespacesFor(username string, groups []string) ([]string, *errors.ServerError) {
	namespaces, err := s.accessResolver.NamespacesFor(username, groups)
	if err != nil {
		return nil, &errors.ServerError{
			Code:    http.StatusServiceUnavailable,
			Err:     fmt.Errorf("failed to resolve accessible namespaces: %w", err),
			Message: "namespace discovery is not ready",
		}
	}
	// Never null: the frontend indexes into this field directly. Owning the guarantee here keeps
	// it next to the failure case it has to be distinguishable from.
	if namespaces == nil {
		return []string{}, nil
	}
	return namespaces, nil
}

func (s *Server) AddAccessRoutes(r *gin.RouterGroup) {
	r.GET("/access-summary", s.authenticate(), s.GetAccessSummary)
}
