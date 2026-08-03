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
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/k8s"
	servererrors "antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/ratelimit"
)

// maxLoginBodySize caps the size of a login request body. A kubeconfig is the largest thing we
// accept and is a few KB at most.
const maxLoginBodySize = 256 * 1024

// createSession registers the session, sets the cookie, and clears the cookies left over from the
// previous authentication scheme.
//
// On any failure the credential is zeroed here: it must not survive a rejected login.
func (s *Server) createSession(c *gin.Context, spec *session.Spec) *servererrors.ServerError {
	// Logging in again replaces whatever session the caller already had, so drop the old one
	// now instead of leaving it to idle out. It is unreachable either way (the cookie is about
	// to be overwritten), but until it expires it holds a slot against MaxSessions and keeps a
	// credential in memory that nothing can use.
	if oldID, ok := s.authenticator.SessionIDFromRequest(c); ok {
		s.sessionStore.Delete(oldID)
	}

	sess, err := s.sessionStore.Create(spec)
	if err != nil {
		spec.Zero()
		if errors.Is(err, session.ErrTooManySessions) {
			return &servererrors.ServerError{
				Code:    http.StatusServiceUnavailable,
				Message: "Too many active sessions, please try again later",
			}
		}
		return &servererrors.ServerError{
			Code: http.StatusInternalServerError,
			Err:  fmt.Errorf("error when creating session: %w", err),
		}
	}
	s.authenticator.SetSessionCookie(c, sess)
	// A browser that was logged in before the upgrade still holds the old Path=/auth cookies,
	// which the new Path=/ session cookie does not overwrite.
	s.authenticator.ClearLegacyCookies(c)
	return nil
}

// validateCredential asks the API server who this credential is, which both rejects a bad paste at
// login time and gives us a username to display.
func (s *Server) validateCredential(c *gin.Context, cred *session.Credential) (string, *servererrors.ServerError) {
	username, err := s.clientFactory.ValidateCredential(c.Request.Context(), cred)
	if err != nil {
		// The error may quote the request the API server rejected, so it is logged, not
		// returned to the caller.
		return "", &servererrors.ServerError{
			Code:    http.StatusUnauthorized,
			Message: "Kubernetes rejected this credential",
			Err:     fmt.Errorf("credential validation failed: %w", err),
		}
	}
	return username, nil
}

// Login handles POST /auth/login: the static admin password (mode 4).
//
// The Kubernetes identity is unchanged from before the session rework: calls made by this session
// are impersonated as the antrea-ui-admin ServiceAccount, so every admin-password user has exactly
// the same cluster access.
func (s *Server) Login(c *gin.Context) {
	if sError := func() *servererrors.ServerError {
		user, password, ok := c.Request.BasicAuth()
		if !ok {
			return &servererrors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Basic Auth required",
			}
		}
		if user != "admin" {
			return &servererrors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Must authenticate as admin",
			}
		}
		if err := s.passwordStore.Compare(c, []byte(password)); err != nil {
			return &servererrors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Invalid admin password",
			}
		}
		return s.createSession(c, &session.Spec{
			Mode:     session.ModeAdmin,
			Username: user,
			Credential: session.Credential{
				Kind:     session.KindImpersonate,
				UserName: s.config.AdminUserName,
			},
		})
	}(); sError != nil {
		servererrors.HandleError(c, sError)
		s.LogError(sError, "Failed to login")
		return
	}
	c.Status(http.StatusOK)
}

// LoginWithToken handles POST /auth/login/token: a pasted Kubernetes bearer token (mode 5).
func (s *Server) LoginWithToken(c *gin.Context) {
	if sError := func() *servererrors.ServerError {
		var req apisv1.LoginTokenRequest
		if err := c.BindJSON(&req); err != nil {
			return &servererrors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Invalid request body",
			}
		}
		cred, err := k8s.BearerCredential([]byte(req.Token))
		// Drop our copy of the request struct's token: the credential now owns the bytes.
		req.Token = ""
		if err != nil {
			return &servererrors.ServerError{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			}
		}
		username, sError := s.validateCredential(c, &cred)
		if sError != nil {
			cred.Zero()
			return sError
		}
		return s.createSession(c, &session.Spec{
			Mode:       session.ModeSAToken,
			Username:   username,
			Credential: cred,
		})
	}(); sError != nil {
		servererrors.HandleError(c, sError)
		s.LogError(sError, "Failed to login with token")
		return
	}
	c.Status(http.StatusOK)
}

