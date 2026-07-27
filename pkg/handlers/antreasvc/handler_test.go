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
	"context"
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

	const impersonatedUser = "system:serviceaccount:kube-system:antrea-ui-admin"
	handler := &requestsHandler{
		logger:          logger,
		antreaNamespace: antreaNamespace,
		host:            url.Host,
		kubeClient:      fakeClient,
		clientProvider:  newAntreaClientProvider(logger, restConfig, fakeClient, antreaNamespace, antreaSvcAddr, impersonatedUser),
		// the port forwarding case cannot be validated in the context of a unit test
		portForwardingNeeded: false,
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	go handler.Run(stopCh)

	require.Eventually(t, func() bool {
		_, err := handler.clientProvider.GetAntreaClient()
		return err == nil
	}, 1*time.Second, 100*time.Millisecond)

	body := "bar"
	b, err := handler.Request(context.Background(), "GET", "/foo", bytes.NewBufferString(body))
	require.NoError(t, err)
	assert.Equal(t, body, string(b))
	// the request sent on the wire must carry the impersonated identity
	assert.Equal(t, impersonatedUser, gotHeader.Get(transport.ImpersonateUserHeader))
}
