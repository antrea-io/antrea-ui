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

// Package authn resolves the caller's identity for every request that reaches the Antrea UI API.
package authn

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"

	"antrea.io/antrea-ui/pkg/auth/session"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/env"
	"antrea.io/antrea-ui/pkg/k8s"
	servererrors "antrea.io/antrea-ui/pkg/server/errors"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
)

// DevOrigin is the origin the frontend dev server runs on (see client/web/antrea-ui/.env.development).
// A strict Origin check would otherwise make local development impossible, since the dev server
// and the backend are on different origins.
const DevOrigin = "http://localhost:3000"

// ginContextKey is where the resolved identity is stashed on the gin context, in addition to the
// request context. Handlers that already have a *gin.Context can use RequestAuthFromGin.
const ginContextKey = "antrea-ui/request-auth"

// Config configures an Authenticator.
type Config struct {
	Store session.Store
	// ServerURL is the address the UI is served from (config.URL). It is only set when the
	// deployment declares one, which today is only required for OIDC.
	ServerURL    string
	CookieSecure bool
	// BearerFallbackEnabled mirrors auth.bearerToken.enabled: it allows a client to
	// authenticate with "Authorization: Bearer <k8s-token>" instead of a session cookie. It is
	// independent of the paste-a-token login mode (auth.serviceAccountToken.enabled), which
	// accepts the same credential but creates a session from it.
	BearerFallbackEnabled bool
	// BearerValidator checks a bearer token with the Kubernetes API server. It is required
	// whenever BearerFallbackEnabled is set: a bearer request has no login step, so this is the
	// only place its credential is ever checked. See bearerValidator.
	BearerValidator CredentialValidator
	// DevMode relaxes the CSRF gate and the cookie's SameSite attribute so that the frontend
	// dev server can talk to a backend on another port.
	DevMode bool
}

// Authenticator resolves the session (or ephemeral bearer credential) behind a request.
type Authenticator struct {
	logger logr.Logger
	config Config
	// serverOrigin is the scheme://host form of Config.ServerURL, or "" if none was set.
	serverOrigin string
	// bearer validates Authorization: Bearer tokens. Nil when the fallback is disabled.
	bearer *bearerValidator
}

// New builds an Authenticator. It fails if the configured server URL cannot be parsed, or if the
// bearer fallback is enabled with no way to validate the tokens it would accept.
func New(logger logr.Logger, config Config) (*Authenticator, error) {
	a := &Authenticator{logger: logger, config: config}
	if config.BearerFallbackEnabled {
		if config.BearerValidator == nil {
			// Fail at startup rather than accept unvalidated tokens at runtime. There is
			// no safe default here: without a validator, every bearer token would be
			// believed, and the routes that never call Kubernetes would believe it to the
			// end.
			return nil, fmt.Errorf("bearer token authentication is enabled but no credential validator was provided")
		}
		bearer, err := newBearerValidator(config.BearerValidator)
		if err != nil {
			return nil, err
		}
		a.bearer = bearer
	}
	if config.ServerURL != "" {
		u, err := url.Parse(config.ServerURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid server URL '%s'", config.ServerURL)
		}
		a.serverOrigin = u.Scheme + "://" + u.Host
	}
	if config.DevMode {
		logger.Info("WARNING: development mode is enabled. The CSRF origin check accepts " +
			DevOrigin + " and the session cookie is set with SameSite=Lax instead of Strict. " +
			"Never run a production deployment with APP_ENV=dev.")
	}
	return a, nil
}

// NewFromServerConfig builds an Authenticator from the parsed server configuration.
//
// It does not yet wire up the bearer fallback (auth.bearerToken.enabled): that requires a
// config.Auth.BearerToken field that lands with the rest of the server wiring.
func NewFromServerConfig(logger logr.Logger, config *serverconfig.Config, store session.Store) (*Authenticator, error) {
	return New(logger, Config{
		Store:        store,
		ServerURL:    config.URL,
		CookieSecure: config.Auth.CookieSecure,
		DevMode:      env.IsDevelopmentEnv(),
	})
}

