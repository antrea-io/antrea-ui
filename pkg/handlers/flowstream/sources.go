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
	"crypto/x509"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/k8s"
)

const caFetchTimeout = 30 * time.Second

// NewRoutedSubscriber builds a RoutedSubscriber over the configured flow sources, in order: the
// first source with the capability a request needs is its default. Each source gets one gRPC
// subscriber, built here, so its CA is read once and a bad caConfigMap fails startup. The
// caller must Close the result.
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
		subscriber, err := newSourceSubscriber(ctx, logger, client, cfg, adminTokenSource)
		if err != nil {
			router.Close()
			return nil, err
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

func newSourceSubscriber(
	ctx context.Context,
	logger logr.Logger,
	client kubernetes.Interface,
	cfg *serverconfig.FlowSourceConfig,
	adminTokenSource *k8s.AdminTokenSource,
) (*GRPCFlowStreamSubscriber, error) {
	logger.Info("Flow source enabled", "source", cfg.Name, "capabilities", cfg.Capabilities, "address", cfg.Address)

	var caData []byte
	if cfg.CAConfigMap != "" {
		fetchCtx, cancel := context.WithTimeout(ctx, caFetchTimeout)
		defer cancel()
		logger.Info("Fetching flow source CA cert", "source", cfg.Name, "namespace", cfg.Namespace, "configMap", cfg.CAConfigMap)
		cm, err := client.CoreV1().ConfigMaps(cfg.Namespace).Get(fetchCtx, cfg.CAConfigMap, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to get CA configmap %s/%s of flow source %q: %w", cfg.Namespace, cfg.CAConfigMap, cfg.Name, err)
		}
		caCert, ok := cm.Data["ca.crt"]
		if !ok || caCert == "" {
			return nil, fmt.Errorf("CA configmap %s/%s of flow source %q is missing or has empty 'ca.crt' key", cfg.Namespace, cfg.CAConfigMap, cfg.Name)
		}
		caData = []byte(caCert)
	}

	tlsCfg, err := buildSourceTLSConfig(logger, cfg, caData)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config for flow source %q: %w", cfg.Name, err)
	}
	subscriber, err := NewGRPCFlowStreamSubscriber(logger, GRPCConfig{
		Name:             cfg.Name,
		Address:          cfg.Address,
		TLSConfig:        tlsCfg,
		AdminTokenSource: adminTokenSource,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC flow stream handler for flow source %q: %w", cfg.Name, err)
	}
	return subscriber, nil
}

func buildSourceTLSConfig(logger logr.Logger, cfg *serverconfig.FlowSourceConfig, caData []byte) (*tls.Config, error) {
	if cfg.InsecureSkipVerify {
		logger.Info("WARNING: TLS certificate verification is disabled for the flow source gRPC connection. This should only be used for development/testing.", "source", cfg.Name)
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true
	} else if len(caData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}
	return tlsCfg, nil
}
