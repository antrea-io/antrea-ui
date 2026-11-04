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
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"

	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/k8s"
)

// NewRoutedSubscriber builds a RoutedSubscriber over the configured flow sources, in order: the
// first source with the capability a request needs is its default. Nothing is dialed or read
// here: each source resolves its endpoint and CA when a stream starts (see EndpointSubscriber),
// so a missing CA ConfigMap never prevents startup. The Service watch of a source which enables
// discovery runs until ctx is done.
func NewRoutedSubscriber(
	ctx context.Context,
	logger logr.Logger,
	client kubernetes.Interface,
	configs []serverconfig.FlowSourceConfig,
	adminTokenSource *k8s.AdminTokenSource,
) (*RoutedSubscriber, error) {
	router := &RoutedSubscriber{}
	for i := range configs {
		cfg := &configs[i]
		logger.Info("Flow source enabled", "source", cfg.Name, "capabilities", cfg.Capabilities, "address", cfg.Address)
		if cfg.InsecureSkipVerify {
			logger.Info("WARNING: TLS certificate verification is disabled for the flow source gRPC connection. This should only be used for development/testing.", "source", cfg.Name)
		}
		endpoint := &EndpointSubscriber{
			Logger: logger, Client: client, Name: cfg.Name,
			Address: cfg.Address, Namespace: cfg.Namespace, CAConfigMap: cfg.CAConfigMap,
			ServerName: cfg.ServerName, InsecureSkipVerify: cfg.InsecureSkipVerify,
			AdminTokenSource: adminTokenSource,
		}
		if cfg.Discovery.Enabled {
			discovery, err := NewServiceDiscovery(client, cfg.Name, cfg.Discovery.LabelSelector)
			if err != nil {
				return nil, fmt.Errorf("failed to start discovery of flow source %q: %w", cfg.Name, err)
			}
			go discovery.Run(ctx)
			endpoint.Discovery = discovery
		}
		router.Sources = append(router.Sources, FlowSource{
			Name:       cfg.Name,
			Watch:      cfg.HasCapability(serverconfig.FlowSourceCapabilityWatch),
			List:       cfg.HasCapability(serverconfig.FlowSourceCapabilityList),
			Subscriber: endpoint,
		})
	}
	return router, nil
}