// SameSite returns the SameSite mode to use for the session cookie.
//
// Strict already covers the common dev setup (frontend on :3000, backend on :8080): SameSite is
// keyed on the registrable domain, which ignores port, so "localhost" on both is the same site
// even though the origins differ. The checkCSRF Origin/Sec-Fetch-Site exemption for DevOrigin is
// what actually makes that case work. Lax is relaxed here anyway, in dev only, to also cover a
// dev setup where the frontend is served from a genuinely different host (a different site, not
// just a different port) — harmless, since DevMode is never meant for production.
func (a *Authenticator) SameSite() http.SameSite {
	if a.config.DevMode {
		return http.SameSiteLaxMode
	}
	return http.SameSiteStrictMode
}

// SetSessionCookie writes the session cookie for s.
func (a *Authenticator) SetSessionCookie(c *gin.Context, s *session.Session) {
	cookieutils.SetSessionCookie(c.Writer, s.ID(), a.config.CookieSecure, a.SameSite())
}

// ClearSessionCookie clears the session cookie.
func (a *Authenticator) ClearSessionCookie(c *gin.Context) {
	cookieutils.UnsetSessionCookie(c.Writer)
}

// ClearLegacyCookies clears the cookies used by the authentication scheme that predates
// server-side sessions. They are scoped to Path=/auth, so the Path=/ session cookie does not
// overwrite them and a browser that was logged in before the upgrade would otherwise keep them
// indefinitely.
func (a *Authenticator) ClearLegacyCookies(c *gin.Context) {
	cookieutils.UnsetLegacyCookies(c.Request, c.Writer)
}

// SessionIDFromRequest returns the session ID in the request cookie, without validating it.
func (a *Authenticator) SessionIDFromRequest(c *gin.Context) (string, bool) {
	return cookieutils.GetSessionCookie(c.Request)
}

// Middleware authenticates a request or rejects it. On success the resolved identity is available
// from the request context (session.RequestAuthFrom) and from the gin context
// (RequestAuthFromGin).
func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ra, sError := a.Resolve(c)
		if sError != nil {
			servererrors.HandleError(c, sError)
			servererrors.LogError(a.logger, sError, "Failed to authenticate request", "path", c.Request.URL.Path)
			c.Abort()
			return
		}
		a.Attach(c, ra)
		c.Next()
	}
}

// Attach makes ra visible to downstream handlers, both through the request context (which is what
// the K8s proxy and the Antrea Service client read) and the gin context.
func (a *Authenticator) Attach(c *gin.Context, ra *session.RequestAuth) {
	c.Request = c.Request.WithContext(session.WithRequestAuth(c.Request.Context(), ra))
	c.Set(ginContextKey, ra)
}

// RequestAuthFromGin returns the identity resolved for this request by the middleware.
func RequestAuthFromGin(c *gin.Context) (*session.RequestAuth, bool) {
	v, ok := c.Get(ginContextKey)
	if !ok {
		return nil, false
	}
	ra, ok := v.(*session.RequestAuth)
	return ra, ok
}

