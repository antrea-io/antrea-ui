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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	"k8s.io/client-go/kubernetes"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/k8s"
)

const (
	// DefaultFlowAggregatorNamespace is the Namespace of the Flow Aggregator CA ConfigMap when
	// WatchedConfig.Namespace is empty.
	DefaultFlowAggregatorNamespace = "flow-aggregator"
	flowAggregatorCAKey            = "ca.crt"

	// caWaitReminderInterval is how often a WatchedSubscriber, while it has no CA bundle yet,
	// logs a message about why that may be taking long.
	caWaitReminderInterval = 30 * time.Second
)

// errWatchedSubscriberUnavailable is what a client sees while a WatchedSubscriber has no
// subscriber. The cause is only logged: it names Kubernetes objects and is meant for the operator,
// not for every user who can open a flow stream.
var errWatchedSubscriberUnavailable = retryableInternalStreamError(errors.New(
	"flow visibility is unavailable: the backend has not loaded the Flow Aggregator CA certificate, " +
		"check the antrea-ui backend logs and the flowAggregator configuration"))

// WatchedConfig holds the settings of a WatchedSubscriber.
type WatchedConfig struct {
	// Address is the Flow Aggregator gRPC address.
	Address string
	// Namespace and CAConfigMap locate the ConfigMap whose "ca.crt" key holds the CA bundle that
	// verifies the Flow Aggregator's certificate. Namespace defaults to
	// DefaultFlowAggregatorNamespace. An empty CAConfigMap means there is nothing to watch: the
	// system trust store is used.
	Namespace   string
	CAConfigMap string
	// InsecureSkipVerify disables verification of the Flow Aggregator's certificate, so there is
	// no CA to watch either.
	InsecureSkipVerify bool
	// ServerName overrides the name the Flow Aggregator's certificate is verified against.
	ServerName string
	// AdminTokenSource is passed to the GRPCFlowStreamSubscriber, see GRPCConfig.
	AdminTokenSource *k8s.AdminTokenSource
}

// caBundleProvider is the part of a dynamiccertificates.CAContentProvider a WatchedSubscriber
// reads.
type caBundleProvider interface {
	CurrentCABundleContent() []byte
}

// WatchedSubscriber is a FlowStreamSubscriber that follows the Flow Aggregator's CA ConfigMap. It
// builds a GRPCFlowStreamSubscriber for each CA bundle it sees, swaps it in and closes the one it
// replaces, so a CA rotation takes effect without restarting antrea-ui. A stream on the closed
// subscriber ends and its client reconnects through the new one.
//
// Until the first bundle is loaded, Subscribe fails with a retryable error, which the UI shows as
// a banner: a missing ConfigMap does not keep the rest of antrea-ui from running. Afterwards, the
// CA controller keeps the last good bundle, and does not notify this type, when the ConfigMap is
// deleted or holds an empty or invalid bundle, so the current subscriber stays. A rebuild that
// fails keeps it too.
//
// Nothing is built from a request. With no ConfigMap to watch (see WatchedConfig), the subscriber
// is built once, by NewWatchedSubscriber.
type WatchedSubscriber struct {
	logger     logr.Logger
	cfg        WatchedConfig
	caProvider caBundleProvider
	// run starts the CA controller and blocks until ctx is done. Nil when nothing is watched.
	run    func(ctx context.Context)
	cancel context.CancelFunc
	ctx    context.Context
	// connKeyPrefix isolates this source's session connections and stays fixed across CA rotations.
	connKeyPrefix string

	// buildMu serializes rebuilds, so that generations are handed out in order.
	buildMu sync.Mutex
	// mu protects current, generation and closed.
	mu      sync.RWMutex
	current *GRPCFlowStreamSubscriber
	// generation counts the subscribers built so far. It is part of the per-session connection
	// cache key, see GRPCConfig.Generation.
	generation int
	closed     bool
}

var (
	_ FlowStreamSubscriber              = &WatchedSubscriber{}
	_ dynamiccertificates.Listener      = &WatchedSubscriber{}
	_ interface{ Close() error }        = &WatchedSubscriber{}
	_ interface{ Run(<-chan struct{}) } = &WatchedSubscriber{}
)

// NewWatchedSubscriber returns a WatchedSubscriber for cfg. When there is a ConfigMap to watch it
// is not loaded until Run starts; otherwise the subscriber is built here, and an error means the
// configuration cannot work.
func NewWatchedSubscriber(logger logr.Logger, client kubernetes.Interface, cfg WatchedConfig) (*WatchedSubscriber, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultFlowAggregatorNamespace
	}
	if !cfg.watchesCA() {
		return newWatchedSubscriber(logger, cfg, nil, nil)
	}
	controller, err := dynamiccertificates.NewDynamicCAFromConfigMapController(
		"flow-aggregator-ca", cfg.Namespace, cfg.CAConfigMap, flowAggregatorCAKey, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create the Flow Aggregator CA controller: %w", err)
	}
	w, err := newWatchedSubscriber(logger, cfg, controller, func(ctx context.Context) { controller.Run(ctx, 1) })
	if err != nil {
		return nil, err
	}
	controller.AddListener(w)
	return w, nil
}

func (c WatchedConfig) watchesCA() bool {
	return c.CAConfigMap != "" && !c.InsecureSkipVerify
}

