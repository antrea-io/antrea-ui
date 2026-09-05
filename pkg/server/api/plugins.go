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
	"bytes"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetPluginsIndex serves the list of frontend plugins currently known to the
// backend, discovered from labeled ConfigMaps and/or a configured plugin
// directory (see pkg/plugins). Like /settings, this is unauthenticated: it
// carries no confidential data, and the frontend needs it before a user has
// authenticated.
func (s *Server) GetPluginsIndex(c *gin.Context) {
	// The set of installed plugins can change at any time (a ConfigMap or a plugin directory
	// entry can be created, updated, or deleted independently of antrea-ui), so this must
	// never be cached - an aggressively-caching browser or proxy would otherwise keep serving
	// a stale index (or worse, a stale plugin bundle below) after an upgrade, with no visible
	// error.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, s.pluginRegistry.Index())
}

// GetPluginFile serves one file from a plugin's bundle.zip, so the frontend can import() it at
// runtime.
func (s *Server) GetPluginFile(c *gin.Context) {
	name := c.Param("name")
	// filename becomes a real filesystem path join for a directory-sourced plugin (see
	// pkg/plugins.pluginEntry.open) - pkg/plugins.safeJoin is what actually neutralizes a
	// "../.." segment, not any property of this string itself.
	filename := strings.TrimPrefix(c.Param("filepath"), "/")
	rc, size, ok := s.pluginRegistry.File(name, filename)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	defer rc.Close()

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", pluginFileContentType(filename))
	// This route is unauthenticated and same-origin with the UI, and its response's Content-Type
	// can be image/svg+xml (see pluginFileContentType) - an SVG document can carry a <script>, so
	// without these two headers a plugin bundle containing a malicious SVG would execute script on
	// the antrea-ui origin as soon as someone navigates to its URL directly, before any
	// authentication and without the plugin ever being import()ed. nosniff stops a browser from
	// ignoring Content-Type and sniffing script/HTML out of some other type instead; the sandboxed
	// CSP denies the response's own declared type the ability to execute anything, script tag or
	// not.
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox")

	// A plugin author may store an already gzip-compressed file (sizeable once bundled with
	// dependencies) in the bundle - detected here by its magic number rather than a naming
	// convention, so no manifest/API changes are needed to opt in. Browsers decompress
	// fetch()/import() responses transparently on this header. Peeking at the first two bytes
	// works the same whether rc is backed by an in-memory []byte or a real file - io.MultiReader
	// below hands them back for the actual copy either way, so this never has to buffer the
	// whole file to make the decision.
	var magic [2]byte
	peeked, err := io.ReadFull(rc, magic[:])
	// io.EOF/io.ErrUnexpectedEOF just mean the file is shorter than len(magic) (0 or 1 bytes),
	// which is a legitimate file, not a read failure. Anything else is a genuine error, and since
	// nothing has been written to c.Writer yet, it can still be reported as a 500 instead of
	// committing headers/status for a file we then fail to fully read.
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		c.Status(http.StatusInternalServerError)
		return
	}
	if peeked == 2 && isGzip(magic[:peeked]) {
		c.Header("Content-Encoding", "gzip")
	}
	if size > 0 {
		c.Header("Content-Length", strconv.FormatInt(size, 10))
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, io.MultiReader(bytes.NewReader(magic[:peeked]), rc))
}

func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

func pluginFileContentType(filename string) string {
	switch path.Ext(filename) {
	case ".js", ".mjs":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".css":
		return "text/css"
	// A bundle.zip's assets/ subdirectory (see docs/plugins.md) can hold anything a plugin's UI
	// references by a relative runtime URL rather than pulls into its JS module graph - most
	// commonly images and fonts. application/octet-stream (the default below) makes a browser
	// treat the response as an opaque download rather than the image/font it actually is - e.g.
	// an <img> tag renders a broken-image placeholder instead of the icon.
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) AddPluginsRoutes(r *gin.RouterGroup) {
	r = r.Group("/plugins")
	r.GET("/index.json", s.GetPluginsIndex)
	r.GET("/:name/*filepath", s.GetPluginFile)
}
