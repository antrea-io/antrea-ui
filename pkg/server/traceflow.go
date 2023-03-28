// Copyright 2023 Antrea Authors.
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
	c.Header("Location", fmt.Sprintf("/api/v1/traceflow/%s", requestID))
	c.Header("Retry-After", "2") // 2 seconds
	c.Status(http.StatusAccepted)
}

func (s *server) GetTraceflowRequestStatus(c *gin.Context) {
	requestID := c.Param("requestId")
	var done bool
	if sError := func() *serverError {
		var err error
		_, done, err = s.traceflowRequestsHandler.GetRequestResult(c, requestID)
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
	if !done {
		c.Header("Access-Control-Expose-Headers", "Location, Retry-After")
		c.Header("Location", fmt.Sprintf("/api/v1/traceflow/%s/status", requestID))
		c.Header("Retry-After", "1") // 1 second
		c.Status(http.StatusOK)
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
		tfResult, done, err := s.traceflowRequestsHandler.GetRequestResult(c, requestID)
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting Traceflow request result: %w", err),
			}
		}
		if !done {
			return &serverError{
				code:    http.StatusNotFound,
				message: "Traceflow result not available, call the /status endpoint to check progress",
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

func (s *server) DeleteTraceflowRequest(c *gin.Context) {
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
	r.POST("", s.CreateTraceflowRequest, rateLimiter(100, 10))
	r.GET("/:requestId/status", s.GetTraceflowRequestStatus)
	r.GET("/:requestId", func(c *gin.Context) {
		c.Redirect(http.StatusSeeOther, c.Request.URL.Path+"/status")
	})
	r.GET("/:requestId/result", s.GetTraceflowRequestResult)
	r.DELETE("/:requestId", s.DeleteTraceflowRequest)
}
