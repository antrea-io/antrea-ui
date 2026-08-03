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
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
	"golang.org/x/oauth2"

	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/server/errors"
	"antrea.io/antrea-ui/pkg/server/utils/template"
	"antrea.io/antrea-ui/pkg/utils/random"
)

func genRandomBytes(bytes int) ([]byte, error) {
	return random.Bytes(bytes)
}

func genNonce() (string, error) {
	return random.HexString(32)
}

func hashOIDCNonce(nonce string) string {
	h := sha256.New()
	h.Write([]byte(nonce))
	b64 := base64.URLEncoding.EncodeToString(h.Sum(nil))
	return b64
}

type OIDCProvider struct {
	logger            logr.Logger
	serverURL         string
	issuerURL         string
	discoveryURL      string
	clientID          string
	clientSecret      string
	callbackURL       string
	logoutURLTemplate *template.Template
	logoutReturnURL   string
	scopes            []string
	provider          *oidc.Provider
	verifier          *oidc.IDTokenVerifier
	oauth2StateSecret []byte
}

// DefaultOIDCScopes are the scopes requested when none are configured.
//
// "offline_access" is what makes the provider issue a refresh token, without which a session could
// not outlive the id_token. "groups" populates the group claims that group-based Kubernetes RBAC
// needs. "email" is here because --oidc-username-claim=email is the most common apiserver
// configuration by a wide margin, and a provider omits the claim entirely if the scope was not
// requested - which surfaces as an opaque "claim not present" rejection at login.
var DefaultOIDCScopes = []string{oidc.ScopeOpenID, "email", "groups", "offline_access"}

func NewOIDCProvider(
	logger logr.Logger,
	serverURL string,
	issuerURL string,
	discoveryURL string,
	clientID string,
	clientSecret string,
	logoutURLTemplate string,
	scopes []string,
) (*OIDCProvider, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL '%s'", serverURL)
	}

	callbackURL := u.JoinPath("auth", "oauth2", "callback").String()
	logoutReturnURL := u
	logoutReturnURL.RawQuery = url.Values{
		"msg": []string{"You successfully logged out from the OIDC provider"},
	}.Encode()

	// we use a key with the same size as the block
	secret, err := genRandomBytes(sha256.BlockSize)
	if err != nil {
		return nil, fmt.Errorf("error when generating secret for OAuth2 state: %w", err)
	}

	// this will work fine if logoutURL is an empty string, no need for a special case
	tpl, err := template.New(logoutURLTemplate, []string{"Token", "ClientID", "URL", "LogoutReturnURL"})
	if err != nil {
		return nil, fmt.Errorf("logout URL is not a valid template: %w", err)
	}

	if len(scopes) == 0 {
		scopes = DefaultOIDCScopes
	}

	return &OIDCProvider{
		logger:            logger,
		serverURL:         serverURL,
		issuerURL:         issuerURL,
		discoveryURL:      discoveryURL,
		clientID:          clientID,
		clientSecret:      clientSecret,
		callbackURL:       callbackURL,
		logoutURLTemplate: tpl,
		logoutReturnURL:   logoutReturnURL.String(),
		scopes:            scopes,
		oauth2StateSecret: secret,
	}, nil
}

func (p *OIDCProvider) Init(ctx context.Context) error {
	logger := p.logger
	const initialWait = 1 * time.Second
	const maxWait = 10 * time.Second
	wait := initialWait
	var provider *oidc.Provider

	discoveryURL := p.issuerURL
	if p.discoveryURL != "" && p.discoveryURL != p.issuerURL {
		logger.Info("OIDC discoveryURL is different from issuerURL")
		ctx = oidc.InsecureIssuerURLContext(ctx, p.issuerURL)
		discoveryURL = p.discoveryURL
	}

	for {
		var err error
		provider, err = oidc.NewProvider(ctx, discoveryURL)
		if err != nil {
			logger.Error(err, "OIDC discovery failed, retrying after backoff", "wait", wait.String())
		} else {
			logger.Info("OIDC discovery succeeded", "issuer", p.issuerURL)
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to initialize OIDC provider with OIDC discovery: %w", ctx.Err())
		case <-time.After(wait):
			wait = 2 * wait
			if wait > maxWait {
				wait = maxWait
			}
		}
	}

	p.provider = provider
	p.verifier = provider.Verifier(&oidc.Config{ClientID: p.clientID})
	return nil
}

