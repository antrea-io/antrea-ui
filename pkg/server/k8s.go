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
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// allowedPaths contains the K8s api paths that we are proxying.
// Note the leading slash, since the Gin "catch-all" parameter ("/*path") will include it.
var allowedPaths = []string{
	"/apis/crd.antrea.io/v1beta1/antreaagentinfos",
	"/apis/crd.antrea.io/v1beta1/antreacontrollerinfos",
}

func (s *server) GetK8s(c *gin.Context) {
	// we need to strip the beginning of the path (/api/v1/k8s) before proxying
	path := c.Param("path")
	request := c.Request
	request.URL.Path = path
	// we also ensure that the Bearer Token is removed
	request.Header.Del("Authorization")
	s.k8sProxyHandler.ServeHTTP(c.Writer, c.Request)
}

func (s *server) checkK8sPath(c *gin.Context) {
	if sError := func() *serverError {
		path := c.Param("path")
		for _, allowedPath := range allowedPaths {
			if strings.HasPrefix(path, allowedPath) {
				return nil
			}
		}
		return &serverError{
			code:    http.StatusNotFound,
			message: "This K8s API path is not being proxied",
		}
	}(); sError != nil {
		s.HandleError(c, sError)
		c.Abort()
		return
	}
}

func (s *server) AddK8sRoutes(r *gin.RouterGroup) {
	r = r.Group("/k8s")
	r.Use(s.checkBearerToken)
	r.GET("/*path", s.checkK8sPath, s.GetK8s)
}
