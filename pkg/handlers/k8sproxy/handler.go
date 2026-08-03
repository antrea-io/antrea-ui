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

package k8sproxy

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/client-go/transport"

	"antrea.io/antrea-ui/pkg/auth/session"
)

// perRequestTransport picks the RoundTripper from the identity the authentication middleware
// resolved for the request, so every proxied call reaches the API server as the end user rather
// than as a single shared identity.
type perRequestTransport struct {
	logger logr.Logger
	// transportFor is k8s.ClientFactory.TransportForRequest.
	transportFor func(req *http.Request) (http.RoundTripper, error)
}

func (t *perRequestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt, err := t.transportFor(r)
	if err != nil {
		return nil, err
	}
	t.logger.V(4).Info("Proxying request", "url", r.URL)
	return rt.RoundTrip(r)
}

type proxyErrorWriter struct {
	logger logr.Logger
}

func (w *proxyErrorWriter) Write(p []byte) (n int, err error) {
	w.logger.Error(errors.New(string(p)), "K8s proxy error")
	return len(p), nil
}

// NewK8sProxyHandler builds the reverse proxy behind /api/v1/k8s.
//
// transportFor resolves the request's own credential into a transport; it is
// k8s.ClientFactory.TransportForRequest in production.
func NewK8sProxyHandler(
	logger logr.Logger,
	k8sServerURL *url.URL,
	transportFor func(req *http.Request) (http.RoundTripper, error),
) http.Handler {
	errorLogger := log.New(&proxyErrorWriter{
		logger: logger,
	}, "", 0)

	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(k8sServerURL) // Also rewrites the Host header.
			r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
			r.SetXForwarded() // Set X-Forwarded-* headers.
			// The proxy authenticates the request itself, from the credential the
			// middleware resolved, so nothing the client sent to authenticate to
			// antrea-ui has any business reaching the API server. The session cookie in
			// particular is credential-equivalent for the whole UI, and forwarding it
			// would deposit it in the API server's audit log and in every proxy in
			// between.
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
			// With the static admin password (mode 4), requests are still authorized via
			// impersonation. The K8s API server's impersonating round tripper leaves any
			// pre-existing Impersonate-* header alone instead of overriding it, so without
			// this, a UI user could set their own Impersonate-User/-Group/-Uid/-Extra-*
			// header and have the proxied request authorized as a different identity.
			r.Out.Header.Del(transport.ImpersonateUserHeader)
			r.Out.Header.Del(transport.ImpersonateUIDHeader)
			r.Out.Header.Del(transport.ImpersonateGroupHeader)
			for name := range r.Out.Header {
				if strings.HasPrefix(name, transport.ImpersonateUserExtraHeaderPrefix) {
					r.Out.Header.Del(name)
				}
			}
		},
		Transport: &perRequestTransport{
			logger:       logger,
			transportFor: transportFor,
		},
		ModifyResponse: func(resp *http.Response) error {
			// The API server answers 401 when it rejects the credential itself, and 403
			// when the credential is fine but the user is not allowed to do this. Only the
			// former means the session is dead; conflating the two would log a user out
			// every time they hit an ordinary permissions error.
			if resp.StatusCode == http.StatusUnauthorized && resp.Request != nil {
				if ra, ok := session.RequestAuthFrom(resp.Request.Context()); ok {
					ra.Invalidate()
				}
			}
			return nil
		},
		ErrorLog: errorLogger,
	}
}
