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
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/ratelimit"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

func (s *Server) Login(c *gin.Context) {
	if sError := func() *errors.ServerError {
		user, password, ok := c.Request.BasicAuth()
		if !ok {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Basic Auth required",
			}
		}
		if user != "admin" {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Must authenticate as admin",
			}
		}
		if err := s.passwordStore.Compare(c, []byte(password)); err != nil {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Invalid admin password",
			}
		}
		refreshToken, err := s.tokenManager.GetRefreshToken()
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting JWT refresh token: %w", err),
			}
		}
		token, err := s.tokenManager.GetToken()
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting JWT token: %w", err),
			}
		}
		resp := apisv1alpha1.Token{
			AccessToken: token.Raw,
			TokenType:   "Bearer",
			ExpiresIn:   int64(token.ExpiresIn / time.Second),
		}
		cookieutils.SetRefreshTokenCookie(c.Writer, refreshToken.Raw, s.config.CookieSecure)
		c.JSON(http.StatusOK, resp)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to login")
		return
	}
}

func (s *Server) RefreshToken(c *gin.Context) {
	if sError := func() *errors.ServerError {
		// /refresh supports both the Authorization header and the token cookie, giving
		// priority to the Authorization header
		var refreshToken string
		auth := c.GetHeader("Authorization")
		if auth != "" {
			t := strings.Split(auth, " ")
			if len(t) != 2 || t[0] != "Bearer" {
				return &errors.ServerError{
					Code:    http.StatusUnauthorized,
					Message: "Authorization header does not have valid format",
				}
			}
			refreshToken = t[1]
		} else if token, ok := cookieutils.GetRefreshTokenFromCookie(c.Request); ok {
			refreshToken = token
		} else {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "No authentication present (cookie / Authorization header)",
			}
		}
		if err := s.tokenManager.VerifyRefreshToken(refreshToken); err != nil {
			return &errors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Invalid refresh token",
				Err:     err,
			}
		}
		token, err := s.tokenManager.GetToken()
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting JWT token: %w", err),
			}
		}
		resp := apisv1alpha1.Token{
			AccessToken: token.Raw,
			TokenType:   "Bearer",
			ExpiresIn:   int64(token.ExpiresIn / time.Second),
		}
		c.JSON(http.StatusOK, resp)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to refresh token")
		return
	}
}

func (s *Server) Logout(c *gin.Context) {
	if sError := func() *errors.ServerError {
		refreshToken, ok := cookieutils.UnsetRefreshTokenCookie(c.Request, c.Writer)
		if ok {
			s.tokenManager.DeleteRefreshToken(refreshToken)
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to logout")
		return
	}
	c.Status(http.StatusOK)
}

func (s *Server) AddAuthRoutes(r *gin.RouterGroup) {
	r = r.Group("/auth")
	loginHandlers := []gin.HandlerFunc{}
	if s.config.MaxLoginsPerSecond >= 0 {
		const clientCacheSize = 10000
		burstSize := 0
		if s.config.MaxLoginsPerSecond > 0 {
			burstSize = 1
		}
		loginRateLimiter := ratelimit.NewClientRateLimiterOrDie(fmt.Sprintf("%d/s", s.config.MaxLoginsPerSecond), burstSize, clientCacheSize, ratelimit.ClientKeyIP)
		loginHandlers = append(loginHandlers, ratelimit.Middleware(loginRateLimiter))
	}
	loginHandlers = append(loginHandlers, s.Login)
	r.POST("/login", loginHandlers...)
	r.GET("/refresh_token", s.RefreshToken)
	r.POST("/logout", s.Logout)
}
