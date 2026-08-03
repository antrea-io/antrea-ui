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
	"github.com/gin-gonic/gin"
)

// GetK8s proxies a read request to the Kubernetes API server, as the identity of the user who made
// it.
//
// There is no path allowlist: RBAC is the guard. In the OIDC, kubeconfig and ServiceAccount-token
// modes it is the end user's own RBAC; with the static admin password it is the antrea-ui-admin
// aggregated ClusterRole, i.e. exactly the permissions an admin approved by applying that RBAC in
// the first place.
func (s *Server) GetK8s(c *gin.Context) {
	// we need to strip the beginning of the path (/api/v1/k8s) before proxying
	path := c.Param("path")
	request := c.Request
	request.URL.Path = path
	// Strip the credentials the client used to authenticate to antrea-ui: the proxy
	// authenticates the request itself, from the credential the middleware resolved, so
	// nothing the client sent has any business reaching the API server. The session cookie in
	// particular is credential-equivalent for the whole UI, and forwarding it would deposit it
	// in the API server's audit log and in every proxy in between.
	//
	// The proxy's own Rewrite does this as well; doing it here too means the guarantee does not
	// depend on which handler is wired in.
	request.Header.Del("Authorization")
	request.Header.Del("Cookie")
	s.k8sProxyHandler.ServeHTTP(c.Writer, c.Request)
}

func (s *Server) AddK8sRoutes(r *gin.RouterGroup) {
	r = r.Group("/k8s")
	r.Use(s.authenticate())
	r.GET("/*path", s.GetK8s)
}
