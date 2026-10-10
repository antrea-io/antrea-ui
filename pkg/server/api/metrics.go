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
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/handlers/metricstap"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/ratelimit"
)

const (
	// metricsAPIGroup and metricsResource name the virtual resource the metrics taps are
	// authorized against. No such resource is served by any API server: it only exists as
	// something RBAC rules can name, with the name of a target group as the resource name.
	metricsAPIGroup = "ui.antrea.io"
	metricsResource = "metrics"

	// backendMetricsPath is where the backend exposes its own metrics, and the nonResourceURL
	// a caller needs "get" on to read them.
	backendMetricsPath = "/metrics"

	// metricsTapsPath is the route which opens a tap, relative to /api/v1/metrics.
	metricsTapsPath = "/taps"

	// metricsKeepAliveInterval is how often a tap stream emits an SSE comment and re-checks
	// its session.
	metricsKeepAliveInterval = 5 * time.Second

	// maxMetricsRequestBytes bounds the body of the requests which carry a selection.
	maxMetricsRequestBytes = 1 << 20

	// metricsRequestsBurst is the burst of the rate limit on the requests which list targets,
	// list metric families or open a tap. Its rate is metrics.maxRequestsPerSecond.
	metricsRequestsBurst = 10
)

// MetricsTapsRoute is the full path of the route which opens a metrics tap, as gin reports it.
const MetricsTapsRoute = "/api/v1/metrics" + metricsTapsPath

// FlowStreamRoute is the full path of the flow stream route, as gin reports it.
const FlowStreamRoute = "/api/v1/flows/stream"

func (s *Server) AddMetricsRoutes(root *gin.RouterGroup, apiv1 *gin.RouterGroup) {
	if s.backendMetricsHandler != nil {
		root.GET(backendMetricsPath, s.authenticate(), s.GetBackendMetrics)
	}

	metrics := apiv1.Group("/metrics")
	metrics.Use(s.authenticate())
	if s.metricsManager == nil {
		metrics.GET("/targets", s.metricsDisabled)
		metrics.GET("/families", s.metricsDisabled)
		metrics.POST(metricsTapsPath, s.metricsDisabled)
		metrics.PUT(metricsTapsPath+"/:id/selections", s.metricsDisabled)
		return
	}
	// One budget per user, shared by the three routes. A request for the families of a target
	// makes antrea-ui scrape it with its own credentials, and so does opening a tap, for each
	// of its targets and whether or not the tap stays open: the caps on open taps do not count
	// taps which are opened and closed. One user must not be able to ask for scrapes at will.
	// Replacing the selection of a tap starts none: it applies from the next tick. The limit is
	// keyed like the one of the access routes, and separate from it. A negative rate disables
	// it, which is meant for test environments.
	limited := metrics.Group("")
	if s.config.MaxMetricsRequestsPerSecond >= 0 {
		limited.Use(ratelimit.Middleware(ratelimit.NewClientRateLimiterOrDie(
			fmt.Sprintf("%d/s", s.config.MaxMetricsRequestsPerSecond), metricsRequestsBurst, accessRateLimitClients, accessRateLimitKey)))
	}
	limited.GET("/targets", s.GetMetricsTargets)
	limited.GET("/families", s.GetMetricFamilies)
	limited.POST(metricsTapsPath, s.OpenMetricsTap)
	metrics.PUT(metricsTapsPath+"/:id/selections", s.UpdateMetricsTapSelections)
}

// metricsDisabled answers the metrics routes when metrics are turned off. 501 and not 503, for the
// reason given on flowStreamDisabled: this is how the deployment is configured, not something a
// retry can change.
func (s *Server) metricsDisabled(c *gin.Context) {
	errors.HandleError(c, &errors.ServerError{
		Code:    http.StatusNotImplemented,
		Message: "Metrics are not enabled for this Antrea UI instance (set metrics.enabled in the Helm chart).",
	})
}

// metricsAccessReview asks Kubernetes, as the caller, whether they may read the metrics of
// targetGroup.
//
// The targets themselves cannot answer this, because they never see the caller: scrapes
// authenticate as a ServiceAccount of antrea-ui (see metricstap.TokenSource). That makes this the
// one place where antrea-ui enforces a Kubernetes RBAC answer itself. The review has no namespace,
// so only a cluster-wide grant satisfies it.
func metricsAccessReview(ctx context.Context, clientset kubernetes.Interface, targetGroup string) (bool, error) {
	review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
		Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group:    metricsAPIGroup,
				Resource: metricsResource,
				Verb:     "get",
				Name:     targetGroup,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return review.Status.Allowed, nil
}