// Resolve determines who is making this request.
//
// The session cookie always wins when both a cookie and an Authorization header are present, so
// that adding a Bearer header can never be used to bypass the CSRF gate that only applies to
// cookie-authenticated requests.
func (a *Authenticator) Resolve(c *gin.Context) (*session.RequestAuth, *servererrors.ServerError) {
	if id, ok := cookieutils.GetSessionCookie(c.Request); ok {
		if sError := a.checkCSRF(c); sError != nil {
			return nil, sError
		}
		s, err := a.config.Store.Get(c.Request.Context(), id)
		if err != nil {
			// The cookie names a session that no longer exists: idle-expired, past the
			// absolute cap, logged out elsewhere, or lost to a backend restart. None of
			// these are recoverable client-side, so clear the cookie and let the frontend
			// send the user back to the login page.
			a.ClearSessionCookie(c)
			a.ClearLegacyCookies(c)
			return nil, &servererrors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Session is no longer valid, please log in again",
				Err:     sessionLookupError(err),
			}
		}
		return session.NewSessionAuth(a.config.Store, s), nil
	}

	// A browser cannot attach an Authorization header to a cross-origin request without the
	// target opting in via CORS preflight, so bearer-authenticated requests are not exposed to
	// CSRF and are exempt from the origin check above.
	if token, ok := bearerToken(c.Request); ok {
		if !a.config.BearerFallbackEnabled {
			return nil, &servererrors.ServerError{
				Code:    http.StatusUnauthorized,
				Message: "Bearer token authentication is disabled (auth.bearerToken.enabled)",
			}
		}
		cred := session.Credential{Kind: session.KindBearer, Token: []byte(token)}
		// A bearer request has no login step, so this is where its credential is checked.
		// It cannot be left to the upstream call: /auth/session and the flow stream resolve
		// an identity and then never talk to Kubernetes at all.
		username, sError := a.bearer.validate(c.Request.Context(), c.Request, cred.Token)
		if sError != nil {
			return nil, sError
		}
		// Only now, after the API server has accepted the token, is its "exp" claim worth
		// anything: reading it off an unvalidated token would be trusting an attacker's own
		// assertion. It is what bounds a long-running request that never presents the
		// credential again - see RequestAuth.KeepAlive.
		cred.ExpiresAt = k8s.JWTExpiry(cred.Token)
		return session.NewEphemeralAuth(cred, username), nil
	}

	return nil, &servererrors.ServerError{
		Code:    http.StatusUnauthorized,
		Message: "No authentication present (session cookie / Authorization header)",
	}
}

// sessionLookupError keeps ErrNotFound and ErrExpired out of the error log (they are routine)
// while preserving genuine failures, e.g. a failed OIDC token refresh.
func sessionLookupError(err error) error {
	if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrExpired) {
		return nil
	}
	return err
}

func bearerToken(req *http.Request) (string, bool) {
	header := req.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// checkCSRF guards cookie-authenticated requests. SameSite=Strict on the session cookie is the
// primary defence; this is the second layer, for browsers or embeddings where SameSite does not
// behave as expected.
func (a *Authenticator) checkCSRF(c *gin.Context) *servererrors.ServerError {
	return a.checkCrossOrigin(c, false)
}

// checkCrossOrigin is the cross-origin gate. allowTopLevelNavigation relaxes it for routes that
// the user reaches by navigating there (see NavigationCSRFMiddleware).
func (a *Authenticator) checkCrossOrigin(c *gin.Context, allowTopLevelNavigation bool) *servererrors.ServerError {
	origin := c.GetHeader("Origin")

	// Development mode: the frontend dev server is a different origin from the backend by
	// construction, so neither Sec-Fetch-Site nor Origin can match.
	if a.config.DevMode && origin == DevOrigin {
		return nil
	}

	reject := func(reason string) *servererrors.ServerError {
		return &servererrors.ServerError{
			Code:    http.StatusForbidden,
			Message: "Cross-origin request rejected",
			Err:     fmt.Errorf("cross-origin request rejected: %s", reason),
		}
	}

	// Sec-Fetch-Site is set by the browser and cannot be forged by script, so it is the more
	// reliable signal when present.
	if fetchSite := c.GetHeader("Sec-Fetch-Site"); fetchSite != "" {
		// "none" means a user-initiated navigation (typing a URL, a bookmark).
		if fetchSite == "same-origin" || fetchSite == "none" {
			return nil
		}
		if allowTopLevelNavigation && isTopLevelNavigation(c) {
			return nil
		}
		return reject(fmt.Sprintf("Sec-Fetch-Site is %q", fetchSite))
	}

	// No Origin header at all means a non-browser client (curl, a controller), which is not a
	// CSRF vector: the browser is what would attach the cookie automatically.
	if origin == "" {
		return nil
	}
	if a.originAllowed(c, origin) {
		return nil
	}
	return reject(fmt.Sprintf("Origin is %q", origin))
}

// isTopLevelNavigation reports whether the browser says this request is the user navigating the
// top-level document here (following a link, or the frontend setting window.location), rather
// than a subresource load or a script-issued fetch.
func isTopLevelNavigation(c *gin.Context) bool {
	return c.GetHeader("Sec-Fetch-Mode") == "navigate" && c.GetHeader("Sec-Fetch-Dest") == "document"
}

// CSRFMiddleware runs the cross-origin gate on its own, for routes that do not go through Resolve
// and so are not covered by the check inside it.
//
// The login endpoints need this: Resolve only gates requests that already carry a session cookie,
// which the request that *creates* the session does not. Without it, a page on another origin can
// POST a credential of its choosing to /auth/login/token and have the victim's browser store the
// resulting session cookie - the victim then browses as the attacker's Kubernetes identity, and
// whatever they do lands under the attacker's account.
func (a *Authenticator) CSRFMiddleware() gin.HandlerFunc {
	return a.csrfMiddleware(false)
}

// NavigationCSRFMiddleware is CSRFMiddleware relaxed to also accept a cross-site *top-level
// navigation*, for a route the user reaches by being sent there rather than by script. Logout is
// the only one: the frontend performs it with window.location, which is a cross-origin navigation
// whenever the frontend and the backend are on different origins (local development, and any
// deployment that does not front both with one nginx).
//
// What this still blocks is the silent forms of the same request - an <img> tag, an iframe, a
// script's fetch() - which is what makes a forced logout worth mounting in the first place. A
// cross-site link or form that navigates the user to the logout URL is not blocked, and cannot
// usefully be: the user ends up looking at the page, which is the same trade-off every logout
// link makes.
func (a *Authenticator) NavigationCSRFMiddleware() gin.HandlerFunc {
	return a.csrfMiddleware(true)
}

func (a *Authenticator) csrfMiddleware(allowTopLevelNavigation bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sError := a.checkCrossOrigin(c, allowTopLevelNavigation); sError != nil {
			servererrors.HandleError(c, sError)
			servererrors.LogError(a.logger, sError, "Rejected cross-origin request", "path", c.Request.URL.Path)
			c.Abort()
			return
		}
		c.Next()
	}
}

