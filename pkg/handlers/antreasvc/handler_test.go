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
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"antrea.io/antrea-ui/pkg/auth/session"
)

func TestRequestsHandler(t *testing.T) {
	logger := testr.New(t)
	restConfig := &rest.Config{}
	const antreaNamespace = "kube-system"
	antreaSvcAddr := antreaSvcName + "." + antreaNamespace + ".svc"

	ca := testcerts.NewCA()
	kp, err := ca.NewKeyPair(antreaSvcAddr)
	require.NoError(t, err)
	cert, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
	require.NoError(t, err)

	var gotHeader http.Header
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	}))
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	ts.StartTLS()
	defer ts.Close()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: antreaNamespace,
			Name:      antreaCAConfigMapName,
		},
		Data: map[string]string{
			antreaCAConfigMapKey: string(ca.PublicKey()),
		},
	}
	fakeClient := fake.NewSimpleClientset(cm)

	url, err := url.Parse(ts.URL)
	require.NoError(t, err)

	handler := &requestsHandler{
		logger:          logger,
		antreaNamespace: antreaNamespace,
		host:            url.Host,
		kubeClient:      fakeClient,
		clientProvider:  newAntreaClientProvider(logger, restConfig, fakeClient, antreaNamespace, antreaSvcAddr),
		// the port forwarding case cannot be validated in the context of a unit test
		portForwardingNeeded: false,
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	go handler.Run(stopCh)

	require.Eventually(t, func() bool {
		_, _, err := handler.clientProvider.GetClientFactory()
		return err == nil
	}, 1*time.Second, 100*time.Millisecond)

	store := session.NewStore(logger, session.Options{})

	// Requests carry the credential of the user who triggered them, so the Antrea Service (which
	// delegates authn/authz to Kubernetes) authorizes them against that user's own RBAC.
	t.Run("end-user bearer token", func(t *testing.T) {
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
		assert.Equal(t, "Bearer user-token", gotHeader.Get("Authorization"))
		assert.Empty(t, gotHeader.Get(transport.ImpersonateUserHeader))
	})

	// The static admin password has no Kubernetes identity of its own, so it keeps impersonating
	// antrea-ui-admin.
	t.Run("admin password impersonation", func(t *testing.T) {
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
		assert.Equal(t, impersonatedUser, gotHeader.Get(transport.ImpersonateUserHeader))
	})

	t.Run("unauthenticated context", func(t *testing.T) {
		_, _, err := handler.Request(t.Context(), "GET", "/foo", nil)
		assert.ErrorContains(t, err, "not authenticated")
	})
}
