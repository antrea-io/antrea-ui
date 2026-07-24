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
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetPluginsIndex serves the list of frontend plugins currently known to the
// backend, discovered from labeled ConfigMaps (see pkg/plugins). Like
// /settings, this is unauthenticated: it carries no confidential data, and
// the frontend needs it before a user has authenticated.
func (s *Server) GetPluginsIndex(c *gin.Context) {
	c.JSON(http.StatusOK, s.pluginRegistry.Index())
}

// GetPluginFile serves one file (the manifest or the JS entry module) for a
// single plugin, so the frontend can import() it at runtime.
func (s *Server) GetPluginFile(c *gin.Context) {
	name := c.Param("name")
	filename := strings.TrimPrefix(c.Param("filepath"), "/")
	data, ok := s.pluginRegistry.File(name, filename)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, pluginFileContentType(filename), data)
}

func pluginFileContentType(filename string) string {
	switch path.Ext(filename) {
	case ".js", ".mjs":
		return "application/javascript"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) AddPluginsRoutes(r *gin.RouterGroup) {
	r = r.Group("/plugins")
	r.GET("/index.json", s.GetPluginsIndex)
	r.GET("/:name/*filepath", s.GetPluginFile)
}
