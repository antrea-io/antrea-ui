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
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Inspired from https://github.com/antrea-io/antrea/blob/v1.15.0/pkg/agent/client.go

const (
	antreaCAConfigMapName = "antrea-ca"
	antreaCAConfigMapKey  = "ca.crt"
)

// antreaHTTPClientProvider provides an AntreaClientProvider that can dynamically react to ConfigMap changes.
type antreaHTTPClientProvider struct {
	logger     logr.Logger
	config     *rest.Config
	serverName string
	// mutex protects client.
	mutex sync.RWMutex
	// client is the Antrea client that will be returned. It will be updated when caBundle is updated.
	client *http.Client
	// caContentProvider provides the very latest content of the ca bundle.
	caContentProvider *dynamiccertificates.ConfigMapCAController
}

var _ dynamiccertificates.Listener = &antreaHTTPClientProvider{}

func newAntreaClientProvider(logger logr.Logger, config *rest.Config, kubeClient kubernetes.Interface, antreaNamespace string, serverName string) *antreaHTTPClientProvider {
	// The key "ca.crt" may not exist at the beginning, no need to fail as the CA provider will watch the ConfigMap
	// and notify antreaHTTPClientProvider of any update. The consumers of antreaHTTPClientProvider are supposed to always
	// call GetAntreaClient() to get a client and not cache it.
	antreaCAProvider, _ := dynamiccertificates.NewDynamicCAFromConfigMapController(
		"antrea-ca",
		antreaNamespace,
		antreaCAConfigMapName,
		antreaCAConfigMapKey,
		kubeClient)
	antreaHTTPClientProvider := &antreaHTTPClientProvider{
		logger:            logger,
		config:            config,
		serverName:        serverName,
		caContentProvider: antreaCAProvider,
	}

	antreaCAProvider.AddListener(antreaHTTPClientProvider)
	return antreaHTTPClientProvider
}

// RunOnce runs the task a single time synchronously.
func (p *antreaHTTPClientProvider) RunOnce() error {
	return p.updateAntreaClient()
}

// Run starts the caContentProvider, which watches the ConfigMap and notifies changes
// by calling Enqueue.
func (p *antreaHTTPClientProvider) Run(ctx context.Context) {
	p.caContentProvider.Run(ctx, 1)
}

// Enqueue implements dynamiccertificates.Listener. It will be called by caContentProvider
// when caBundle is updated.
func (p *antreaHTTPClientProvider) Enqueue() {
	if err := p.updateAntreaClient(); err != nil {
		p.logger.Error(err, "Failed to update Antrea client")
	}
}

// GetAntreaClient implements AntreaClientProvider.
func (p *antreaHTTPClientProvider) GetAntreaClient() (*http.Client, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	if p.client == nil {
		return nil, fmt.Errorf("Antrea client is not ready")
	}
	return p.client, nil
}

func (p *antreaHTTPClientProvider) updateAntreaClient() error {
	caBundle := p.caContentProvider.CurrentCABundleContent()
	if caBundle == nil {
		p.logger.Info("Didn't get CA certificate, skip updating Antrea Client")
		return nil
	}
	kubeConfig := p.config
	if kubeConfig == nil {
		var err error
		if kubeConfig, err = inClusterConfig(caBundle); err != nil {
			return err
		}
	}
	// name used in the server certificate
	kubeConfig.TLSClientConfig.CAData = caBundle
	kubeConfig.TLSClientConfig.ServerName = p.serverName

	client, err := rest.HTTPClientFor(kubeConfig)
	if err != nil {
		return err
	}

	p.logger.Info("Updating Antrea client with the new CA bundle")
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.client = client

	return nil
}

func inClusterConfig(caBundle []byte) (*rest.Config, error) {
	// #nosec G101: not credentials
	const tokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}

	return &rest.Config{
		BearerToken:     string(token),
		BearerTokenFile: tokenFile,
	}, nil
}
