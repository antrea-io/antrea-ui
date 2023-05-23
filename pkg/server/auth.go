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
	"antrea.io/antrea-ui/pkg/server/ratelimit"
)

func (s *server) Login(c *gin.Context) {
	if sError := func() *serverError {
		user, password, ok := c.Request.BasicAuth()
		if !ok {
			return &serverError{
				code:    http.StatusUnauthorized,
				message: "Basic Auth required",
			}
		}
		if user != "admin" {
			return &serverError{
				code:    http.StatusUnauthorized,
				message: "Must authenticate as admin",
			}
		}
		if err := s.passwordStore.Compare(c, []byte(password)); err != nil {
			return &serverError{
				code:    http.StatusUnauthorized,
				message: "Invalid admin password",
			}
		}
		refreshToken, err := s.tokenManager.GetRefreshToken()
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting JWT refresh token: %w", err),
			}
		}
		token, err := s.tokenManager.GetToken()
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting JWT token: %w", err),
			}
		}
		resp := apisv1alpha1.Token{
			AccessToken: token.Raw,
			TokenType:   "Bearer",
			ExpiresIn:   int64(token.ExpiresIn / time.Second),
		}
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     "antrea-ui-refresh-token",
			Value:    refreshToken.Raw,
			Path:     "/api/v1/auth",
			Domain:   "",
			MaxAge:   0, // make it a session cookie
			Secure:   s.config.CookieSecure,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		c.JSON(http.StatusOK, resp)
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to login")
		return
	}
}

func (s *server) RefreshToken(c *gin.Context) {
	if sError := func() *serverError {
		// /refresh supports both the Authorization header and the token cookie, giving
		// priority to the Authorization header
		var refreshToken string
		auth := c.GetHeader("Authorization")
		if auth != "" {
			t := strings.Split(auth, " ")
			if len(t) != 2 || t[0] != "Bearer" {
				return &serverError{
					code:    http.StatusUnauthorized,
					message: "Authorization header does not have valid format",
				}
			}
			refreshToken = t[1]
		} else {
			cookie, err := c.Request.Cookie("antrea-ui-refresh-token")
			if err != nil {
				return &serverError{
					code:    http.StatusUnauthorized,
					message: "Missing 'antrea-ui-refresh-token' cookie",
					err:     err,
				}
			}
			refreshToken = cookie.Value
		}
		if err := s.tokenManager.VerifyRefreshToken(refreshToken); err != nil {
			return &serverError{
				code:    http.StatusUnauthorized,
				message: "Invalid refresh token",
				err:     err,
			}
		}
		token, err := s.tokenManager.GetToken()
		if err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when getting JWT token: %w", err),
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
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to refresh token")
		return
	}
}

func (s *server) Logout(c *gin.Context) {
	if sError := func() *serverError {
		cookie, err := c.Request.Cookie("antrea-ui-refresh-token")
		if err != nil {
			// no cookie
			return nil
		}
		refreshToken := cookie.Value
		s.tokenManager.DeleteRefreshToken(refreshToken)
		cookie.Value = ""
		cookie.MaxAge = -1
		http.SetCookie(c.Writer, cookie)
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to logout")
		return
	}
	c.Status(http.StatusOK)
}

func (s *server) AddAuthRoutes(r *gin.RouterGroup) {
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