func newWatchedSubscriber(
	logger logr.Logger,
	cfg WatchedConfig,
	caProvider caBundleProvider,
	run func(ctx context.Context),
) (*WatchedSubscriber, error) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &WatchedSubscriber{
		logger:        logger,
		cfg:           cfg,
		caProvider:    caProvider,
		run:           run,
		ctx:           ctx,
		cancel:        cancel,
		connKeyPrefix: newFlowStreamConnKeyPrefix(),
	}
	if caProvider == nil {
		if err := w.rebuild(nil); err != nil {
			cancel()
			return nil, err
		}
	}
	return w, nil
}

// Run starts following the CA ConfigMap and blocks until stopCh is closed. It returns right away
// when there is no ConfigMap to watch.
func (w *WatchedSubscriber) Run(stopCh <-chan struct{}) {
	if w.run == nil {
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// The CA controller reports through klog, which is not routed to this logger, so a ConfigMap
	// that cannot be read (missing, or no list/watch permission on it) would otherwise leave no
	// trace here. Remind periodically, until a bundle is loaded, that flow visibility is waiting
	// for it. Logging this at startup instead would be noise on a healthy deployment, where the
	// bundle arrives right after, and a single line would be long gone from the log by the time
	// anyone looks into a permanent mistake.
	go func() {
		ticker := time.NewTicker(caWaitReminderInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if w.available() {
					return
				}
				w.logger.Info("Flow visibility is still waiting for the Flow Aggregator CA certificate: "+
					"check that the ConfigMap exists, has a non-empty ca.crt key, and that antrea-ui "+
					"may get, list and watch it",
					"namespace", w.cfg.Namespace, "configMap", w.cfg.CAConfigMap)
			}
		}
	}()
	w.run(ctx)
}

// Enqueue implements dynamiccertificates.Listener: the CA controller calls it when the bundle
// changes to a new, valid one. It never calls it for a missing ConfigMap or an empty or invalid
// bundle.
func (w *WatchedSubscriber) Enqueue() {
	if err := w.rebuild(w.caProvider.CurrentCABundleContent()); err != nil {
		w.logger.Error(err, "Failed to apply the Flow Aggregator CA bundle, keeping the current one",
			"namespace", w.cfg.Namespace, "configMap", w.cfg.CAConfigMap)
	}
}

// rebuild builds a subscriber for caData, swaps it in and closes the previous one.
func (w *WatchedSubscriber) rebuild(caData []byte) error {
	w.buildMu.Lock()
	defer w.buildMu.Unlock()

	w.mu.RLock()
	generation := w.generation + 1
	closed := w.closed
	w.mu.RUnlock()
	if closed {
		return nil
	}

	tlsCfg, err := buildTLSConfig(w.logger, w.cfg, caData)
	if err != nil {
		return fmt.Errorf("failed to build TLS config for the Flow Aggregator: %w", err)
	}
	sub, err := NewGRPCFlowStreamSubscriber(w.logger, GRPCConfig{
		Address:          w.cfg.Address,
		TLSConfig:        tlsCfg,
		AdminTokenSource: w.cfg.AdminTokenSource,
		ConnKeyPrefix:    w.connKeyPrefix,
		Generation:       generation,
	})
	if err != nil {
		return fmt.Errorf("failed to create gRPC flow stream handler: %w", err)
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		sub.Close()
		return nil
	}
	previous := w.current
	w.current = sub
	w.generation = generation
	w.mu.Unlock()

	if previous != nil {
		w.logger.Info("The Flow Aggregator CA bundle changed, replaced the connection", "generation", generation)
		previous.Close()
	} else if caData != nil {
		w.logger.Info("Loaded the Flow Aggregator CA bundle, flow visibility is available")
	}
	return nil
}

func (w *WatchedSubscriber) available() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.current != nil
}

// Subscribe delegates to the current subscriber. It never builds one: while there is none, it
// closes flowsCh and errCh after sending one retryable error on errCh, and never closes the ready
// channel.
//
// A Subscribe that reads the current subscriber just as a new bundle replaces it fails or ends on
// the closed one like any stream on it; its client reconnects.
func (w *WatchedSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	w.mu.RLock()
	current := w.current
	w.mu.RUnlock()
	if current != nil {
		return current.Subscribe(ctx, scope, filter)
	}
	flows := make(chan apisv1.FlowStreamEvent)
	close(flows)
	errs := make(chan error, 1)
	errs <- errWatchedSubscriberUnavailable
	close(errs)
	return flows, errs, make(chan struct{})
}

// Close stops following the ConfigMap and closes the current subscriber.
func (w *WatchedSubscriber) Close() error {
	w.cancel()
	w.mu.Lock()
	w.closed = true
	current := w.current
	w.current = nil
	w.mu.Unlock()
	if current == nil {
		return nil
	}
	return current.Close()
}

func buildTLSConfig(logger logr.Logger, cfg WatchedConfig, caData []byte) (*tls.Config, error) {
	if cfg.InsecureSkipVerify {
		logger.Info("WARNING: TLS certificate verification is disabled for the FlowAggregator gRPC connection. This should only be used for development/testing.")
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true
	} else if len(caData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, errors.New("failed to parse FlowAggregator CA cert")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}
	return tlsCfg, nil
}
