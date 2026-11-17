// Copyright 2024 Antrea Authors.
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

package antreasvc

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/transport"

	"antrea.io/antrea-ui/pkg/auth/session"
	k8stesting "antrea.io/antrea-ui/pkg/k8s/testing"
)

const testAntreaNamespace = "kube-system"

var testAntreaSvcAddr = antreaSvcName + "." + testAntreaNamespace + ".svc"

// fakeAntreaSvc is a fake Antrea Service, which records the headers of the last request it got,
// and echoes its body.
type fakeAntreaSvc struct {
	host     string
	caBundle []byte
	header   http.Header
}

// newTestRequestsHandler starts a fake Antrea Service and a requestsHandler for it, and waits for
// the handler to get the CA bundle of the Service from its ConfigMap. It must be called in a
// synctest bubble.
func newTestRequestsHandler(t *testing.T) (*requestsHandler, *fakeAntreaSvc) {
	logger := testr.New(t)

	ca := testcerts.NewCA()
	kp, err := ca.NewKeyPair(testAntreaSvcAddr)
	require.NoError(t, err)
	cert, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
	require.NoError(t, err)

	svc := &fakeAntreaSvc{caBundle: ca.PublicKey()}
	// client-go does its own TLS handshake, with the CA bundle and the server name.
	restConfig := k8stesting.ServeInMemoryTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.header = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	}), &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testAntreaNamespace,
			Name:      antreaCAConfigMapName,
		},
		Data: map[string]string{
			antreaCAConfigMapKey: string(ca.PublicKey()),
		},
	}
	fakeClient := fake.NewSimpleClientset(cm)

	url, err := url.Parse(restConfig.Host)
	require.NoError(t, err)
	svc.host = url.Host

	handler := &requestsHandler{
		logger:          logger,
		antreaNamespace: testAntreaNamespace,
		host:            url.Host,
		kubeClient:      fakeClient,
		clientProvider:  newAntreaClientProvider(logger, restConfig, fakeClient, testAntreaNamespace, testAntreaSvcAddr),
		// the port forwarding case cannot be validated in the context of a unit test
		portForwardingNeeded: false,
	}

	stopCh := make(chan struct{})
	t.Cleanup(func() { close(stopCh) })
	go handler.Run(stopCh)

	// The CA controller waits for its informer with cache.WaitForCacheSync, which polls every
	// 100ms, and then delivers the CA bundle without any timer involved.
	synctest.Sleep(time.Second)
	_, _, err = handler.clientProvider.GetClientFactory()
	require.NoError(t, err)

	return handler, svc
}

func TestRequestsHandler(t *testing.T) {
	// Requests carry the credential of the user who triggered them, so the Antrea Service (which
	// delegates authn/authz to Kubernetes) authorizes them against that user's own RBAC.
	t.Run("end-user bearer token", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, svc := newTestRequestsHandler(t)
			store := session.NewStore(testr.New(t), session.Options{})
			sess, err := store.Create(&session.Spec{
				Mode:       session.ModeToken,
				Credential: session.Credential{Kind: session.KindBearer, Token: []byte("user-token")},
			})
			require.NoError(t, err)
			ctx := session.WithRequestAuth(t.Context(), session.NewSessionAuth(store, sess))

			body := "bar"
			b, statusCode, err := handler.Request(ctx, "GET", "/foo", bytes.NewBufferString(body))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, statusCode)
			assert.Equal(t, body, string(b))
			assert.Equal(t, "Bearer user-token", svc.header.Get("Authorization"))
			assert.Empty(t, svc.header.Get(transport.ImpersonateUserHeader))
		})
	})

	// The static admin password has no Kubernetes identity of its own, so it keeps impersonating
	// antrea-ui-admin.
	t.Run("admin password impersonation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, svc := newTestRequestsHandler(t)
			store := session.NewStore(testr.New(t), session.Options{})
			const impersonatedUser = "system:serviceaccount:kube-system:antrea-ui-admin"
			sess, err := store.Create(&session.Spec{
				Mode:       session.ModeAdmin,
				Credential: session.Credential{Kind: session.KindImpersonate, UserName: impersonatedUser},
			})
			require.NoError(t, err)
			ctx := session.WithRequestAuth(t.Context(), session.NewSessionAuth(store, sess))

			_, statusCode, err := handler.Request(ctx, "GET", "/foo", bytes.NewBufferString("bar"))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, statusCode)
			assert.Equal(t, impersonatedUser, svc.header.Get(transport.ImpersonateUserHeader))
		})
	})

	t.Run("unauthenticated context", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, _ := newTestRequestsHandler(t)
			_, _, err := handler.Request(t.Context(), "GET", "/foo", nil)
			assert.ErrorContains(t, err, "not authenticated")
		})
	})

	t.Run("connection info", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			handler, svc := newTestRequestsHandler(t)
			host, serverName, caBundle, err := handler.ConnInfo()
			require.NoError(t, err)
			assert.Equal(t, svc.host, host)
			assert.Equal(t, testAntreaSvcAddr, serverName)
			assert.Equal(t, svc.caBundle, caBundle)

			handler.setHost("")
			_, _, _, err = handler.ConnInfo()
			assert.ErrorContains(t, err, "not ready")
		})
	})
}
