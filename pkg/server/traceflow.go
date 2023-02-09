package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	traceflowhandler "antrea.io/antrea-ui/pkg/handlers/traceflow"
)

func (s *server) CreateTraceflowRequest(c *gin.Context) {
	var requestID string
	if sError := func() *serverError {
		var tfRequest map[string]interface{}
		if err := c.BindJSON(&tfRequest); err != nil {
			return &serverError{
				code:    http.StatusBadRequest,
				message: err.Error(),
			}
		}
		var err error
		requestID, err = s.traceflowRequestsHandler.CreateRequest(c, &traceflowhandler.Request{
			Object: tfRequest,
		})
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when creating Traceflow request: %w", err),
			}
		}
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to create Traceflow request")
		return
	}
	c.Header("Access-Control-Expose-Headers", "Location, Retry-After")
	c.Header("Location", fmt.Sprintf("/api/v1/traceflow/%s/status", requestID))
	c.Header("Retry-After", "2") // 2 seconds
	c.Status(http.StatusAccepted)
}

func (s *server) GetTraceflowRequestStatus(c *gin.Context) {
	requestID := c.Param("requestId")
	var requestStatus *traceflowhandler.RequestStatus
	if sError := func() *serverError {
		var err error
		requestStatus, err = s.traceflowRequestsHandler.GetRequestStatus(c, requestID)
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting Traceflow request status: %w", err),
			}
		}
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to get Traceflow request status", "requestId", requestID)
		return
	}
	if !requestStatus.Done {
		c.Header("Access-Control-Expose-Headers", "Location, Retry-After")
		c.Header("Location", fmt.Sprintf("/api/v1/traceflow/%s/status", requestID))
		c.Header("Retry-After", "1") // 1 second
		// this may not be the best status code for this case
		c.Status(http.StatusAccepted)
		return
	}
	c.Header("Access-Control-Expose-Headers", "Location")
	c.Header("Location", fmt.Sprintf("/api/v1/traceflow/%s/result", requestID))
	c.Status(http.StatusFound)
}

func (s *server) GetTraceflowRequestResult(c *gin.Context) {
	requestID := c.Param("requestId")
	var data []byte
	if sError := func() *serverError {
		tfResult, err := s.traceflowRequestsHandler.GetRequestResult(c, requestID)
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting Traceflow request result: %w", err),
			}
		}
		data, err = json.Marshal(tfResult)
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when converting Traceflow request result to JSON: %w", err),
			}
		}
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to get result for Traceflow request", "requestId", requestID)
		return
	}
	c.Header("Access-Control-Expose-Headers", "Content-Disposition")
	c.Data(http.StatusOK, "application/json; charset=utf-8", data)
}

func (s *server) DeleteTraceflowRequestResult(c *gin.Context) {
	requestID := c.Param("requestId")
	if sError := func() *serverError {
		ok, err := s.traceflowRequestsHandler.DeleteRequest(c, requestID)
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when deleting Traceflow request: %w", err),
			}
		}
		if !ok {
			return &serverError{
				code:    http.StatusNotFound,
				message: "Traceflow request not found",
			}
		}
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to delete Traceflow request", "requestId", requestID)
		return
	}
	c.Status(http.StatusOK)
}

func (s *server) AddTraceflowRoutes(r *gin.RouterGroup) {
	r = r.Group("/traceflow")
	r.Use(s.checkBearerToken)
	// Because this API supports creating resources in the cluster, we
	// rate-limit it to 100 requests per hour out of caution.
	r.POST("", s.CreateTraceflowRequest).Use(rateLimiter(100, 10))
	r.GET("/:requestId/status", s.GetTraceflowRequestStatus)
	r.GET("/:requestId/result", s.GetTraceflowRequestResult)
	r.DELETE("/:requestId/result", s.DeleteTraceflowRequestResult)
}