// LoginWithKubeconfig handles POST /auth/login/kubeconfig (mode 3).
//
// The kubeconfig text is parsed for the current context's credential and then discarded; only the
// credential is retained, in the session.
func (s *Server) LoginWithKubeconfig(c *gin.Context) {
	if sError := func() *servererrors.ServerError {
		var req apisv1.LoginKubeconfigRequest
		if err := c.BindJSON(&req); err != nil {
			return &servererrors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Invalid request body",
			}
		}
		cred, err := k8s.CredentialFromKubeconfig([]byte(req.Kubeconfig))
		req.Kubeconfig = ""
		if err != nil {
			// These messages describe the *shape* of the kubeconfig (an exec plugin, a
			// file reference), never its contents.
			return &servererrors.ServerError{
				Code:    http.StatusBadRequest,
				Message: err.Error(),
			}
		}
		username, sError := s.validateCredential(c, &cred)
		if sError != nil {
			cred.Zero()
			return sError
		}
		return s.createSession(c, &session.Spec{
			Mode:       session.ModeKubeconfig,
			Username:   username,
			Credential: cred,
		})
	}(); sError != nil {
		servererrors.HandleError(c, sError)
		s.LogError(sError, "Failed to login with kubeconfig")
		return
	}
	c.Status(http.StatusOK)
}

// Session handles GET /auth/session. It is both the app-start "am I logged in?" probe and the
// keepalive the frontend pings while a tab is visible, so it goes through the normal resolution
// path and therefore bumps the session's last-seen time.
func (s *Server) Session(c *gin.Context) {
	ra, sError := s.authenticator.Resolve(c)
	if sError != nil {
		// 401 here simply means "not logged in"; it is the expected answer on a fresh visit.
		servererrors.HandleError(c, sError)
		return
	}
	info := apisv1.SessionInfo{
		Authenticated: true,
		Mode:          string(ra.Mode),
		Username:      ra.Username,
	}
	if sess := ra.Session(); sess != nil {
		expiresAt := sess.ExpiresAt()
		info.ExpiresAt = &expiresAt
	}
	c.JSON(http.StatusOK, info)
}

// Logout deletes the session (zeroing its credential) and clears the cookie. When the identity
// provider supports it, the user is then redirected to log out there too.
func (s *Server) Logout(c *gin.Context) {
	if sError := func() *servererrors.ServerError {
		// The redirect target comes from the query string, so it is only followed if it
		// stays on antrea-ui's own origin. Otherwise /auth/logout is an open redirect: a
		// link that starts on a URL the user recognizes and ends up wherever the attacker
		// chose.
		redirectURL := s.authenticator.SafeRedirectURL(c, c.Query("redirect_url"))

		// Read the OIDC id_token out of the session before deleting it: it is an input to the
		// provider's logout URL. Peek, not Get: logging out must not wait on (or fail because
		// of) a credential refresh whose result is about to be discarded anyway, and the
		// id_token is only ever used as a logout hint, which does not need to be currently valid.
		var idToken string
		if id, ok := s.authenticator.SessionIDFromRequest(c); ok {
			if sess, ok := s.sessionStore.Peek(id); ok {
				if sess.Mode() == session.ModeOIDC {
					idToken = string(sess.Credential().Token)
				}
			}
			s.sessionStore.Delete(id)
		}
		s.authenticator.ClearSessionCookie(c)
		s.authenticator.ClearLegacyCookies(c)

		if s.config.OIDCAuthEnabled && s.config.OIDCNeedsLogout && idToken != "" {
			logoutURL, err := s.oidcProvider.BuildLogoutURL(idToken)
			if err != nil {
				return &servererrors.ServerError{
					Code: http.StatusInternalServerError,
					Err:  fmt.Errorf("error when building OIDC logout URL: %w", err),
				}
			}
			c.Redirect(http.StatusSeeOther, logoutURL)
			return nil
		}
		if redirectURL != "" {
			c.Redirect(http.StatusFound, redirectURL)
		} else {
			c.Status(http.StatusOK)
		}
		return nil
	}(); sError != nil {
		servererrors.HandleError(c, sError)
		s.LogError(sError, "Failed to logout")
		return
	}
}