// SafeRedirectURL returns raw if it is a target this deployment is willing to send a browser to,
// and "" otherwise. A redirect target comes from the query string, so without this an attacker
// can use antrea-ui's own URL to bounce a victim anywhere ("open redirect"), which is exactly
// what makes a phishing link look legitimate.
//
// Accepted: an absolute-path reference ("/foo?bar"), or an http(s) URL on antrea-ui's own origin.
// Everything else - another host, a protocol-relative "//evil.example.com", a "javascript:" URL -
// is refused.
func (a *Authenticator) SafeRedirectURL(c *gin.Context, raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme == "" && u.Host == "" {
		// A relative reference. It must start with exactly one "/": "//evil.example.com" is
		// protocol-relative and leaves the origin, and a path with no leading "/" is
		// resolved against the current one, which is not something we want to guess at.
		if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return ""
		}
		// net/url is not the parser that matters here: the browser is. Per the WHATWG URL
		// spec, a browser resolving a relative reference against an http(s) base treats "\"
		// exactly like "/" in the path-start state, so "/\evil.example.com" is
		// protocol-relative to a browser even though net/url parses it as an ordinary path
		// with an empty Host. Any backslash is refused rather than only the one in second
		// position: a legitimate redirect target has no reason to contain one, and that
		// leaves no parser-divergence edge cases to reason about.
		if strings.Contains(raw, `\`) {
			return ""
		}
		return raw
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if a.originAllowed(c, u.Scheme+"://"+u.Host) {
		return raw
	}
	return ""
}

func (a *Authenticator) originAllowed(c *gin.Context, origin string) bool {
	if a.serverOrigin != "" {
		return strings.EqualFold(origin, a.serverOrigin)
	}
	// config.URL is only required for OIDC, so a basic-auth-only deployment has nothing to
	// compare against. Fall back to the request's own Host, which is correct for the
	// single-origin nginx deployment (the SPA and the /api and /auth proxies are all served
	// from one server block) - the only supported topology.
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, c.Request.Host)
}
