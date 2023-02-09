package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
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
			MaxAge:   int(refreshToken.ExpiresIn / time.Second),
			Secure:   s.config.cookieSecure,
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
		cookie, err := c.Request.Cookie("antrea-ui-refresh-token")
		if err != nil {
			return &serverError{
				code:    http.StatusUnauthorized,
				message: "Missing 'antrea-ui-refresh-token' cookie",
				err:     err,
			}
		}
		refreshToken := cookie.Value
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
	r.GET("/login", s.Login)
	r.GET("/refresh_token", s.RefreshToken)
	r.GET("/logout", s.Logout)
}