// loginRateLimitMiddleware builds the rate-limit middleware shared by every login endpoint. All of
// them accept attacker-controlled credential material, and the token and kubeconfig endpoints each
// make a SelfSubjectReview call to the API server, so an unlimited endpoint would be both a
// credential oracle and an amplification vector against the API server.
//
// Built once and shared across every login route: one budget per client IP, not one per route.
// ClientRateLimiter is safe for concurrent use, so the same instance can back every route's
// middleware chain.
func (s *Server) loginRateLimitMiddleware() gin.HandlerFunc {
	if s.config.MaxLoginsPerSecond < 0 {
		return nil
	}
	const clientCacheSize = 10000
	burstSize := 0
	if s.config.MaxLoginsPerSecond > 0 {
		burstSize = 1
	}
	loginRateLimiter := ratelimit.NewClientRateLimiterOrDie(
		fmt.Sprintf("%d/s", s.config.MaxLoginsPerSecond), burstSize, clientCacheSize, ratelimit.ClientKeyIP)
	return ratelimit.Middleware(loginRateLimiter)
}

// loginHandlers builds the middleware chain for a login endpoint.
//
// The cross-origin gate has to be applied explicitly here. The one inside authn.Resolve only
// covers requests that already carry a session cookie, which a login request does not, so without
// this a page on another origin could POST a credential of its choosing and have the victim's
// browser keep the resulting session.
func (s *Server) loginHandlers(rateLimitMiddleware gin.HandlerFunc, handler gin.HandlerFunc) []gin.HandlerFunc {
	handlers := []gin.HandlerFunc{s.authenticator.CSRFMiddleware(), limitBodySize(maxLoginBodySize)}
	if rateLimitMiddleware != nil {
		handlers = append(handlers, rateLimitMiddleware)
	}
	return append(handlers, handler)
}

// limitBodySize caps how much of a login request body the server will read. It also keeps the
// uploaded kubeconfig out of any request logging: the body is only ever consumed by the handler
// that parses it.
func limitBodySize(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

func (s *Server) AddAuthRoutes(r *gin.RouterGroup) {
	r = r.Group("/auth")
	rateLimitMiddleware := s.loginRateLimitMiddleware()
	if s.config.BasicAuthEnabled {
		r.POST("/login", s.loginHandlers(rateLimitMiddleware, s.Login)...)
	}
	if s.config.SATokenAuthEnabled {
		r.POST("/login/token", s.loginHandlers(rateLimitMiddleware, s.LoginWithToken)...)
	}
	if s.config.KubeconfigAuthEnabled {
		r.POST("/login/kubeconfig", s.loginHandlers(rateLimitMiddleware, s.LoginWithKubeconfig)...)
	}
	r.GET("/session", s.Session)
	// Logout destroys a session, so it needs the same gate the login endpoints do. The GET form
	// is what the frontend uses, via window.location, so it takes the variant that still allows
	// a top-level navigation; the POST form has no such constraint.
	r.GET("/logout", s.authenticator.NavigationCSRFMiddleware(), s.Logout)
	r.POST("/logout", s.authenticator.CSRFMiddleware(), s.Logout)
	if s.config.OIDCAuthEnabled {
		s.logger.Info("Adding OAuth2 routes")
		s.AddOAuth2Routes(r)
	}
}
