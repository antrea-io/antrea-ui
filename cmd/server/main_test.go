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

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

func TestGinLoggerOmitsAuthQuery(t *testing.T) {
	var lines []string
	logger := funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ginLogger(logger, 0))
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/auth/oauth2/callback", ok)
	r.GET("/api/v1/foo", ok)

	for _, target := range []string{"/auth/oauth2/callback?code=secret&state=x", "/api/v1/foo?bar=1"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], `"path"="/auth/oauth2/callback"`)
	assert.NotContains(t, lines[0], "secret")
	assert.Contains(t, lines[1], `"path"="/api/v1/foo?bar=1"`)
}

func TestLogConfigRedactsSecrets(t *testing.T) {
	var lines []string
	logger := funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{Verbosity: 2})
	var c serverconfig.Config
	c.Auth.OIDC.ClientSecret = "very-secret"
	logConfig(logger, &c)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "<redacted>")
	assert.NotContains(t, lines[0], "very-secret")
}
