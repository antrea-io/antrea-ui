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
	stderrors "errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	supportbundlehandler "antrea.io/antrea-ui/pkg/handlers/supportbundle"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/ratelimit"
)

// The support bundle API is authorized against a virtual resource: nothing serves
// supportbundles.ui.antrea.io, but RBAC can still grant it, and a SelfSubjectAccessReview can still
// check the grant.
const (
	supportBundleAPIGroup = "ui.antrea.io"
	supportBundleResource = "supportbundles"
)

func supportBundleStatusPath(id string) string {
	return fmt.Sprintf("/api/v1/supportbundle/%s/status", id)
}

// supportBundleRBACGate aborts the request unless the caller may perform verb on
// supportbundles.ui.antrea.io. Bundles are shared, so this is the only authorization there is:
// whoever passes it can see, download and delete every bundle.
func (s *Server) supportBundleRBACGate(verb string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sError := func() *errors.ServerError {
			ctx := c.Request.Context()
			clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
			if err != nil {
				return &errors.ServerError{
					Code: http.StatusInternalServerError,
					Err:  fmt.Errorf("failed to build K8s client for request: %w", err),
				}
			}
			review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
				Spec: authorizationv1.SelfSubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Verb:     verb,
						Group:    supportBundleAPIGroup,
						Resource: supportBundleResource,
					},
				},
			}, metav1.CreateOptions{})
			if err != nil {
				return s.k8sError(c, err, "error when checking support bundle access")
			}
			if !review.Status.Allowed {
				return &errors.ServerError{
					Code:    http.StatusForbidden,
					Message: fmt.Sprintf("not allowed to %s %s.%s", verb, supportBundleResource, supportBundleAPIGroup),
				}
			}
			return nil
		}(); sError != nil {
			errors.HandleError(c, sError)
			s.LogError(sError, "Failed to authorize support bundle request")
			c.Abort()
			return
		}
	}
}

func (s *Server) CreateSupportBundle(c *gin.Context) {
	var bundle *apisv1.SupportBundle
	if sError := func() *errors.ServerError {
		var request apisv1.SupportBundleRequest
		// The body is optional.
		if err := c.ShouldBindJSON(&request); err != nil && !stderrors.Is(err, io.EOF) {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			}
		}
		ra, ok := authn.RequestAuthFromGin(c)
		if !ok {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("request carries no resolved identity"),
			}
		}
		var err error
		bundle, err = s.supportBundleManager.Create(ra.Username, &request)
		switch {
		case err == nil:
			return nil
		case stderrors.Is(err, supportbundlehandler.ErrInvalidRequest):
			return &errors.ServerError{Code: http.StatusBadRequest, Message: err.Error()}
		case stderrors.Is(err, supportbundlehandler.ErrLimitReached):
			return &errors.ServerError{Code: http.StatusTooManyRequests, Message: err.Error()}
		default:
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when creating support bundle: %w", err),
			}
		}
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to create support bundle")
		return
	}
	c.Header("Access-Control-Expose-Headers", "Location, Retry-After")
	c.Header("Location", supportBundleStatusPath(bundle.ID))
	c.Header("Retry-After", "2") // 2 seconds
	c.JSON(http.StatusAccepted, bundle)
}

func (s *Server) ListSupportBundles(c *gin.Context) {
	items := s.supportBundleManager.List()
	if items == nil {
		// Never null: the frontend iterates over this field directly.
		items = []apisv1.SupportBundle{}
	}
	c.JSON(http.StatusOK, apisv1.SupportBundleList{Items: items})
}

func (s *Server) supportBundleNotFound() *errors.ServerError {
	return &errors.ServerError{
		Code:    http.StatusNotFound,
		Message: "Support bundle not found",
	}
}

