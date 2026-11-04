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

package flowstream

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

func caPEM(t *testing.T) string {
	t.Helper()
	return string(testcerts.NewCA().PublicKey())
}

func setCA(t *testing.T, client *fake.Clientset, name, pem string) {
	t.Helper()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "trusted"}, Data: map[string]string{"ca.crt": pem}}
	_, err := client.CoreV1().ConfigMaps("trusted").Update(t.Context(), cm, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.CoreV1().ConfigMaps("trusted").Create(t.Context(), cm, metav1.CreateOptions{})
	}
	require.NoError(t, err)
}

func newEndpoint(client *fake.Clientset, name string) *EndpointSubscriber {
	return &EndpointSubscriber{Client: client, Name: name, Address: name + ".trusted.svc:14740", Namespace: "trusted", CAConfigMap: name + "-ca"}
}

func TestEndpointConnectionConfigReadsTrust(t *testing.T) {
	client := fake.NewClientset()
	setCA(t, client, "src-ca", caPEM(t))
	cfg, _, err := newEndpoint(client, "src").connectionConfig(t.Context())
	require.NoError(t, err)
	require.NotNil(t, cfg.TLSConfig.RootCAs)
	assert.False(t, cfg.TLSConfig.InsecureSkipVerify)
	assert.Equal(t, "src", cfg.Name)
	assert.Equal(t, "src.trusted.svc:14740", cfg.Address)

	// Without a CA ConfigMap, TLS uses the system trust store and the API is not read.
	client = fake.NewClientset()
	endpoint := newEndpoint(client, "src")
	endpoint.CAConfigMap = ""
	cfg, _, err = endpoint.connectionConfig(t.Context())
	require.NoError(t, err)
	assert.Nil(t, cfg.TLSConfig.RootCAs)
	assert.Empty(t, client.Actions())
}

func TestEndpointConnectionConfigErrors(t *testing.T) {
	forbidden := func(client *fake.Clientset) {
		client.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "src-ca", nil)
		})
	}
	for _, tc := range []struct {
		name          string
		prepare       func(*fake.Clientset)
		wantCode      string
		wantRetryable bool
	}{
		{name: "missing ConfigMap", prepare: func(*fake.Clientset) {}, wantCode: dependencyUnavailableCode, wantRetryable: true},
		{name: "backend not allowed to read it", prepare: forbidden, wantCode: StreamErrorCodeInternal},
		{name: "empty ca.crt", prepare: func(c *fake.Clientset) { setCA(t, c, "src-ca", "") }, wantCode: StreamErrorCodeInternal},
		{name: "invalid ca.crt", prepare: func(c *fake.Clientset) { setCA(t, c, "src-ca", "not a certificate") }, wantCode: StreamErrorCodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset()
			tc.prepare(client)
			_, _, err := newEndpoint(client, "src").connectionConfig(t.Context())
			var se *StreamError
			require.ErrorAs(t, err, &se)
			assert.Equal(t, tc.wantCode, se.Code)
			assert.Equal(t, tc.wantRetryable, se.Retryable)
			assert.Contains(t, se.Error(), "trusted/src-ca")
			assert.Contains(t, se.Error(), `"src"`)
		})
	}
}

// The key versions the connection a certificate session caches, so that a changed endpoint or
// trust retires the connection built for the old one, and keeps each source in its own upstream.
func TestEndpointConnectionKey(t *testing.T) {
	client := fake.NewClientset()
	pem := caPEM(t)
	setCA(t, client, "a-ca", pem)
	setCA(t, client, "b-ca", pem)
	key := func(e *EndpointSubscriber) string {
		cfg, _, err := e.connectionConfig(t.Context())
		require.NoError(t, err)
		return cfg.ConnectionKey
	}
	a := newEndpoint(client, "a")
	base := key(a)
	require.True(t, strings.HasPrefix(base, "flow-source-a/"), base)
	assert.Equal(t, base, key(a), "unchanged trust and endpoint must keep the key")

	// The same CA and the same endpoint settings do not make two sources share a key, or an
	// upstream: the session retires only the older versions of one upstream.
	b := newEndpoint(client, "b")
	b.Address = a.Address
	other := key(b)
	assert.NotEqual(t, base, other)
	upstream, _, versioned := strings.Cut(other, "/")
	assert.True(t, versioned)
	assert.Equal(t, "flow-source-b", upstream)

	for name, change := range map[string]func(*EndpointSubscriber){
		"address":            func(e *EndpointSubscriber) { e.Address = "elsewhere.trusted.svc:14740" },
		"server name":        func(e *EndpointSubscriber) { e.ServerName = "other.example.test" },
		"insecure skip":      func(e *EndpointSubscriber) { e.InsecureSkipVerify = true },
		"rotated CA content": func(e *EndpointSubscriber) { setCA(t, client, "a-ca", caPEM(t)) },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEndpoint(client, "a")
			setCA(t, client, "a-ca", pem)
			before := key(e)
			change(e)
			assert.NotEqual(t, before, key(e))
			assert.True(t, strings.HasPrefix(key(e), "flow-source-a/"))
		})
	}
}

// A missing or invalid CA is a failure of the request that needs it, reported when the stream
// starts, and it is retried by the next one.
func TestEndpointSubscribeFailsOnlyTheRequest(t *testing.T) {
	client := fake.NewClientset()
	endpoint := newEndpoint(client, "src")
	flows, errs, ready := endpoint.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, finiteFilter())
	err := <-errs
	var se *StreamError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, dependencyUnavailableCode, se.Code)
	_, open := <-flows
	assert.False(t, open)
	select {
	case <-ready:
		t.Fatal("a stream that failed to start must not become ready")
	default:
	}
}

// A rotated CA applies to the next stream, with no restart, and the connection cached for the
// session is replaced rather than reused. This exercises real certificate handshakes, so it uses
// loopback TCP rather than synctest.
func TestEndpointAppliesRotatedCAToNextStream(t *testing.T) {
	serverCA := testcerts.NewCA()
	serverKP, err := serverCA.NewKeyPair("flow.test")
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(serverKP.PublicKey(), serverKP.PrivateKey())
	require.NoError(t, err)
	clientKP, err := serverCA.NewKeyPair("viewer")
	require.NoError(t, err)
	server := &fakeFlowStreamServer{handle: func(int, string) error { return nil }}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    serverCA.CertPool(),
	})))
	flowpb.RegisterFlowStreamServiceServer(srv, server)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	client := fake.NewClientset()
	setCA(t, client, "src-ca", string(serverCA.PublicKey()))
	endpoint := newEndpoint(client, "src")
	endpoint.Logger = testr.New(t)
	endpoint.Address = lis.Addr().String()
	endpoint.ServerName = "flow.test"
	ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{Mode: session.ModeKubeconfig, Credential: session.Credential{Kind: session.KindCert, CertPEM: clientKP.PublicKey(), KeyPEM: clientKP.PrivateKey()}})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream := func() error {
		return relay(ctx, endpoint, &FlowStreamScope{ClusterWide: true}, finiteFilter(), nil, func() error { return nil }, func(apisv1.FlowStreamEvent) error { return nil })
	}

	require.NoError(t, stream())
	require.EqualValues(t, 1, server.calls.Load())

	setCA(t, client, "src-ca", caPEM(t))
	require.Error(t, stream(), "the rotated CA does not trust the server")
	require.EqualValues(t, 1, server.calls.Load())

	setCA(t, client, "src-ca", string(serverCA.PublicKey()))
	require.NoError(t, stream())
	require.EqualValues(t, 2, server.calls.Load())
}
