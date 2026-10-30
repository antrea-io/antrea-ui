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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/k8s"
)

// EndpointSubscriber loads current public trust for each request. Missing Service
// or CA dependencies never stop the UI server from starting.
type EndpointSubscriber struct {
	Logger                                      logr.Logger
	Client                                      kubernetes.Interface
	Address, Namespace, CAConfigMap, ServerName string
	InsecureSkipVerify                          bool
	Historical                                  bool
	Discovery                                   *ServiceDiscovery
	AdminTokenSource                            *k8s.AdminTokenSource
}

func (s *EndpointSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flows := make(chan apisv1.FlowStreamEvent)
	errs := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		defer close(errs)
		defer close(flows)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		cfg, changed, err := s.connectionConfig(ctx)
		if err != nil {
			errs <- err
			return
		}
		subscriber, err := NewGRPCFlowStreamSubscriber(s.Logger, cfg)
		if err != nil {
			errs <- internalStreamError(err)
			return
		}
		defer subscriber.Close()
		err = relay(ctx, subscriber, scope, filter, changed, func() error { close(ready); return nil }, func(event apisv1.FlowStreamEvent) error {
			select {
			case flows <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()
	return flows, errs, ready
}

func (s *EndpointSubscriber) connectionConfig(ctx context.Context) (GRPCConfig, <-chan struct{}, error) {
	address, namespace := s.Address, s.Namespace
	var revision string
	var changed <-chan struct{}
	if s.Discovery != nil {
		var endpoint DiscoveredEndpoint
		var err error
		endpoint, changed, err = s.Discovery.Current()
		if err != nil {
			return GRPCConfig{}, changed, err
		}
		address, namespace = endpoint.Address, endpoint.Namespace
		revision = endpoint.Revision
	}
	var ca []byte
	if s.CAConfigMap != "" {
		fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cm, err := s.Client.CoreV1().ConfigMaps(namespace).Get(fetchCtx, s.CAConfigMap, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return GRPCConfig{}, changed, internalStreamError(fmt.Errorf("backend is not allowed to read flow source CA %s/%s", namespace, s.CAConfigMap))
			}
			return GRPCConfig{}, changed, dependencyUnavailable(fmt.Sprintf("cannot read flow source CA %s/%s: %v", namespace, s.CAConfigMap, err))
		}
		ca = []byte(cm.Data["ca.crt"])
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName, InsecureSkipVerify: s.InsecureSkipVerify} // #nosec G402 -- explicit development override, never inferred.
	if s.CAConfigMap != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return GRPCConfig{}, changed, internalStreamError(fmt.Errorf("flow source CA %s/%s is empty or invalid", namespace, s.CAConfigMap))
		}
		tlsConfig.RootCAs = roots
	}
	role := "flow-aggregator-grpc"
	if s.Historical {
		role = "flow-reader-grpc"
	}
	// Versioned cache keys retire connections when endpoint or trust changes, while
	// keeping live and historical client-certificate sessions in separate pools.
	digest := sha256.Sum256(fmt.Appendf(ca, "|%s|%s|%t|%s", address, s.ServerName, s.InsecureSkipVerify, revision))
	return GRPCConfig{Address: address, TLSConfig: tlsConfig, Historical: s.Historical,
		ConnectionKey: fmt.Sprintf("%s/%x", role, digest), AdminTokenSource: s.AdminTokenSource}, changed, nil
}
