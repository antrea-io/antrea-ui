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
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/k8s"
)

const (
	flowSourceCAKey = "ca.crt"

	// caWaitReminderInterval is how often a WatchedSubscriber, while it has no CA bundle yet,
	// logs a message about why that may be taking long.
	caWaitReminderInterval = 30 * time.Second
)

// errWatchedSubscriberUnavailable is what a client sees while a WatchedSubscriber has no
// subscriber. The cause is only logged: it names Kubernetes objects and is meant for the operator,
// not for every user who can open a flow stream.
// An unloaded CA is a backend trust configuration issue, so this is internal rather than
// flow_source_unavailable. It is retryable because loading the CA makes the source usable.
var errWatchedSubscriberUnavailable = retryableInternalStreamError(errors.New(
	"flow visibility is unavailable: the backend has not loaded the flow source CA certificate, " +
		"check the antrea-ui backend logs and the flowSources configuration"))

// errDiscoveredCAUnavailable is what a client sees when the backend cannot follow the CA of a
// discovered flow source at all. Like errWatchedSubscriberUnavailable, it does not name the cause.
var errDiscoveredCAUnavailable = internalStreamError(errors.New(
	"flow visibility is unavailable: the backend cannot follow the CA certificate of the discovered " +
		"flow source, check the antrea-ui backend logs"))

// WatchedConfig holds the settings of a WatchedSubscriber.
type WatchedConfig struct {
	// Name is the configured source name, included in logs and passed to GRPCConfig.
	Name string
	// Address is the flow source's gRPC address. It is ignored when Discovery is enabled.
	Address string
	// Namespace and CAConfigMap locate the ConfigMap whose "ca.crt" key holds the CA bundle that
	// verifies the source's certificate. Namespace is required when CAConfigMap is set, and
	// ignored when Discovery is enabled: the CA is then read in the namespace of the Service.
	// An empty CAConfigMap means there is nothing to watch: the system trust store is used.
	Namespace   string
	CAConfigMap string
	// Discovery, when enabled, finds the source's Service by label instead of using Address, see
	// ServiceDiscovery.
	Discovery serverconfig.FlowSourceDiscoveryConfig
	// InsecureSkipVerify disables verification of the source's certificate, so there is
	// no CA to watch either.
	InsecureSkipVerify bool
	// ServerName overrides the name the source's certificate is verified against.
	ServerName string
	// AdminTokenSource is passed to the GRPCFlowStreamSubscriber, see GRPCConfig.
	AdminTokenSource *k8s.AdminTokenSource
}

// caBundleProvider is the part of a dynamiccertificates.CAContentProvider a WatchedSubscriber
// reads.
type caBundleProvider interface {
	CurrentCABundleContent() []byte
}

// watchTarget is where a WatchedSubscriber connects: an address, and the CA ConfigMap that
// verifies it. A source with a configured address has one target for its lifetime. With discovery,
// every change of the Service's address or CA namespace replaces the target, which stops the CA
// controller of the previous one so that its late notifications are ignored.
type watchTarget struct {
	address, namespace string
	// revision is the Service revision of a discovered target, see DiscoveredEndpoint. Only
	// retarget reads or writes it.
	revision string
	// caProvider and run are nil when there is nothing to watch. run starts the CA controller and
	// blocks until ctx is done.
	caProvider caBundleProvider
	run        func(ctx context.Context)
	// cancel stops a controller started by retarget.
	cancel context.CancelFunc
}