// GetSupportBundleStatus always answers 200, never a redirect to the download (unlike Traceflow's
// status): fetch() follows redirects on its own, and would pull the whole tarball into the page's
// memory. Location tells the client where to go next instead.
func (s *Server) GetSupportBundleStatus(c *gin.Context) {
	id := c.Param("bundleId")
	bundle, err := s.supportBundleManager.Get(id)
	if err != nil {
		sError := s.supportBundleNotFound()
		if !stderrors.Is(err, supportbundlehandler.ErrNotFound) {
			sError = &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting support bundle: %w", err),
			}
		}
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get support bundle status", "bundleId", id)
		return
	}
	switch bundle.Status {
	case apisv1.SupportBundleStatusCollecting:
		c.Header("Access-Control-Expose-Headers", "Location, Retry-After")
		c.Header("Location", supportBundleStatusPath(id))
		c.Header("Retry-After", "2") // 2 seconds
	case apisv1.SupportBundleStatusCollected:
		c.Header("Access-Control-Expose-Headers", "Location")
		c.Header("Location", fmt.Sprintf("/api/v1/supportbundle/%s/download", id))
	}
	c.JSON(http.StatusOK, bundle)
}

func (s *Server) DownloadSupportBundle(c *gin.Context) {
	id := c.Param("bundleId")
	f, bundle, err := s.supportBundleManager.Open(id)
	if err != nil {
		var sError *errors.ServerError
		switch {
		case stderrors.Is(err, supportbundlehandler.ErrNotFound):
			sError = s.supportBundleNotFound()
		case stderrors.Is(err, supportbundlehandler.ErrNotCollected):
			sError = &errors.ServerError{
				Code:    http.StatusNotFound,
				Message: "Support bundle not available, call the /status endpoint to check progress",
			}
		default:
			sError = &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when opening support bundle: %w", err),
			}
		}
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to download support bundle", "bundleId", id)
		return
	}
	defer f.Close()
	filename := fmt.Sprintf("antrea-ui-supportbundle-%s.tar.gz", bundle.CreatedAt.UTC().Format("20060102T150405Z"))
	c.Header("Access-Control-Expose-Headers", "Content-Disposition")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Content-Type", "application/gzip")
	http.ServeContent(c.Writer, c.Request, filename, bundle.CreatedAt, f)
}

func (s *Server) DeleteSupportBundle(c *gin.Context) {
	id := c.Param("bundleId")
	if err := s.supportBundleManager.Delete(id); err != nil {
		sError := s.supportBundleNotFound()
		if !stderrors.Is(err, supportbundlehandler.ErrNotFound) {
			sError = &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when deleting support bundle: %w", err),
			}
		}
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to delete support bundle", "bundleId", id)
		return
	}
	c.Status(http.StatusOK)
}

// supportBundleDisabled handles every support bundle route when the feature is off. 501, as for
// the flow stream: this is a per-deployment configuration choice that no retry can change.
func (s *Server) supportBundleDisabled(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": "Support bundles are not enabled for this Antrea UI instance (set supportBundle.enabled in the Helm chart).",
	})
}

func (s *Server) AddSupportBundleRoutes(r *gin.RouterGroup) {
	r = r.Group("/supportbundle")
	r.Use(s.authenticate())
	route := func(method, path, verb string, handlers ...gin.HandlerFunc) {
		if s.supportBundleManager == nil {
			r.Handle(method, path, s.supportBundleDisabled)
			return
		}
		r.Handle(method, path, append([]gin.HandlerFunc{s.supportBundleRBACGate(verb)}, handlers...)...)
	}

	// The rate limiter runs after the RBAC gate, so that callers who may not create bundles
	// cannot use up everyone else's budget.
	createHandlers := []gin.HandlerFunc{}
	if s.config.MaxSupportBundlesPerHour >= 0 {
		rateLimiter := ratelimit.NewGlobalRateLimiterOrDie(fmt.Sprintf("%d/h", s.config.MaxSupportBundlesPerHour), s.config.MaxSupportBundlesPerHour)
		createHandlers = append(createHandlers, ratelimit.Middleware(rateLimiter))
	}
	createHandlers = append(createHandlers, s.CreateSupportBundle)
	route(http.MethodPost, "", "create", createHandlers...)
	route(http.MethodGet, "", "list", s.ListSupportBundles)
	route(http.MethodGet, "/:bundleId", "get", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, c.Request.URL.Path+"/status")
	})
	route(http.MethodGet, "/:bundleId/status", "get", s.GetSupportBundleStatus)
	route(http.MethodGet, "/:bundleId/download", "get", s.DownloadSupportBundle)
	route(http.MethodDelete, "/:bundleId", "delete", s.DeleteSupportBundle)
}