func (p *OIDCProvider) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.clientID,
		ClientSecret: p.clientSecret,
		RedirectURL:  p.callbackURL,
		// Discovery returns the OAuth2 endpoints.
		Endpoint: p.provider.Endpoint(),
		Scopes:   p.scopes,
	}
}

func (p *OIDCProvider) Verify(ctx context.Context, rawIDToken string) (*oidc.IDToken, error) {
	return p.verifier.Verify(ctx, rawIDToken)
}

// Scopes returns the scopes requested from the provider.
func (p *OIDCProvider) Scopes() []string {
	return p.scopes
}

// Refresher returns the session.Refresher that renews an id_token before it expires, using the
// refresh token stored alongside it. Without this, an OIDC session would end as soon as the
// id_token did - typically after a few minutes.
func (p *OIDCProvider) Refresher() session.Refresher {
	return &oidcRefresher{provider: p}
}

type oidcRefresher struct {
	provider *OIDCProvider
}

func (r *oidcRefresher) Refresh(ctx context.Context, refreshToken []byte) (session.Credential, []byte, error) {
	fail := func(format string, args ...interface{}) (session.Credential, []byte, error) {
		return session.Credential{}, nil, fmt.Errorf(format, args...)
	}
	if len(refreshToken) == 0 {
		return fail("session has no refresh token")
	}
	// The token has no access token and no expiry, so the source treats it as expired and
	// performs the refresh_token grant immediately.
	tokenSource := r.provider.OAuth2Config().TokenSource(ctx, &oauth2.Token{RefreshToken: string(refreshToken)})
	newToken, err := tokenSource.Token()
	if err != nil {
		// The provider's error can echo the refresh token back, so it is not wrapped.
		return fail("refresh token was rejected by the OIDC provider")
	}
	rawIDToken, ok := newToken.Extra("id_token").(string)
	if !ok {
		return fail("no id_token in refresh response")
	}
	idToken, err := r.provider.Verify(ctx, rawIDToken)
	if err != nil {
		return fail("failed to verify refreshed id_token")
	}
	// Providers that rotate refresh tokens return a new one; those that do not return the same
	// one (or none), in which case the session keeps what it has.
	var newRefreshToken []byte
	if newToken.RefreshToken != "" && newToken.RefreshToken != string(refreshToken) {
		newRefreshToken = []byte(newToken.RefreshToken)
	}
	return session.Credential{
		Kind:      session.KindBearer,
		Token:     []byte(rawIDToken),
		ExpiresAt: idToken.Expiry,
	}, newRefreshToken, nil
}

type oauth2State struct {
	Nonce       string `json:"nonce"`
	RedirectURL string `json:"redirectURL"`
}

func (p *OIDCProvider) GetOAuth2State(redirectURL string) (*oauth2State, string, error) {
	state := &oauth2State{
		RedirectURL: redirectURL,
	}
	nonce, err := genNonce()
	if err != nil {
		return nil, "", err
	}
	state.Nonce = nonce
	b, err := json.Marshal(state)
	if err != nil {
		return nil, "", err
	}
	b64 := base64.URLEncoding.EncodeToString(b)
	h := hmac.New(sha256.New, p.oauth2StateSecret)
	h.Write([]byte(b64))
	raw := b64 + "." + base64.URLEncoding.EncodeToString(h.Sum(nil))
	return state, raw, nil
}

func (p *OIDCProvider) ParseOAuth2State(raw string) (*oauth2State, error) {
	s := strings.Split(raw, ".")
	if len(s) != 2 {
		return nil, fmt.Errorf("invalid format")
	}
	data, err := base64.URLEncoding.DecodeString(s[0])
	if err != nil {
		return nil, fmt.Errorf("invalid format")
	}
	signature, err := base64.URLEncoding.DecodeString(s[1])
	if err != nil {
		return nil, fmt.Errorf("invalid format")
	}
	h := hmac.New(sha256.New, p.oauth2StateSecret)
	h.Write([]byte(s[0]))
	if !hmac.Equal(signature, h.Sum(nil)) {
		return nil, fmt.Errorf("invalid signature")
	}
	var state oauth2State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid format")
	}
	return &state, nil
}

