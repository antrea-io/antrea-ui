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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

	"antrea.io/antrea-ui/pkg/server/errors"
	cookieutils "antrea.io/antrea-ui/pkg/server/utils/cookie"
	"antrea.io/antrea-ui/pkg/server/utils/template"
)

// After 30 minutes, the user will be redirected to the OIDC provider to
// authenticate again. This does NOT mean that the user will need to enter his
// credentials again, as the OIDC provider is likely to rely on its own cookies.
const OIDCAuthRefreshTokenLifetime = 30 * time.Minute

func genRandomBytes(bytes int) ([]byte, error) {
	r := make([]byte, bytes)
	_, err := rand.Read(r)
	if err != nil {
		return nil, fmt.Errorf("error when generating random data: %w", err)
	}
	return r, nil
}

func genRandomHexString(bytes int) (string, error) {
	r, err := genRandomBytes(bytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(r), nil
}

func genNonce() (string, error) {
	return genRandomHexString(32)
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

func NewOIDCProvider(
	logger logr.Logger,
	serverURL string,
	issuerURL string,
	discoveryURL string,
	clientID string,
	clientSecret string,
	logoutURLTemplate string,
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
		// "openid" is a required scope for OpenID Connect flows.
		// Other scopes, such as "email" & "groups" can be requested.
		scopes:            []string{oidc.ScopeOpenID},
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
		redirectURL := c.Query("redirect_url")

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
				Err:  fmt.Errorf("failed to verify id_token"),
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

		// at the moment, we are not doing anything with the id_token claims (e.g. email address)

		refreshToken, err := s.tokenManager.GetRefreshToken(OIDCAuthRefreshTokenLifetime, idToken.Subject)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when getting JWT refresh token: %w", err),
			}
		}
		cookieutils.SetRefreshTokenCookie(c.Writer, refreshToken.Raw, s.config.CookieSecure)

		idTokenCookie := &http.Cookie{
			Name:  "antrea-ui-oidc-id-token",
			Value: rawIDToken,
			// the id_token is only needed for logging out (from the OIDC provider)
			Path:     "/auth",
			MaxAge:   0, // make it a session cookie
			Secure:   s.config.CookieSecure,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		}
		// this cookie can potentially be larger than 4KB, so we split it if needed
		if err := cookieutils.SetLargeCookie(c.Writer, idTokenCookie); err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("failed to set large cookie for OIDC id_token: %w", err),
			}
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
