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
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"antrea.io/antrea-ui/pkg/auth/session"
)

// recordingServer captures the last request it saw, so tests can assert on what reached the wire.
type recordingServer struct {
	*httptest.Server
	header http.Header
	peer   []*x509.Certificate
}

func newRecordingServer(t *testing.T, tlsEnabled bool) *recordingServer {
	rs := &recordingServer{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.header = r.Header.Clone()
		if r.TLS != nil {
			rs.peer = r.TLS.PeerCertificates
		}
		w.WriteHeader(http.StatusOK)
	})
	if tlsEnabled {
		rs.Server = httptest.NewUnstartedServer(handler)
		rs.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
		rs.StartTLS()
	} else {
		rs.Server = httptest.NewServer(handler)
	}
	t.Cleanup(rs.Close)
	return rs
}

func generateClientCert(t *testing.T, commonName string) (certPEM []byte, keyPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func TestTransportForBearer(t *testing.T) {
	ts := newRecordingServer(t, false)
	// The SA transport would add antrea-ui's own token; the user's bearer token must be what
	// reaches the API server, so the factory has to layer it on an anonymous base instead.
	saTransport := transport.NewBearerAuthRoundTripper("antrea-ui-sa-token", http.DefaultTransport)
	f, err := NewClientFactory(&rest.Config{Host: ts.URL}, saTransport, session.TransportKeyK8s)
	require.NoError(t, err)

	rt, cleanup, err := f.TransportFor(&session.Credential{Kind: session.KindBearer, Token: []byte("user-token")})
	require.NoError(t, err)
	assert.Nil(t, cleanup, "a bearer transport shares the base pool and owns nothing to clean up")

	resp, err := f.HTTPClient(rt).Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "Bearer user-token", ts.header.Get("Authorization"))
	assert.Empty(t, ts.header.Get(transport.ImpersonateUserHeader))
}

func TestTransportForImpersonate(t *testing.T) {
	ts := newRecordingServer(t, false)
	saTransport := transport.NewBearerAuthRoundTripper("antrea-ui-sa-token", http.DefaultTransport)
	f, err := NewClientFactory(&rest.Config{Host: ts.URL}, saTransport, session.TransportKeyK8s)
	require.NoError(t, err)

	userName := ServiceAccountUserName("kube-system", "antrea-ui-admin")
	rt, _, err := f.TransportFor(&session.Credential{Kind: session.KindImpersonate, UserName: userName})
	require.NoError(t, err)

	resp, err := f.HTTPClient(rt).Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Mode 4 still authenticates as antrea-ui's own SA and impersonates antrea-ui-admin.
	assert.Equal(t, "Bearer antrea-ui-sa-token", ts.header.Get("Authorization"))
	assert.Equal(t, userName, ts.header.Get(transport.ImpersonateUserHeader))
}

func TestTransportForCert(t *testing.T) {
	ts := newRecordingServer(t, true)
	certPEM, keyPEM := generateClientCert(t, "alice")
	config := &rest.Config{Host: ts.URL}
	config.Insecure = true
	f, err := NewClientFactory(config, http.DefaultTransport, session.TransportKeyK8s)
	require.NoError(t, err)

	rt, cleanup, err := f.TransportFor(&session.Credential{Kind: session.KindCert, CertPEM: certPEM, KeyPEM: keyPEM})
	require.NoError(t, err)
	require.NotNil(t, cleanup, "a cert transport owns its own connection pool and must be closeable")

	resp, err := f.HTTPClient(rt).Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Len(t, ts.peer, 1)
	assert.Equal(t, "alice", ts.peer[0].Subject.CommonName)
	assert.Empty(t, ts.header.Get("Authorization"))

	// Should not panic or error.
	cleanup()
}

func TestTransportForUnsupportedKind(t *testing.T) {
	f, err := NewClientFactory(&rest.Config{Host: "https://localhost:6443"}, http.DefaultTransport, session.TransportKeyK8s)
	require.NoError(t, err)
	_, _, err = f.TransportFor(&session.Credential{Kind: "nonsense"})
	assert.ErrorContains(t, err, "unsupported credential kind")
}

func TestDynamicClient(t *testing.T) {
	ts := newRecordingServer(t, false)
	f, err := NewClientFactory(&rest.Config{Host: ts.URL}, http.DefaultTransport, session.TransportKeyK8s)
	require.NoError(t, err)
	rt, _, err := f.TransportFor(&session.Credential{Kind: session.KindBearer, Token: []byte("user-token")})
	require.NoError(t, err)
	client, err := f.DynamicClient(rt)
	require.NoError(t, err)
	assert.NotNil(t, client)
}