func (p *OIDCProvider) BuildLogoutURL(idToken string) (string, error) {
	inputs := map[string]string{
		"Token":           url.QueryEscape(idToken),
		"ClientID":        url.QueryEscape(p.clientID),
		"URL":             url.QueryEscape(p.serverURL),
		"LogoutReturnURL": url.QueryEscape(p.logoutReturnURL),
	}
	logoutURL, err := p.logoutURLTemplate.Replace(inputs)
	if err != nil {
		return "", err
	}
	// should we do more validation here?
	if _, err := url.Parse(logoutURL); err != nil {
		return "", fmt.Errorf("invalid logout URL: %w", err)
	}
	return logoutURL, nil
}

func (s *Server) OAuth2Login(c *gin.Context) {
	if sError := func() *errors.ServerError {
		// Validated before it is signed into the state, so the callback can redirect to it
		// without further checks. Unvalidated, it would make /auth/oauth2/login an open
		// redirect, and the signature would not help: an attacker crafting the link is the
		// one who supplies the value in the first place.
		redirectURL := s.authenticator.SafeRedirectURL(c, c.Query("redirect_url"))

		// See https://auth0.com/docs/secure/attack-protection/state-parameters
		// Our state is a JSON message which consists of a random nonce alongside
		// app-specific state (in our case, a redirect URL provided by the frontend). The
		// JSON message is serialized and signed using HMAC-SHA-256, to guarantee
		// integrity. The state is stored in an httpOnly secure cookie.
		state, raw, err := s.oidcProvider.GetOAuth2State(redirectURL)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when generating OAuth2 state: %w", err),
			}
		}
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     "antrea-ui-oauth2-state",
			Value:    raw,
			Path:     "/auth/oauth2",
			MaxAge:   0, // make it a session cookie
			Secure:   s.config.CookieSecure,
			HttpOnly: true,
			// It seems that we need Lax mode (instead of Strict) here, or the cookie
			// may not be present during the /callback.
			// 1. User visits Antrea UI and chooses to authenticate with OIDC
			// 2. The server sets this cookie
			// 3. User is redirected to OIDC provider (e.g., Auth0) and has to authenticate
			// 4. User is redirected to Antrea UI by OIDC provider
			// In the above scenario, because of the required user action in 3, the
			// "chain" of redirects is broken and the cookie will not be sent to server
			// in 4, unless the SameSite policy is set to Lax.
			// This is the accepted "solution" and is not a security risk here.
			SameSite: http.SameSiteLaxMode,
		})

		// From https://openid.net/specs/openid-connect-core-1_0-17_orig.html#NonceNotes
		// The nonce parameter value needs to include per-session state and be unguessable to attackers. One
		// method to achieve this for Web Server Clients is to store a cryptographically random value as an
		// HttpOnly session cookie and use a cryptographic hash of the value as the nonce parameter. In that
		// case, the nonce in the returned ID Token is compared to the hash of the session cookie to detect
		// ID Token replay by third parties. A related method applicable to JavaScript Clients is to store
		// the cryptographically random value in HTML5 local storage and use a cryptographic hash of this
		// value.
		oidcNonce, err := genNonce()
		oidcNonceHash := hashOIDCNonce(oidcNonce)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when generating OIDC nonce: %w", err),
			}
		}
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     "antrea-ui-oidc-nonce",
			Value:    oidcNonce,
			Path:     "/auth/oauth2",
			MaxAge:   0, // make it a session cookie
			Secure:   s.config.CookieSecure,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		authCodeURL := s.oidcProvider.OAuth2Config().AuthCodeURL(state.Nonce, oidc.Nonce(oidcNonceHash))
		c.Redirect(http.StatusSeeOther, authCodeURL)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to login")
		return
	}
}