func (s *Server) kubernetesClientFor(c *gin.Context) (kubernetes.Interface, *errors.ServerError) {
	clientset, err := s.clientFactory.KubernetesClientForRequest(c.Request.Context())
	if err != nil {
		return nil, &errors.ServerError{
			Code: http.StatusInternalServerError,
			Err:  fmt.Errorf("failed to build K8s client for request: %w", err),
		}
	}
	return clientset, nil
}

func metricsForbiddenError(targetGroup string) *errors.ServerError {
	return &errors.ServerError{
		Code: http.StatusForbidden,
		Message: fmt.Sprintf("not allowed to read the metrics of %q: it requires \"get\" on %s.%s with resource name %q, granted cluster-wide",
			targetGroup, metricsResource, metricsAPIGroup, targetGroup),
	}
}

// requireMetricsAccess is the gate of the metrics routes: the caller must be allowed to read
// every one of targetGroups.
func (s *Server) requireMetricsAccess(c *gin.Context, targetGroups []string) *errors.ServerError {
	clientset, sError := s.kubernetesClientFor(c)
	if sError != nil {
		return sError
	}
	for _, targetGroup := range targetGroups {
		allowed, err := metricsAccessReview(c.Request.Context(), clientset, targetGroup)
		if err != nil {
			return s.k8sError(c, err, "error when checking access to metrics")
		}
		if !allowed {
			return metricsForbiddenError(targetGroup)
		}
	}
	return nil
}

