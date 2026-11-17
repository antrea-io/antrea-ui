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

// Package testing provides helpers for tests which need a fake K8s API server. It does not depend
// on pkg/k8s, so that the tests of pkg/k8s can use it too.
package testing

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/test/bufconn"
	"k8s.io/client-go/rest"
)

// InMemoryServerURL is the URL to use for a server which uses the in-memory network of
// httptest.NewTestServer, in place of its URL field: that field is only set once the server has
// started, and to http://example.com. The client of such a server ignores the address it is asked
// to dial, so the host can be a name which never resolves: if a request ever stopped going through
// that client, it would fail instead of reaching a real host.
const InMemoryServerURL = "http://fake-apiserver.invalid"

// InMemoryServerConfig returns what client-go needs to talk to ts, which must have been created
// with httptest.NewTestServer and must use the in-memory network: it is not listening on a real
// address, so only its own client can reach it. It starts ts, which must not be configured any
// further.
//
// The returned transport is the one of that client. Use it as is where a transport is passed in,
// for example as the ServiceAccount transport of k8s.NewClientFactory, or as the base of it. The
// transports which client-go builds from the returned config dial the same way: client-go keeps
// Dial and Proxy when it derives a config from another (see rest.AnonymousClientConfig), but not
// Transport. Unlike a loopback address, the Host of the config is subject to the proxy configured
// in the environment, if any, hence Proxy.
func InMemoryServerConfig(t testing.TB, ts *httptest.Server) (*rest.Config, http.RoundTripper) {
	t.Helper()
	transport, ok := ts.Client().Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialContext)
	return &rest.Config{
		Host:  InMemoryServerURL,
		Dial:  transport.DialContext,
		Proxy: noProxy,
	}, transport
}

// InMemoryTLSServerURL is the URL to use for a server started with ServeInMemoryTLS. As with
// InMemoryServerURL, the host is a name which never resolves.
const InMemoryTLSServerURL = "https://fake-apiserver.invalid"

// ServeInMemoryTLS serves handler over TLS on an in-memory listener until the end of the test, and
// returns what client-go needs to reach it. tlsConfig must hold the server certificate.
//
// It is for clients which do their own TLS handshake, with the CAData, ServerName or client
// certificate of the config. The in-memory network of httptest.NewTestServer has no documented
// entry point for that: only the client of the server can reach it, and that client does the TLS
// handshake itself. Like the transport returned by InMemoryServerConfig, the Dial of the returned
// config ignores the address it is asked to dial. bufconn does no real network I/O, so tests using
// this server can run under testing/synctest.
func ServeInMemoryTLS(t testing.TB, handler http.Handler, tlsConfig *tls.Config) *rest.Config {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	// ReadHeaderTimeout is for the linter (gosec G112): requests come from the test itself.
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Minute}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(tls.NewListener(lis, tlsConfig))
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return &rest.Config{
		Host:  InMemoryTLSServerURL,
		Dial:  func(ctx context.Context, _, _ string) (net.Conn, error) { return lis.DialContext(ctx) },
		Proxy: noProxy,
	}
}

func noProxy(*http.Request) (*url.URL, error) { return nil, nil }