// WatchedSubscriber is a FlowStreamSubscriber that follows one flow source's CA ConfigMap. It
// builds a GRPCFlowStreamSubscriber for each CA bundle it sees, swaps it in and closes the one it
// replaces, so a CA rotation takes effect without restarting antrea-ui. This closes the shared
// connection for bearer and admin-password sessions: live clients reconnect through the new
// subscriber, while interrupted finite queries fail. Client-certificate sessions keep their own
// connections and ongoing streams; their next flow request to this source replaces the connection
// using the new CA.
//
// Until the first bundle is loaded, Subscribe fails with a retryable error, which the UI shows as
// a banner: a missing ConfigMap does not keep the rest of antrea-ui from running. Afterwards, the
// CA controller keeps the last good bundle, and does not notify this type, when the ConfigMap is
// deleted or holds an empty or invalid bundle, so the current subscriber stays. A rebuild that
// fails keeps it too.
//
// Nothing is built from a request. With no ConfigMap to watch (see WatchedConfig), the subscriber
// is built once, by NewWatchedSubscriber.
//
// With discovery (see WatchedConfig), the target is not known in advance. The source is
// unavailable until its Service is found and, when it has a CA ConfigMap, that ConfigMap in the
// Service's namespace is loaded. When the Service changes, the subscriber is retired: besides
// what a CA rotation does, that ends the streams of client-certificate sessions, since the
// endpoint they run against is gone. The connection key prefix and the generation carry on
// across targets, so a session closes the connections it dialed for any earlier one.
type WatchedSubscriber struct {
	logger logr.Logger
	cfg    WatchedConfig
	client kubernetes.Interface
	// discovery is not nil for a source which enables discovery.
	discovery *ServiceDiscovery
	cancel    context.CancelFunc
	ctx       context.Context
	// connKeyPrefix isolates this source's session connections and stays fixed across CA rotations
	// and targets.
	connKeyPrefix string
	// controllers tracks the CA controllers retarget started.
	controllers sync.WaitGroup

	// buildMu serializes rebuilds and retargets, so that generations are handed out in order.
	buildMu sync.Mutex
	// mu protects target, discoveryErr, runCtx, current, generation and closed.
	mu sync.RWMutex
	// target is where the source connects. Nil while discovery has found nothing usable.
	target *watchTarget
	// discoveryErr is why a discovering source has no target. It is nil while there is one.
	discoveryErr error
	// runCtx is the context of Run, which the CA controllers of discovered targets run in. Nil
	// until Run is called, then w.ctx is used.
	runCtx  context.Context
	current *GRPCFlowStreamSubscriber
	// generation counts the subscribers built so far. It is part of the per-session connection
	// cache key, see GRPCConfig.Generation.
	generation int
	closed     bool
}

var (
	_ FlowStreamSubscriber              = &WatchedSubscriber{}
	_ dynamiccertificates.Listener      = &WatchedSubscriber{}
	_ dynamiccertificates.Listener      = targetListener{}
	_ interface{ Close() error }        = &WatchedSubscriber{}
	_ interface{ Run(<-chan struct{}) } = &WatchedSubscriber{}
)

// targetListener notifies a WatchedSubscriber of a new CA bundle of one target. A notification
// of a target which was replaced since is ignored.
type targetListener struct {
	w *WatchedSubscriber
	t *watchTarget
}

// Enqueue implements dynamiccertificates.Listener.
func (l targetListener) Enqueue() { l.w.enqueue(l.t) }

// NewWatchedSubscriber returns a WatchedSubscriber for cfg. When there is a ConfigMap to watch it
// is not loaded until Run starts; otherwise the subscriber is built here, and an error means the
// configuration cannot work. With discovery, nothing is built or watched until Run finds the
// Service.
func NewWatchedSubscriber(logger logr.Logger, client kubernetes.Interface, cfg WatchedConfig) (*WatchedSubscriber, error) {
	logger = logger.WithValues("source", cfg.Name)
	if cfg.Discovery.Enabled {
		w := newWatchedSubscriber(logger, cfg, client, nil)
		w.discoveryErr = errDiscoveryNotStarted(cfg.Name)
		discovery, err := NewServiceDiscovery(w.logger, client, cfg.Name, cfg.Discovery.LabelSelector, w.retarget)
		if err != nil {
			w.Close()
			return nil, fmt.Errorf("failed to start discovery of the flow source: %w", err)
		}
		w.discovery = discovery
		return w, nil
	}
	target := &watchTarget{address: cfg.Address, namespace: cfg.Namespace}
	if !cfg.watchesCA() {
		w := newWatchedSubscriber(logger, cfg, client, target)
		if err := w.rebuild(target, nil, false); err != nil {
			w.Close()
			return nil, err
		}
		return w, nil
	}
	controller, err := dynamiccertificates.NewDynamicCAFromConfigMapController(
		"flow-source-"+cfg.Name+"-ca", cfg.Namespace, cfg.CAConfigMap, flowSourceCAKey, client)
	if err != nil {
		return nil, fmt.Errorf("failed to create the flow source CA controller: %w", err)
	}
	target.caProvider = controller
	target.run = func(ctx context.Context) { controller.Run(ctx, 1) }
	w := newWatchedSubscriber(logger, cfg, client, target)
	controller.AddListener(targetListener{w: w, t: target})
	return w, nil
}

func errDiscoveryNotStarted(name string) error {
	return dependencyUnavailable(fmt.Sprintf("waiting for the discovery of flow source %q", name))
}

func (c WatchedConfig) watchesCA() bool {
	return c.CAConfigMap != "" && !c.InsecureSkipVerify
}

// newWatchedSubscriber returns a WatchedSubscriber with nothing built yet. target is nil for a
// source which discovers its Service.
func newWatchedSubscriber(logger logr.Logger, cfg WatchedConfig, client kubernetes.Interface, target *watchTarget) *WatchedSubscriber {
	ctx, cancel := context.WithCancel(context.Background())
	return &WatchedSubscriber{
		logger:        logger,
		cfg:           cfg,
		client:        client,
		cancel:        cancel,
		ctx:           ctx,
		connKeyPrefix: newFlowStreamConnKeyPrefix(),
		target:        target,
	}
}