// authorizeMetricsTap is the metricstap.Authorizer of every tap: the same review as
// requireMetricsAccess, repeated for as long as the tap is open so that a tap does not outlive the
// grant it was opened with. ctx is the context of the request which opened the tap, so the review
// is made as the same user.
func (s *Server) authorizeMetricsTap(ctx context.Context, targetGroups []string) error {
	clientset, err := s.clientFactory.KubernetesClientForRequest(ctx)
	if err != nil {
		return err
	}
	for _, targetGroup := range targetGroups {
		allowed, err := metricsAccessReview(ctx, clientset, targetGroup)
		if apierrors.IsUnauthorized(err) {
			// Same as k8sError, minus the cookie: the response is committed already. The
			// credential itself was rejected, so no later request with this session can
			// succeed either.
			if ra, ok := session.RequestAuthFrom(ctx); ok {
				ra.Invalidate()
			}
			return metricstap.ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		if !allowed {
			return metricstap.ErrForbidden
		}
	}
	return nil
}

// metricsRequestError maps a request the manager refused to a response. unknownTargetGroupStatus is
// the status for a target which names a target group that does not exist: a 404 when the target is
// the resource the request is about, a 400 when it is one item of a selection.
func metricsRequestError(err error, unknownTargetGroupStatus int) *errors.ServerError {
	requestErr, ok := stderrors.AsType[*metricstap.RequestError](err)
	if !ok {
		return &errors.ServerError{Code: http.StatusInternalServerError, Err: err}
	}
	code := http.StatusBadRequest
	if requestErr.Kind == metricstap.RequestErrorUnknownTargetGroup {
		code = unknownTargetGroupStatus
	}
	return &errors.ServerError{Code: code, Message: requestErr.Message}
}

// GetMetricsTargets handles GET /api/v1/metrics/targets: the target groups, and for each one the
// caller may read, its current targets. No target is contacted.
//
// A target group the caller may not read, or whose targets cannot be listed, is reported as such in
// its own entry and does not fail the request.
func (s *Server) GetMetricsTargets(c *gin.Context) {
	var list *apisv1.MetricsTargetList
	if sError := func() *errors.ServerError {
		ctx := c.Request.Context()
		clientset, sError := s.kubernetesClientFor(c)
		if sError != nil {
			return sError
		}
		result := &apisv1.MetricsTargetList{TargetGroups: []apisv1.MetricsTargetGroup{}}
		for _, status := range s.metricsManager.TargetGroups() {
			allowed, err := metricsAccessReview(ctx, clientset, status.Name)
			if err != nil {
				return s.k8sError(c, err, "error when checking access to metrics")
			}
			targetGroup := apisv1.MetricsTargetGroup{
				Name:      status.Name,
				Allowed:   allowed,
				Available: status.Available,
				Reason:    status.Reason,
				Targets:   []apisv1.MetricsTarget{},
			}
			if allowed && status.Available {
				targets, err := s.metricsManager.Targets(ctx, status.Name)
				if err != nil {
					s.logger.Error(err, "Failed to list metrics targets", "targetGroup", status.Name)
					targetGroup.Available = false
					targetGroup.Reason = metricstap.ErrorMessage(err)
				} else {
					targetGroup.Targets = targets
				}
			}
			result.TargetGroups = append(result.TargetGroups, targetGroup)
		}
		list = result
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get metrics targets")
		return
	}
	c.JSON(http.StatusOK, list)
}

// GetMetricFamilies handles GET /api/v1/metrics/families?target=<id>: one scrape of one target,
// returned as the catalog of the metric families it exposes, without sample values. This is how a
// client picks the metrics of a tap.
func (s *Server) GetMetricFamilies(c *gin.Context) {
	var list *apisv1.MetricFamilyList
	if sError := func() *errors.ServerError {
		values := c.QueryArray("target")
		switch {
		case len(values) == 0 || values[0] == "":
			return &errors.ServerError{Code: http.StatusBadRequest, Message: "the target query parameter is required"}
		case len(values) > 1:
			return &errors.ServerError{Code: http.StatusBadRequest, Message: fmt.Sprintf("expected a single target, but %d were given", len(values))}
		}
		id := values[0]
		targetGroup, err := s.metricsManager.ResolveTarget(id)
		if err != nil {
			return metricsRequestError(err, http.StatusNotFound)
		}
		if sError := s.requireMetricsAccess(c, []string{targetGroup}); sError != nil {
			return sError
		}
		result, err := s.metricsManager.Families(c.Request.Context(), id)
		if err != nil {
			sError := &errors.ServerError{
				// The failure is the target's, hence an upstream status. Message names the
				// class of the failure only: Err has the details, for the log.
				Code:    http.StatusBadGateway,
				Message: metricstap.ErrorMessage(err),
				Err:     err,
			}
			switch metricstap.ErrorCode(err) {
			case metricstap.ErrorCodeUnsupported:
				sError.Code = http.StatusNotImplemented
			case metricstap.ErrorCodeNotFound:
				sError.Code = http.StatusNotFound
			}
			return sError
		}
		list = result
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get metric families")
		return
	}
	c.JSON(http.StatusOK, list)
}

// metricsTapOwner identifies who opens a tap, and so who can update it: a session, or for a bearer
// request, which has none, a user. This is defense in depth: the ID of a tap is not guessable, and
// an update goes through the same gate as the request which opened the tap.
func metricsTapOwner(ra *session.RequestAuth) string {
	if id := ra.SessionID(); id != "" {
		return "session:" + id + "/" + ra.Username
	}
	return "user:" + ra.Username
}

// metricsTapUser identifies who a tap counts against, for the cap on the open taps of one user:
// the authenticated user, so that opening more sessions does not buy more taps. A static-admin
// session counts on its own, for the reason given on accessRateLimitKey. Nothing but the global
// cap then bounds the taps of these sessions together, so whoever holds the admin password can
// use it up by logging in several times. This is accepted, as it is for the session store (see
// session.perUserCapKey).
func metricsTapUser(ra *session.RequestAuth) string {
	if ra.Mode != session.ModeAdmin && ra.Username != "" {
		return "user:" + ra.Username
	}
	return metricsTapOwner(ra)
}

// errUnauthenticatedMetricsRequest means a handler was reached without the authentication
// middleware having resolved an identity, which is a wiring bug.
var errUnauthenticatedMetricsRequest = stderrors.New("metrics request carries no resolved identity")

func bindMetricsRequest(c *gin.Context, obj any) *errors.ServerError {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxMetricsRequestBytes)
	if err := c.ShouldBindJSON(obj); err != nil {
		return &errors.ServerError{Code: http.StatusBadRequest, Message: "invalid request body"}
	}
	return nil
}

