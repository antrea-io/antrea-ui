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

package k8s

import (
	"context"
	"fmt"
	"net/http"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"antrea.io/antrea-ui/pkg/auth/session"
)

// ClientFactory builds Kubernetes clients that authenticate as an end user rather than as
// antrea-ui's own ServiceAccount.
//
// It holds one shared, credential-free base transport, built from rest.AnonymousClientConfig so
// that it keeps the host, TLS and CA settings but carries no Authorization header of its own.
// Per-user authentication is layered on top of it; without the anonymous base, antrea-ui's SA
// token and the user's credential would fight over the Authorization header.
type ClientFactory struct {
	config *rest.Config
	// baseTransport is credential-free and shared by every bearer-authenticated user, so they
	// all share one connection pool.
	baseTransport http.RoundTripper
	// saTransport authenticates as antrea-ui's own ServiceAccount. It is only used as the
	// base for impersonation (mode 4).
	saTransport http.RoundTripper
	// transportKey is the key this factory's transports are cached under on a session. Two
	// factories that talk to different upstreams (the kube-apiserver, the Antrea Service)
	// must use different keys.
	transportKey string
}

// NewClientFactory builds a factory for the cluster described by config. saTransport must
// authenticate as antrea-ui's own ServiceAccount: it is the base for impersonated requests.
// transportKey identifies the upstream this factory targets, for per-session transport caching.
func NewClientFactory(config *rest.Config, saTransport http.RoundTripper, transportKey string) (*ClientFactory, error) {
	baseTransport, err := rest.TransportFor(rest.AnonymousClientConfig(config))
	if err != nil {
		return nil, fmt.Errorf("failed to build anonymous base transport: %w", err)
	}
	return &ClientFactory{
		config:        config,
		baseTransport: baseTransport,
		saTransport:   saTransport,
		transportKey:  transportKey,
	}, nil
}

// TransportFor builds an http.RoundTripper that authenticates as cred. The second return value,
// when not nil, releases resources owned by the transport and must be called once it is
// discarded.
func (f *ClientFactory) TransportFor(cred *session.Credential) (http.RoundTripper, func(), error) {
	switch cred.Kind {
	case session.KindBearer:
		// Cheap: this only sets a header, so the shared base transport's connection pool is
		// reused across users.
		//
		// string(cred.Token) makes a copy that outlives Credential.Zero(): see that method's
		// doc comment for why, and why it is left as is.
		return transport.NewBearerAuthRoundTripper(string(cred.Token), f.baseTransport), nil, nil
	case session.KindCert:
		// TLS client certificates are negotiated per connection, so a cert credential needs
		// its own transport and its own pool. This is why cert-shaped credentials cost more
		// than bearer ones.
		cfg := rest.AnonymousClientConfig(f.config)
		cfg.CertData = cred.CertPEM
		cfg.KeyData = cred.KeyPEM
		rt, err := rest.TransportFor(cfg)
		if err != nil {
			// Deliberately not wrapping err with any credential detail.
			return nil, nil, fmt.Errorf("failed to build client certificate transport: %w", err)
		}
		return rt, func() { utilnet.CloseIdleConnectionsFor(rt) }, nil
	case session.KindImpersonate:
		return transport.NewImpersonatingRoundTripper(
			transport.ImpersonationConfig{UserName: cred.UserName},
			f.saTransport,
		), nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported credential kind %q", cred.Kind)
	}
}

// TransportForRequest returns the transport for the identity the authentication middleware
// resolved for this request. Session-backed requests reuse a cached transport.
func (f *ClientFactory) TransportForRequest(ctx context.Context) (http.RoundTripper, error) {
	ra, ok := session.RequestAuthFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("request is not authenticated")
	}
	return ra.TransportFor(f.transportKey, f.TransportFor)
}

// DynamicClientForRequest builds a dynamic client that acts as this request's identity.
func (f *ClientFactory) DynamicClientForRequest(ctx context.Context) (dynamic.Interface, error) {
	rt, err := f.TransportForRequest(ctx)
	if err != nil {
		return nil, err
	}
	return f.DynamicClient(rt)
}

// HTTPClient wraps rt in an http.Client with the factory's configured timeout.
func (f *ClientFactory) HTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{Transport: rt, Timeout: f.config.Timeout}
}

// DynamicClient builds a dynamic client that sends its requests through rt. The transport (and
// therefore the connection pool) is the expensive part and is cached per session; the client
// itself is a thin wrapper and is cheap to build per request.
func (f *ClientFactory) DynamicClient(rt http.RoundTripper) (dynamic.Interface, error) {
	return dynamic.NewForConfigAndClient(f.config, f.HTTPClient(rt))
}