// Run starts following the CA ConfigMap, or the Service when discovering, and blocks until stopCh
// is closed. It returns right away when there is nothing to follow.
func (w *WatchedSubscriber) Run(stopCh <-chan struct{}) {
	w.mu.RLock()
	target := w.target
	w.mu.RUnlock()
	if w.discovery == nil && (target == nil || target.run == nil) {
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	w.mu.Lock()
	w.runCtx = ctx
	w.mu.Unlock()
	go func() {
		select {
		case <-stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// The CA controller reports through klog, which is not routed to this logger, so a ConfigMap
	// that cannot be read (missing, or no list/watch permission on it) would otherwise leave no
	// trace here. Remind periodically, while there is no subscriber, that flow visibility is
	// waiting for one. Logging this at startup instead would be noise on a healthy deployment,
	// where the bundle arrives right after, and a single line would be long gone from the log by
	// the time anyone looks into a permanent mistake.
	go func() {
		ticker := time.NewTicker(caWaitReminderInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// A source can become unavailable again after it has been available, when its
				// Service goes away or several match, so this keeps going for as long as Run does.
				if !w.available() {
					w.logWaiting()
				}
			}
		}
	}()
	if w.discovery != nil {
		w.discovery.Run(ctx)
		cancel()
		w.controllers.Wait()
		return
	}
	target.run(ctx)
}

func (w *WatchedSubscriber) logWaiting() {
	w.mu.RLock()
	target, discoveryErr := w.target, w.discoveryErr
	w.mu.RUnlock()
	if target == nil {
		w.logger.Info("Flow visibility is still waiting for the flow source to be discovered: "+
			"check that exactly one Service matches the discovery label selector, with a "+
			FlowServicePortName+" TCP port, and that antrea-ui may list and watch Services",
			"reason", discoveryErr.Error())
		return
	}
	w.logger.Info("Flow visibility is still waiting for the flow source CA certificate: "+
		"check that the ConfigMap exists, has a ca.crt key holding a valid, non-empty "+
		"PEM bundle, and that antrea-ui may get, list and watch it",
		"namespace", target.namespace, "configMap", w.cfg.CAConfigMap)
}

// Enqueue implements dynamiccertificates.Listener: the CA controller calls it when the bundle
// changes to a new, valid one. It never calls it for a missing ConfigMap or an empty or invalid
// bundle.
func (w *WatchedSubscriber) Enqueue() {
	w.mu.RLock()
	target := w.target
	w.mu.RUnlock()
	w.enqueue(target)
}

func (w *WatchedSubscriber) enqueue(t *watchTarget) {
	if t == nil || t.caProvider == nil {
		return
	}
	if err := w.rebuild(t, t.caProvider.CurrentCABundleContent(), false); err != nil {
		w.logger.Error(err, "Failed to apply the flow source CA bundle, keeping the current one",
			"namespace", t.namespace, "configMap", w.cfg.CAConfigMap)
	}
}

// rebuild builds a subscriber for caData, swaps it in and closes the previous one, unless t is no
// longer the target. When retire is set it retires the previous one instead of closing it.
func (w *WatchedSubscriber) rebuild(t *watchTarget, caData []byte, retire bool) error {
	w.buildMu.Lock()
	defer w.buildMu.Unlock()
	return w.rebuildLocked(t, caData, retire)
}

func (w *WatchedSubscriber) rebuildLocked(t *watchTarget, caData []byte, retire bool) error {
	w.mu.RLock()
	generation := w.generation + 1
	stale := w.closed || w.target != t
	w.mu.RUnlock()
	if stale {
		return nil
	}

	tlsCfg, err := buildTLSConfig(w.logger, w.cfg, caData)
	if err != nil {
		return fmt.Errorf("failed to build TLS config for the flow source: %w", err)
	}
	sub, err := NewGRPCFlowStreamSubscriber(w.logger, GRPCConfig{
		Name:             w.cfg.Name,
		Address:          t.address,
		TLSConfig:        tlsCfg,
		AdminTokenSource: w.cfg.AdminTokenSource,
		ConnKeyPrefix:    w.connKeyPrefix,
		Generation:       generation,
	})
	if err != nil {
		return fmt.Errorf("failed to create gRPC flow stream handler: %w", err)
	}

	w.mu.Lock()
	if w.closed || w.target != t {
		w.mu.Unlock()
		sub.Close()
		return nil
	}
	previous := w.current
	w.current = sub
	w.generation = generation
	w.mu.Unlock()

	if previous != nil {
		if retire {
			w.logger.Info("The flow source endpoint changed, replaced the connection", "generation", generation)
			previous.Retire()
		} else {
			w.logger.Info("The flow source CA bundle changed, replaced the connection", "generation", generation)
			previous.Close()
		}
	} else if caData != nil {
		w.logger.Info("Loaded the flow source CA bundle, flow visibility is available")
	} else {
		w.logger.Info("Flow visibility is available", "address", t.address)
	}
	return nil
}

// retarget points a discovering source at the endpoint its Service has now, or makes it
// unavailable when discoveryErr is not nil. It is the callback of the source's ServiceDiscovery.
//
// A change of address or namespace replaces the target: the source stays unavailable until the CA
// of the new one is loaded, and the old subscriber is retired. A change of revision alone keeps
// the target and its CA, and replaces the subscriber, as the Service now reaches other backends.
func (w *WatchedSubscriber) retarget(endpoint DiscoveredEndpoint, discoveryErr error) {
	w.buildMu.Lock()
	defer w.buildMu.Unlock()

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	old := w.target
	if discoveryErr == nil && old != nil && old.address == endpoint.Address && old.namespace == endpoint.Namespace {
		changed := old.revision != endpoint.Revision
		old.revision = endpoint.Revision
		w.mu.Unlock()
		if !changed {
			return
		}
		var caData []byte
		if old.caProvider != nil {
			if caData = old.caProvider.CurrentCABundleContent(); len(caData) == 0 {
				// The CA is not loaded yet, so there is no subscriber to replace: the one built
				// when it is will use the new revision.
				return
			}
		}
		if err := w.rebuildLocked(old, caData, true); err != nil {
			w.logger.Error(err, "Failed to apply the discovered flow source endpoint, keeping the current one")
		}
		return
	}

	var next *watchTarget
	if discoveryErr == nil {
		next = &watchTarget{address: endpoint.Address, namespace: endpoint.Namespace, revision: endpoint.Revision}
		if w.cfg.watchesCA() {
			controller, err := dynamiccertificates.NewDynamicCAFromConfigMapController(
				"flow-source-"+w.cfg.Name+"-ca", endpoint.Namespace, w.cfg.CAConfigMap, flowSourceCAKey, w.client)
			if err != nil {
				w.logger.Error(err, "Failed to create the flow source CA controller", "namespace", endpoint.Namespace)
				next, discoveryErr = nil, errDiscoveredCAUnavailable
			} else {
				next.caProvider = controller
				next.run = func(ctx context.Context) { controller.Run(ctx, 1) }
				controller.AddListener(targetListener{w: w, t: next})
			}
		}
	}
	previous := w.current
	w.current = nil
	w.target = next
	w.discoveryErr = discoveryErr
	w.mu.Unlock()

	if old != nil && old.cancel != nil {
		old.cancel()
	}
	switch {
	case discoveryErr != nil && (old != nil || previous != nil):
		// The source was targeting an endpoint and no longer is: say why, since nothing else in
		// the log does.
		w.logger.Info("The flow source is unavailable, its Service cannot be used", "reason", discoveryErr.Error())
	case previous != nil:
		w.logger.Info("The flow source endpoint changed, the source is unavailable until its new endpoint is ready")
	}
	if previous != nil {
		previous.Retire()
	}
	if next == nil {
		return
	}
	if next.run == nil {
		if err := w.rebuildLocked(next, nil, false); err != nil {
			w.logger.Error(err, "Failed to apply the discovered flow source endpoint")
		}
		return
	}
	ctx, cancel := context.WithCancel(w.runContext())
	next.cancel = cancel
	w.controllers.Go(func() { next.run(ctx) })
}

// runContext is the context the CA controllers of discovered targets run in.
func (w *WatchedSubscriber) runContext() context.Context {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.runCtx != nil {
		return w.runCtx
	}
	return w.ctx
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
// A Subscribe using the shared connection that reads the current subscriber just as a new bundle
// replaces it fails or ends on the closed one like any stream on it; live clients reconnect.
func (w *WatchedSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	w.mu.RLock()
	current, discoveryErr := w.current, w.discoveryErr
	w.mu.RUnlock()
	if current != nil {
		return current.Subscribe(ctx, scope, filter)
	}
	unavailable := error(errWatchedSubscriberUnavailable)
	if discoveryErr != nil {
		unavailable = discoveryErr
	}
	flows := make(chan apisv1.FlowStreamEvent)
	close(flows)
	errs := make(chan error, 1)
	errs <- unavailable
	close(errs)
	return flows, errs, make(chan struct{})
}

// Close stops following the ConfigMap and the Service, and closes the current subscriber.
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
		logger.Info("WARNING: TLS certificate verification is disabled for the flow source gRPC connection. This should only be used for development/testing.")
	}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if cfg.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true
	} else if len(caData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, errors.New("failed to parse flow source CA cert")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	}
	return tlsCfg, nil
}
