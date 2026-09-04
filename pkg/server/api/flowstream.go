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

	"github.com/gin-gonic/gin"

	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

// requireFlowVisibility restricts the flow stream to the built-in admin and to Kubernetes cluster
// admins. TEMPORARY: flow data has no per-user authorization, so "can log in at all" is too broad a
// boundary for it (antrea-io/antrea-ui#1387). Remove once FlowStreamService authorizes per user.
//
// One extra SelfSubjectAccessReview per stream *open*, not per flow; opens are one per page load.
func (s *Server) requireFlowVisibility() gin.HandlerFunc {
	return func(c *gin.Context) {
		// The static-admin session impersonates the antrea-ui-admin ServiceAccount, whose
		// aggregated ClusterRole holds no */*/* rule, so the wildcard review below answers
		// false for it. Short-circuiting before any Kubernetes call is also what keeps a
		// broken API server from locking out the one session an operator uses to fix a
		// cluster — the same reasoning as step 4 of GetAccessSummary.
		if ra, ok := authn.RequestAuthFromGin(c); ok && ra.Mode == session.ModeAdmin {
			c.Next()
			return
		}

		sErr := func() *errors.ServerError {
			ctx := c.Request.Context()
			clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
			if err != nil {
				return &errors.ServerError{
					Code: http.StatusInternalServerError,
					Err:  fmt.Errorf("failed to build K8s client for request: %w", err),
				}
			}
			// Fails closed: a review we could not evaluate is not an allow. A transport
			// failure lands in k8sError's default branch as a 500, and an
			// IsUnauthorized error invalidates the session and clears the cookie, which
			// is the right outcome for a credential the API server rejected.
			clusterAdmin, err := selfSubjectClusterAdmin(ctx, clientset)
			if err != nil {
				return s.k8sError(c, err, "error when evaluating flow visibility access")
			}
			if clusterAdmin {
				return nil
			}
			// Names the restriction, so the message is distinguishable from the
			// Kubernetes RBAC text an SSAR 403 would carry.
			return &errors.ServerError{
				Code:    http.StatusForbidden,
				Message: "Flow visibility is restricted to administrators",
			}
		}()
		if sErr != nil {
			errors.HandleError(c, sErr)
			s.LogError(sErr, "Failed to authorize flow visibility request")
			// HandleError writes a response but does not abort, so without this the
			// handler would still run and start streaming.
			c.Abort()
			return
		}
		c.Next()
	}
}
