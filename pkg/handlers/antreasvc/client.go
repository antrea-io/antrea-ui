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
	"os"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/k8s"
)

// Inspired from https://github.com/antrea-io/antrea/blob/v1.15.0/pkg/agent/client.go

const (
	antreaCAConfigMapName = "antrea-ca"
	antreaCAConfigMapKey  = "ca.crt"
)

// antreaClientFactoryProvider hands out a k8s.ClientFactory for the Antrea Service, rebuilding it
// whenever the Antrea CA bundle changes.
//
// The factory is what turns a per-request end-user credential into a transport. The Antrea Service
// delegates authn/authz to Kubernetes, so a request carrying the user's own credential is
// authorized against the user's own RBAC - the point of the whole exercise.
type antreaClientFactoryProvider struct {
	logger     logr.Logger
	config     *rest.Config
	serverName string
	// mutex protects factory and generation.
	mutex sync.RWMutex
	// factory builds per-credential clients against the current CA bundle.
	factory *k8s.ClientFactory
	// generation increments on every CA bundle change. It is part of the transport cache key,
	// so a session that cached a transport against an older bundle rebuilds instead of
	// silently using a stale one.
	generation int
	// caContentProvider provides the very latest content of the ca bundle.
	caContentProvider *dynamiccertificates.ConfigMapCAController
}

var _ dynamiccertificates.Listener = &antreaClientFactoryProvider{}

func newAntreaClientProvider(logger logr.Logger, config *rest.Config, kubeClient kubernetes.Interface, antreaNamespace string, serverName string) *antreaClientFactoryProvider {
	// The key "ca.crt" may not exist at the beginning, no need to fail as the CA provider will watch the ConfigMap
	// and notify antreaClientFactoryProvider of any update. The consumers of antreaClientFactoryProvider are supposed
	// to always call GetClientFactory() and not cache the result.
	antreaCAProvider, _ := dynamiccertificates.NewDynamicCAFromConfigMapController(
		"antrea-ca",
		antreaNamespace,
		antreaCAConfigMapName,
		antreaCAConfigMapKey,
		kubeClient)
	provider := &antreaClientFactoryProvider{
		logger:            logger,
		config:            config,
		serverName:        serverName,
		caContentProvider: antreaCAProvider,
	}

	antreaCAProvider.AddListener(provider)
	return provider
}

// RunOnce runs the task a single time synchronously.
func (p *antreaClientFactoryProvider) RunOnce() error {
	return p.updateClientFactory()
}

// Run starts the caContentProvider, which watches the ConfigMap and notifies changes
// by calling Enqueue.
func (p *antreaClientFactoryProvider) Run(ctx context.Context) {
	p.caContentProvider.Run(ctx, 1)
}

// Enqueue implements dynamiccertificates.Listener. It will be called by caContentProvider
// when caBundle is updated.
func (p *antreaClientFactoryProvider) Enqueue() {
	if err := p.updateClientFactory(); err != nil {
		p.logger.Error(err, "Failed to update Antrea client factory")
	}
}

// GetClientFactory returns the current factory and the generation of the CA bundle it was built
// for. The generation is used as part of the per-session transport cache key.
func (p *antreaClientFactoryProvider) GetClientFactory() (*k8s.ClientFactory, int, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	if p.factory == nil {
		return nil, 0, fmt.Errorf("Antrea client is not ready")
	}
	return p.factory, p.generation, nil
}

func (p *antreaClientFactoryProvider) updateClientFactory() error {
	caBundle := p.caContentProvider.CurrentCABundleContent()
	if caBundle == nil {
		p.logger.Info("Didn't get CA certificate, skip updating Antrea Client")
		return nil
	}
	var kubeConfig *rest.Config
	if p.config != nil {
		kubeConfig = rest.CopyConfig(p.config)
	} else {
		var err error
		if kubeConfig, err = inClusterConfig(caBundle); err != nil {
			return err
		}
	}
	// name used in the server certificate
	kubeConfig.CAData = caBundle
	kubeConfig.ServerName = p.serverName

	// The SA transport keeps antrea-ui's own credential, and is only used as the base for
	// impersonated (static admin password) requests.
	saTransport, err := rest.TransportFor(kubeConfig)
	if err != nil {
		return err
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	generation := p.generation + 1
	factory, err := k8s.NewClientFactory(kubeConfig, saTransport, transportKey(generation))
	if err != nil {
		return err
	}
	p.logger.Info("Updating Antrea client with the new CA bundle")
	p.factory = factory
	p.generation = generation

	return nil
}

// transportKey namespaces the Antrea Service transports a session caches, and includes the CA
// bundle generation so a rotation invalidates them.
func transportKey(generation int) string {
	return fmt.Sprintf("antreasvc/%d", generation)
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
