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

package supportbundle

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-logr/logr"
	"k8s.io/client-go/transport"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

const (
	// The user extras antrea-ui-admin is impersonated with for requests to apiServer sources. The
	// apiserver records them in its audit log, and the aggregation layer forwards them to the
	// APIService as X-Remote-Extra-* headers.
	extraRequestedBy = "supportbundle.ui.antrea.io/requested-by"
	extraBundleID    = "supportbundle.ui.antrea.io/bundle-id"

	// The same information, as headers on every request to an https source.
	//
	// The requester's username is percent-encoded UTF-8 (url.PathEscape) in both, since it may
	// hold any character, and a header value cannot.
	headerRequestedBy = "X-Antrea-UI-Requested-By"
	headerBundleID    = "X-Antrea-UI-Support-Bundle"

	// tokenAudiencePrefix, followed by the source's name, is the only audience of the tokens
	// presented to an https source: the apiserver rejects them, and so does every other source.
	tokenAudiencePrefix = "supportbundle.ui.antrea.io/"
)

// TokenSource mints a bearer token for antrea-ui-admin, for one audience (see
// k8s.AdminTokenSource).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// requester identifies the collection a request to a source is made for. Sources are always
// called as antrea-ui-admin: this only tells them, and the audit log, who asked for it.
type requester struct {
	username string
	bundleID string
}

// source is a secondary support bundle source.
type source interface {
	name() string
	kind() string
	// connect returns what is needed to talk to the source for the collection r requested.
	connect(r requester) *sourceConn
}

// sourceConn is a client for one source, for one collection.
type sourceConn struct {
	client *http.Client
	base   *url.URL
	// authorize, when not nil, adds the credential and the audit headers to a request, for
	// sources whose transport does not carry them.
	authorize func(ctx context.Context, req *http.Request) error
}

// newNoRedirectClient returns a client that never follows a redirect. The apiserver transport adds
// antrea-ui's own credential to every hop, and the client forwards the first request's headers
// (the token for an https source) to some redirect targets: following a redirect would hand a
// credential to wherever the source points.
func newNoRedirectClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// apiServerSource is reached through the Kubernetes apiserver (typically served by an
// APIService), as antrea-ui-admin: antrea-ui's own credential never leaves the apiserver, which
// forwards the identity it impersonated.
type apiServerSource struct {
	sourceName string
	sourceKind string
	base       *url.URL
	// transport authenticates as antrea-ui's own ServiceAccount, which impersonates adminUser.
	transport http.RoundTripper
	adminUser string
}

func newAPIServerSource(name, kind string, apiServerURL *url.URL, path string, transport http.RoundTripper, adminUser string) *apiServerSource {
	return &apiServerSource{
		sourceName: name,
		sourceKind: kind,
		base:       apiServerURL.JoinPath(path),
		transport:  transport,
		adminUser:  adminUser,
	}
}

func (s *apiServerSource) name() string { return s.sourceName }
func (s *apiServerSource) kind() string { return s.sourceKind }

func (s *apiServerSource) connect(r requester) *sourceConn {
	rt := transport.NewImpersonatingRoundTripper(transport.ImpersonationConfig{
		UserName: s.adminUser,
		Extra: map[string][]string{
			extraRequestedBy: {url.PathEscape(r.username)},
			extraBundleID:    {r.bundleID},
		},
	}, s.transport)
	return &sourceConn{client: newNoRedirectClient(rt), base: s.base}
}

// httpsSource is reached directly by URL. Only the operator can configure one: the URL receives a
// token for antrea-ui-admin, so where it points is a deployment decision. The token's audience is
// the source's own, so it cannot be replayed against the apiserver or another source.
type httpsSource struct {
	sourceName string
	base       *url.URL
	transport  *http.Transport
	tokens     TokenSource
}

// newHTTPSSource builds an operator-configured source reached by URL. tokens mints the
// antrea-ui-admin tokens presented to it, for its audience.
func newHTTPSSource(logger logr.Logger, name string, cfg *serverconfig.SupportBundleHTTPSSource, tokens TokenSource) (*httpsSource, error) {
	base, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: cfg.ServerName,
	}
	if cfg.CAData != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CAData)) {
			return nil, fmt.Errorf("caData holds no valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.InsecureSkipVerify {
		logger.Info("WARNING: TLS certificate verification is disabled for a support bundle source, which receives antrea-ui-admin tokens. This should only be used for development/testing.", "source", name)
		tlsConfig.InsecureSkipVerify = true
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSClientConfig = tlsConfig
	return &httpsSource{
		sourceName: name,
		base:       base,
		transport:  t,
		tokens:     tokens,
	}, nil
}

func (s *httpsSource) name() string { return s.sourceName }
func (s *httpsSource) kind() string { return apisv1.SupportBundleSourceKindExtra }

func (s *httpsSource) connect(r requester) *sourceConn {
	return &sourceConn{
		client: newNoRedirectClient(s.transport),
		base:   s.base,
		authorize: func(ctx context.Context, req *http.Request) error {
			// Asked for before every request, never once per collection: a collection can
			// outlive a token.
			token, err := s.tokens.Token(ctx)
			if err != nil {
				return fmt.Errorf("failed to mint admin token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set(headerRequestedBy, url.PathEscape(r.username))
			req.Header.Set(headerBundleID, r.bundleID)
			return nil
		},
	}
}
