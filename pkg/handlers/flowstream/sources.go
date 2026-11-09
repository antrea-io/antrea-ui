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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"

	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/k8s"
)

// NewRoutedSubscriber builds a RoutedSubscriber over the configured flow sources, in order: the
// first source with the capability a request needs is its default. Each source owns a
// WatchedSubscriber, so a missing or rotated CA affects only that source, and so does a source
// which discovers its Service. The caller must Run the result to start its CA and Service
// watchers, and Close it when done.
func NewRoutedSubscriber(
	logger logr.Logger,
	client kubernetes.Interface,
	configs []serverconfig.FlowSourceConfig,
	adminTokenSource *k8s.AdminTokenSource,
) (*RoutedSubscriber, error) {
	router := &RoutedSubscriber{}
	for i := range configs {
		cfg := &configs[i]
		if cfg.Discovery.Enabled {
			logger.Info("Flow source enabled", "source", cfg.Name, "capabilities", cfg.Capabilities, "discovery", true)
		} else {
			logger.Info("Flow source enabled", "source", cfg.Name, "capabilities", cfg.Capabilities, "address", cfg.Address)
		}
		subscriber, err := NewWatchedSubscriber(logger, client, WatchedConfig{
			Name:               cfg.Name,
			Address:            cfg.Address,
			Namespace:          cfg.Namespace,
			CAConfigMap:        cfg.CAConfigMap,
			Discovery:          cfg.Discovery,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			ServerName:         cfg.ServerName,
			AdminTokenSource:   adminTokenSource,
		})
		if err != nil {
			router.Close()
			return nil, fmt.Errorf("failed to create subscriber for flow source %q: %w", cfg.Name, err)
		}
		router.Sources = append(router.Sources, FlowSource{
			Name:       cfg.Name,
			Watch:      cfg.HasCapability(serverconfig.FlowSourceCapabilityWatch),
			List:       cfg.HasCapability(serverconfig.FlowSourceCapabilityList),
			Subscriber: subscriber,
		})
	}
	return router, nil
}

// Run starts each source's CA and Service watchers and blocks until stopCh is closed.
func (s *RoutedSubscriber) Run(stopCh <-chan struct{}) {
	var wg sync.WaitGroup
	for _, source := range s.Sources {
		if runner, ok := source.Subscriber.(interface{ Run(<-chan struct{}) }); ok {
			wg.Go(func() { runner.Run(stopCh) })
		}
	}
	<-stopCh
	wg.Wait()
}
