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
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"antrea.io/antrea-ui/pkg/server/errors"
)

// k8sProxyMethods are the HTTP methods the K8s proxy forwards. Between them they cover every
// Kubernetes API request verb (get, list, watch, create, update, patch, delete, deletecollection).
var k8sProxyMethods = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
}

// isProxySubresource reports whether p, a clean path, names the proxy subresource of a Service, a
// Pod or a Node, through which the API server relays the responses of a workload or a kubelet.
func isProxySubresource(p string) bool {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "v1" {
		return false
	}
	switch parts[2] {
	case "namespaces":
		// api/v1/namespaces/<namespace>/{services,pods}/<name>/proxy[/...]
		return len(parts) >= 7 && (parts[4] == "services" || parts[4] == "pods") && parts[6] == "proxy"
	case "nodes":
		// api/v1/nodes/<name>/proxy[/...]
		return len(parts) >= 5 && parts[4] == "proxy"
	}
	return false
}

// cleanPath returns p with "//", "." and ".." elements resolved, keeping a trailing slash.
func cleanPath(p string) string {
	cleaned := path.Clean(p)
	if strings.HasSuffix(p, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

// ProxyK8s proxies a request to the Kubernetes API server, as the identity of the user who made it.
//
// There is no verb allowlist, and the only paths refused are the proxy subresources of Services,
// Pods and Nodes (see below): RBAC is the guard. In the OIDC, kubeconfig and ServiceAccount-token
// modes it is the end user's own RBAC; with the static admin password it is the antrea-ui-admin
// aggregated ClusterRole, i.e. exactly the permissions an admin approved by applying that RBAC in
// the first place.
//
// Write requests need no CSRF check of their own: the authentication middleware already rejects a
// cross-origin request authenticated by the session cookie, whatever its method.
func (s *Server) ProxyK8s(c *gin.Context) {
	// Strip the beginning of the path (/api/v1/k8s) before proxying. The path is cleaned so
	// that the one checked below is exactly the one the API server receives. RawPath still
	// holds the original, prefixed path, so it is dropped as well.
	urlPath := cleanPath(c.Param("path"))
	// A proxy subresource relays whatever a workload (or a kubelet) answers, and the browser
	// gets it on the UI's own origin: HTML with script that runs as the UI, headers such as
	// Set-Cookie or Clear-Site-Data that act on the UI's origin, a 401 that ends the session.
	// The UI has no use for these subresources, so they are refused rather than filtered.
	if isProxySubresource(urlPath) {
		errors.HandleError(c, &errors.ServerError{
			Code:    http.StatusForbidden,
			Message: "The K8s proxy does not forward requests to the proxy subresource of Services, Pods and Nodes",
		})
		return
	}
	request := c.Request
	request.URL.Path = urlPath
	request.URL.RawPath = ""
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
	for _, method := range k8sProxyMethods {
		r.Handle(method, "/*path", s.ProxyK8s)
	}
}