// OpenMetricsTap handles POST /api/v1/metrics/taps. The response is an SSE stream which lasts for
// as long as the tap: the tap ends when the client disconnects, when its session ends, or when it
// is no longer authorized.
func (s *Server) OpenMetricsTap(c *gin.Context) {
	// Canceled when this handler returns for any reason, which is what ends the tap.
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	var ra *session.RequestAuth
	var events <-chan metricstap.Event
	if sError := func() *errors.ServerError {
		var req apisv1.MetricsTapRequest
		if sError := bindMetricsRequest(c, &req); sError != nil {
			return sError
		}
		interval, err := s.metricsManager.ParseInterval(req.Interval)
		if err != nil {
			return metricsRequestError(err, http.StatusBadRequest)
		}
		targetGroups, err := s.metricsManager.ValidateSelections(req.Selections)
		if err != nil {
			return metricsRequestError(err, http.StatusBadRequest)
		}
		if sError := s.requireMetricsAccess(c, targetGroups); sError != nil {
			return sError
		}
		var ok bool
		ra, ok = authn.RequestAuthFromGin(c)
		if !ok {
			return &errors.ServerError{Code: http.StatusInternalServerError, Err: errUnauthenticatedMetricsRequest}
		}
		_, events, err = s.metricsManager.Open(ctx, metricstap.TapOptions{
			Owner:      metricsTapOwner(ra),
			User:       metricsTapUser(ra),
			Interval:   interval,
			Selections: req.Selections,
			Authorizer: s.authorizeMetricsTap,
		})
		switch {
		case stderrors.Is(err, metricstap.ErrTooManyTaps), stderrors.Is(err, metricstap.ErrTooManyTapsForUser):
			return &errors.ServerError{Code: http.StatusTooManyRequests, Message: err.Error()}
		case err != nil:
			return metricsRequestError(err, http.StatusBadRequest)
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to open metrics tap")
		return
	}

	// See flowstream.SSEHandler.StreamFlows for what each header is for, and for why the
	// headers are flushed before there is anything to send.
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()

	keepAlive := time.NewTicker(metricsKeepAliveInterval)
	defer keepAlive.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case <-ctx.Done():
			return false
		case <-keepAlive.C:
			// Like the flow stream, an open tap keeps its session alive, in a background
			// tab too, and must end with it: see RequestAuth.KeepAlive.
			if !ra.KeepAlive(ctx) {
				s.logger.V(2).Info("Closing metrics tap: session is no longer valid")
				return false
			}
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return false
			}
			if fl, ok := c.Writer.(http.Flusher); ok {
				fl.Flush()
			}
			return true
		case event, ok := <-events:
			if !ok {
				// The tap ended on its own, after a terminal error.
				return false
			}
			data, err := json.Marshal(event.Data)
			if err != nil {
				s.logger.Error(err, "Failed to marshal metrics tap event", "event", event.Name)
				return true
			}
			c.SSEvent(event.Name, string(data))
			return true
		}
	})
}

// UpdateMetricsTapSelections handles PUT /api/v1/metrics/taps/:id/selections, which replaces the
// whole selection of an open tap while its stream stays open.
func (s *Server) UpdateMetricsTapSelections(c *gin.Context) {
	if sError := func() *errors.ServerError {
		ra, ok := authn.RequestAuthFromGin(c)
		if !ok {
			return &errors.ServerError{Code: http.StatusInternalServerError, Err: errUnauthenticatedMetricsRequest}
		}
		id := c.Param("id")
		owner := metricsTapOwner(ra)
		notFound := &errors.ServerError{Code: http.StatusNotFound, Message: "metrics tap not found"}
		// Checked first, so that the answer for a tap which is not the caller's does not
		// depend on what the request asks for.
		if !s.metricsManager.HasTap(id, owner) {
			return notFound
		}
		var req apisv1.MetricsTapSelectionsRequest
		if sError := bindMetricsRequest(c, &req); sError != nil {
			return sError
		}
		targetGroups, err := s.metricsManager.ValidateSelections(req.Selections)
		if err != nil {
			return metricsRequestError(err, http.StatusBadRequest)
		}
		if sError := s.requireMetricsAccess(c, targetGroups); sError != nil {
			return sError
		}
		err = s.metricsManager.UpdateSelections(id, owner, req.Selections)
		switch {
		case stderrors.Is(err, metricstap.ErrTapNotFound):
			// The tap ended between the check above and now.
			return notFound
		case err != nil:
			return metricsRequestError(err, http.StatusBadRequest)
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to update metrics tap")
		return
	}
	c.Status(http.StatusNoContent)
}

// GetBackendMetrics handles GET /metrics: the backend's own metrics, in the Prometheus exposition
// format, for a Prometheus server to scrape.
//
// It is authorized the way the Antrea components authorize their own /metrics endpoint, with "get"
// on the /metrics nonResourceURL, so the ClusterRole a Prometheus server already has for them
// works here too. The review is made as the caller, on every scrape.
func (s *Server) GetBackendMetrics(c *gin.Context) {
	if sError := func() *errors.ServerError {
		clientset, sError := s.kubernetesClientFor(c)
		if sError != nil {
			return sError
		}
		review, err := clientset.AuthorizationV1().SelfSubjectAccessReviews().Create(c.Request.Context(), &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				NonResourceAttributes: &authorizationv1.NonResourceAttributes{
					Path: backendMetricsPath,
					Verb: "get",
				},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return s.k8sError(c, err, "error when checking access to metrics")
		}
		if !review.Status.Allowed {
			return &errors.ServerError{
				Code:    http.StatusForbidden,
				Message: fmt.Sprintf("not allowed to read the metrics of Antrea UI: it requires \"get\" on the %s nonResourceURL", backendMetricsPath),
			}
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to serve metrics")
		return
	}
	s.backendMetricsHandler.ServeHTTP(c.Writer, c.Request)
}