func (s *Server) OAuth2Callback(c *gin.Context) {
	if sError := func() *errors.ServerError {
		state, ok := c.GetQuery("state")
		if !ok {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Missing state query param",
			}
		}

		stateCookie, err := c.Request.Cookie("antrea-ui-oauth2-state")
		if err != nil {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Missing OAuth2 state cookie",
			}
		}
		cs, err := s.oidcProvider.ParseOAuth2State(stateCookie.Value)
		if err != nil {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Invalid OAuth2 state cookie",
				Err:     err,
			}
		}
		if state != cs.Nonce {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "OAuth2 state mismatch",
			}
		}

		code, ok := c.GetQuery("code")
		if !ok {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Missing code query param",
			}
		}

		oauth2Token, err := s.oidcProvider.OAuth2Config().Exchange(c, code)
		if err != nil {
			return &errors.ServerError{
				// should we return Unauthorized here instead?
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when exchanging Code: %w", err),
			}
		}
		rawIDToken, ok := oauth2Token.Extra("id_token").(string)
		if !ok {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("no id_token in token response"),
			}
		}
		idToken, err := s.oidcProvider.Verify(c, rawIDToken)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("failed to verify id_token: %w", err),
			}
		}

		oidcNonceCookie, err := c.Request.Cookie("antrea-ui-oidc-nonce")
		if err != nil {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Missing OIDC nonce cookie",
			}
		}
		oidcNonceHash := hashOIDCNonce(oidcNonceCookie.Value)
		if idToken.Nonce != oidcNonceHash {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("invalid OIDC nonce"),
			}
		}

		// The id_token is the credential antrea-ui will present to the kube-apiserver on
		// this user's behalf, so it is kept (server-side, in the session) rather than
		// discarded. This is what makes the UI act as the real end user, and it is why mode 1
		// requires the kube-apiserver to be configured to trust the same issuer.
		var refreshTokenBytes []byte
		if oauth2Token.RefreshToken != "" {
			refreshTokenBytes = []byte(oauth2Token.RefreshToken)
		} else {
			// Without a refresh token the session cannot outlive the id_token, which is
			// typically minutes. Worth saying out loud, since it usually means the
			// "offline_access" scope was not granted.
			s.logger.Info("OIDC provider returned no refresh token: sessions will end when the id_token expires",
				"scopes", s.oidcProvider.Scopes())
		}

		spec := &session.Spec{
			Mode: session.ModeOIDC,
			Credential: session.Credential{
				Kind:      session.KindBearer,
				Token:     []byte(rawIDToken),
				ExpiresAt: idToken.Expiry,
			},
			RefreshToken: refreshTokenBytes,
			Refresher:    s.oidcProvider.Refresher(),
		}

		// Verifying the id_token only proves the *provider* issued it. It says nothing about
		// whether the kube-apiserver will accept it, which is what actually matters now that
		// the id_token is the credential we present upstream. So ask the API server, the same
		// way the token and kubeconfig modes do at login.
		//
		// Without this the misconfiguration is invisible and destructive: the session is
		// created, the first K8s call 401s, that 401 invalidates the session, and the user is
		// bounced back to the login page with no explanation - every time they log in.
		username, sError := s.validateCredential(c, &spec.Credential)
		if sError != nil {
			spec.Zero()
			// The generic "Kubernetes rejected this credential" is unhelpful here: the
			// overwhelmingly likely cause is one specific, fixable server-side mistake.
			sError.Message = "Kubernetes rejected the id_token issued by the OIDC provider. " +
				"The kube-apiserver must be configured to trust the same issuer and client ID " +
				"as Antrea UI (--oidc-issuer-url / --oidc-client-id, or the equivalent " +
				"structured authentication configuration). See docs/oidc.md."
			return sError
		}
		spec.Username = username

		if sError := s.createSession(c, spec); sError != nil {
			return sError
		}

		// at this point, it seems reasonable to delete the cookies used for oauth2
		http.SetCookie(c.Writer, &http.Cookie{
			Name:   "antrea-ui-oauth2-state",
			Value:  "",
			Path:   "/auth/oauth2",
			MaxAge: -1,
		})
		http.SetCookie(c.Writer, &http.Cookie{
			Name:   "antrea-ui-oidc-nonce",
			Value:  "",
			Path:   "/auth/oauth2",
			MaxAge: -1,
		})

		var redirectURL *url.URL
		if cs.RedirectURL != "" {
			var err error
			redirectURL, err = url.Parse(cs.RedirectURL)
			if err != nil {
				redirectURL = &url.URL{
					Path: "/",
				}
			}
		} else {
			redirectURL = &url.URL{
				Path: "/",
			}
		}
		q := redirectURL.Query()
		q.Set("auth_method", "oidc")
		redirectURL.RawQuery = q.Encode()
		c.Redirect(http.StatusFound, redirectURL.String())
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to login")
		return
	}
}

func (s *Server) AddOAuth2Routes(r *gin.RouterGroup) {
	r = r.Group("/oauth2")
	r.GET("/login", s.OAuth2Login)
	r.GET("/callback", s.OAuth2Callback)
}
