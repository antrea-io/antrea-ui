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

package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"antrea.io/antrea-ui/pkg/server/errors"
)

var (
	controllerInfoGVR = schema.GroupVersionResource{
		Group:    "crd.antrea.io",
		Version:  "v1beta1",
		Resource: "antreacontrollerinfos",
	}
	agentInfoGVR = schema.GroupVersionResource{
		Group:    "crd.antrea.io",
		Version:  "v1beta1",
		Resource: "antreaagentinfos",
	}
)

func (s *Server) GetControllerInfo(c *gin.Context) {
	if sError := func() *errors.ServerError {
		controllerInfo, err := s.k8sClient.Resource(controllerInfoGVR).Get(c, "antrea-controller", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &errors.ServerError{
				Code:    http.StatusNotFound,
				Message: "Controller Info not found",
			}
		} else if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting Controller Info CR: %w", err),
			}
		}
		c.JSON(http.StatusOK, controllerInfo)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get controller info")
	}
}

func (s *Server) GetAgentInfos(c *gin.Context) {
	if sError := func() *errors.ServerError {
		agentInfos, err := s.k8sClient.Resource(agentInfoGVR).List(c, metav1.ListOptions{})
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when listing Agent Info CRs: %w", err),
			}
		}
		c.JSON(http.StatusOK, agentInfos.Items)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to list agent infos")
	}
}

func (s *Server) GetAgentInfo(c *gin.Context) {
	name := c.Param("name")
	if sError := func() *errors.ServerError {
		agentInfo, err := s.k8sClient.Resource(agentInfoGVR).Get(c, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &errors.ServerError{
				Code:    http.StatusNotFound,
				Message: "Agent Info not found",
			}
		} else if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting Agent Info CR: %w", err),
			}
		}
		c.JSON(http.StatusOK, agentInfo)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get agent info", "name", name)
	}
}

func (s *Server) AddInfoRoutes(r *gin.RouterGroup) {
	r = r.Group("/info")
	removalDate := time.Date(2023, 7, 1, 0, 0, 0, 0, time.UTC)
	r.Use(s.checkBearerToken, announceDeprecationMiddleware(removalDate, "use /k8s instead"))
	r.GET("/controller", s.GetControllerInfo)
	r.GET("/agents", s.GetAgentInfos)
	r.GET("/agents/:name", s.GetAgentInfo)
}
