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

// Package testing provides helpers for tests which need a fake K8s API server. It only depends on
// client-go, so that the tests of pkg/k8s can use it too.
package testing

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
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
		Proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	}, transport
}
