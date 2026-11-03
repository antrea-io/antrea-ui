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

const caFetchTimeout = 10 * time.Second

// EndpointSubscriber serves one flow source. It reads the source's CA bundle at the start of
// every stream and builds a gRPC subscriber for that stream, so a rotated CA applies to the next
// stream and a missing or invalid CA only fails the requests that need it: it never prevents the
// UI server from starting.
type EndpointSubscriber struct {
	Logger                                      logr.Logger
	Client                                      kubernetes.Interface
	Name                                        string
	Address, Namespace, CAConfigMap, ServerName string
	InsecureSkipVerify                          bool
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
		cfg, err := s.connectionConfig(ctx)
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
		err = relay(ctx, subscriber, scope, filter, func() error { close(ready); return nil }, func(event apisv1.FlowStreamEvent) error {
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

func (s *EndpointSubscriber) connectionConfig(ctx context.Context) (GRPCConfig, error) {
	var ca []byte
	if s.CAConfigMap != "" {
		fetchCtx, cancel := context.WithTimeout(ctx, caFetchTimeout)
		defer cancel()
		cm, err := s.Client.CoreV1().ConfigMaps(s.Namespace).Get(fetchCtx, s.CAConfigMap, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return GRPCConfig{}, internalStreamError(fmt.Errorf("backend is not allowed to read the CA %s/%s of flow source %q", s.Namespace, s.CAConfigMap, s.Name))
			}
			return GRPCConfig{}, dependencyUnavailable(fmt.Sprintf("cannot read the CA %s/%s of flow source %q: %v", s.Namespace, s.CAConfigMap, s.Name, err))
		}
		ca = []byte(cm.Data["ca.crt"])
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName, InsecureSkipVerify: s.InsecureSkipVerify} // #nosec G402 -- explicit development override, never inferred.
	if s.CAConfigMap != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return GRPCConfig{}, internalStreamError(fmt.Errorf("the CA %s/%s of flow source %q is empty or invalid", s.Namespace, s.CAConfigMap, s.Name))
		}
		tlsConfig.RootCAs = roots
	}
	// The digest versions the connection key, so a changed endpoint or trust retires the
	// connections built for the old one. The name stays ahead of the first "/": the session
	// treats that prefix as the upstream, so sources never retire each other's connections.
	digest := sha256.Sum256(fmt.Appendf(ca, "|%s|%s|%t", s.Address, s.ServerName, s.InsecureSkipVerify))
	return GRPCConfig{Name: s.Name, Address: s.Address, TLSConfig: tlsConfig,
		ConnectionKey: fmt.Sprintf("%s%s/%x", flowSourceConnKeyPrefix, s.Name, digest), AdminTokenSource: s.AdminTokenSource}, nil
}
