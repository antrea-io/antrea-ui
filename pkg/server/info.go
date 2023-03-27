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
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func (s *server) GetControllerInfo(c *gin.Context) {
	if sError := func() *serverError {
		controllerInfo, err := s.k8sClient.Resource(controllerInfoGVR).Get(c, "antrea-controller", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &serverError{
				code:    http.StatusNotFound,
				message: "Controller Info not found",
			}
		} else if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting Controller Info CR: %w", err),
			}
		}
		c.JSON(http.StatusOK, controllerInfo)
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to get controller info")
	}
}

func (s *server) GetAgentInfos(c *gin.Context) {
	if sError := func() *serverError {
		agentInfos, err := s.k8sClient.Resource(agentInfoGVR).List(c, metav1.ListOptions{})
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when listing Agent Info CRs: %w", err),
			}
		}
		c.JSON(http.StatusOK, agentInfos.Items)
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to list agent infos")
	}
}

func (s *server) GetAgentInfo(c *gin.Context) {
	name := c.Param("name")
	if sError := func() *serverError {
		agentInfo, err := s.k8sClient.Resource(agentInfoGVR).Get(c, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &serverError{
				code:    http.StatusNotFound,
				message: "Agent Info not found",
			}
		} else if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting Agent Info CR: %w", err),
			}
		}
		c.JSON(http.StatusOK, agentInfo)
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to get agent info", "name", name)
	}
}

func (s *server) AddInfoRoutes(r *gin.RouterGroup) {
	r = r.Group("/info")
	r.Use(s.checkBearerToken)
	r.GET("/controller", s.GetControllerInfo)
	r.GET("/agents", s.GetAgentInfos)
	r.GET("/agents/:name", s.GetAgentInfo)
}
