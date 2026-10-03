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

package supportbundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

const (
	gcInterval = time.Minute

	bundleFileName = "bundle.tar.gz"
	workDirName    = "work"
)

// Options configures a Manager.
type Options struct {
	Logger logr.Logger
	Config *serverconfig.SupportBundleConfig
	// Backend configures the collection of antrea-ui's own diagnostics.
	Backend BackendOptions
	// APIServerURL is the base URL of the Kubernetes apiserver, which apiServer sources are
	// relative to.
	APIServerURL *url.URL
	// APIServerTransport reaches the apiserver as antrea-ui's own ServiceAccount, which
	// impersonates AdminUserName for every request to an apiServer source.
	APIServerTransport http.RoundTripper
	// AdminUserName is the user name of the antrea-ui-admin ServiceAccount, which every source is
	// called as, whoever requested the bundle.
	AdminUserName string
	// NewAdminTokenSource returns a TokenSource minting antrea-ui-admin tokens for audience, to
	// present to an https source. It is called once per such source, and required if there is
	// any.
	NewAdminTokenSource func(audience string) TokenSource
}

type manager struct {
	logger             logr.Logger
	config             *serverconfig.SupportBundleConfig
	backend            *BackendOptions
	apiServerURL       *url.URL
	apiServerTransport http.RoundTripper
	adminUser          string
	extraSources       []source
	plugins            func() []apisv1.PluginManifest
	budget             *budget

	// ctx is canceled when Run's stopCh is closed, and every collection derives from it.
	ctx    context.Context
	cancel context.CancelCauseFunc

	mutex   sync.Mutex
	bundles map[string]*bundle
	// collecting counts the collections still running, including those of bundles deleted (or
	// expired) that have not stopped yet.
	collecting int
}

// bundle is one support bundle. Its exported state is guarded by manager.mutex.
type bundle struct {
	dir    string
	cancel context.CancelCauseFunc
	// done is closed once collect has returned and stopped writing to dir.
	done chan struct{}
	// workBytes and tarBytes are what the files under dir are charged to the budget.
	workBytes atomic.Int64
	tarBytes  atomic.Int64

	state apisv1.SupportBundle
}

func (b *bundle) snapshot() apisv1.SupportBundle {
	s := b.state
	s.Sources = append([]apisv1.SupportBundleSourceStatus(nil), b.state.Sources...)
	return s
}

// NewManager builds a Manager, and restores the bundles a previous run left in the bundle
// directory.
func NewManager(o Options) (Manager, error) {
	if err := os.MkdirAll(o.Config.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create support bundle directory: %w", err)
	}

	var extraSources []source
	for i := range o.Config.ExtraSources {
		cfg := &o.Config.ExtraSources[i]
		switch {
		case cfg.HTTPS != nil:
			if o.NewAdminTokenSource == nil {
				return nil, fmt.Errorf("support bundle source %q is reached by URL, but no admin token source is configured", cfg.Name)
			}
			s, err := newHTTPSSource(o.Logger, cfg.Name, cfg.HTTPS, o.NewAdminTokenSource(tokenAudiencePrefix+cfg.Name))
			if err != nil {
				return nil, fmt.Errorf("invalid support bundle source %q: %w", cfg.Name, err)
			}
			extraSources = append(extraSources, s)
		case cfg.APIServer != nil:
			extraSources = append(extraSources, newAPIServerSource(cfg.Name, apisv1.SupportBundleSourceKindExtra, o.APIServerURL, cfg.APIServer.Path, o.APIServerTransport, o.AdminUserName))
		default:
			return nil, fmt.Errorf("support bundle source %q has no https or apiServer", cfg.Name)
		}
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	backend := o.Backend
	m := &manager{
		logger:             o.Logger.WithName("supportbundle"),
		config:             o.Config,
		backend:            &backend,
		apiServerURL:       o.APIServerURL,
		apiServerTransport: o.APIServerTransport,
		adminUser:          o.AdminUserName,
		extraSources:       extraSources,
		plugins:            o.Backend.Plugins,
		budget:             &budget{max: o.Config.MaxTotalBytes},
		ctx:                ctx,
		cancel:             cancel,
		bundles:            make(map[string]*bundle),
	}
	if err := m.restore(); err != nil {
		cancel(nil)
		return nil, err
	}
	return m, nil
}

// pendingSource is a source to collect, or the reason one declared by a plugin cannot be.
type pendingSource struct {
	status apisv1.SupportBundleSourceStatus
	source source
}

// resolveSources lists the sources to collect for a new bundle: the operator's, then the ones the
// currently loaded plugins declare.
func (m *manager) resolveSources() []pendingSource {
	var sources []pendingSource
	for _, s := range m.extraSources {
		sources = append(sources, pendingSource{
			status: apisv1.SupportBundleSourceStatus{Name: s.name(), Kind: s.kind(), Status: apisv1.SupportBundleStatusCollecting},
			source: s,
		})
	}
	if m.plugins == nil {
		return sources
	}
	for _, manifest := range m.plugins() {
		if manifest.SupportBundle == nil {
			continue
		}
		status := apisv1.SupportBundleSourceStatus{Name: manifest.Name, Kind: apisv1.SupportBundleSourceKindPlugin, Status: apisv1.SupportBundleStatusCollecting}
		var s source
		switch {
		case !isSafeName(manifest.Name):
			// The name is the tarball's file name inside the bundle.
			status.Status = apisv1.SupportBundleStatusFailed
			status.Error = "plugin name cannot be used as a file name"
		case manifest.SupportBundle.APIServer != nil:
			s = newAPIServerSource(manifest.Name, apisv1.SupportBundleSourceKindPlugin, m.apiServerURL, manifest.SupportBundle.APIServer.Path, m.apiServerTransport, m.adminUser)
		default:
			m.logger.Info("Plugin declares a support bundle source this version does not support, skipping it", "plugin", manifest.Name)
			status.Status = apisv1.SupportBundleStatusFailed
			status.Error = "unsupported source declaration"
		}
		sources = append(sources, pendingSource{status: status, source: s})
	}
	return sources
}

func (m *manager) Create(requestedBy string, request *apisv1.SupportBundleRequest) (*apisv1.SupportBundle, error) {
	var since time.Duration
	if request != nil && request.Since != "" {
		var err error
		since, err = time.ParseDuration(request.Since)
		if err != nil || since <= 0 {
			return nil, fmt.Errorf("%w: since must be a positive duration, e.g. \"1h\"", ErrInvalidRequest)
		}
	}
	sources := m.resolveSources()

	m.mutex.Lock()
	defer m.mutex.Unlock()
	if len(m.bundles) >= m.config.MaxBundles {
		return nil, fmt.Errorf("%w: at most %d bundles are retained, delete one first", ErrLimitReached, m.config.MaxBundles)
	}
	if m.collecting >= m.config.MaxConcurrent {
		return nil, fmt.Errorf("%w: at most %d bundles may be collected at once", ErrLimitReached, m.config.MaxConcurrent)
	}
	// The budget is enforced on every write, so a bundle that outgrows it fails rather than
	// filling the volume. Refusing up front when not even one source of the maximum size would fit
	// spares the requester a collection that is likely to fail. Such a source needs twice its size
	// at peak: while it is copied into the bundle tarball, it is charged both as a work file and as
	// part of the tarball.
	if free := m.budget.max - m.budget.used.Load(); free < 2*m.config.MaxSourceBytes {
		return nil, fmt.Errorf("%w: not enough room left in supportBundle.maxTotalBytes, delete a bundle first", ErrLimitReached)
	}

	id := uuid.NewString()
	// In UTC, so that a restored bundle is identical to the one before the restart: JSON keeps
	// the offset only, and decodes a zero offset as UTC.
	now := time.Now().UTC()
	ctx, cancel := context.WithCancelCause(m.ctx)
	b := &bundle{
		dir:    filepath.Join(m.config.Directory, id),
		cancel: cancel,
		done:   make(chan struct{}),
		state: apisv1.SupportBundle{
			ID:        id,
			Status:    apisv1.SupportBundleStatusCollecting,
			CreatedBy: requestedBy,
			CreatedAt: now,
			ExpiresAt: now.Add(m.config.TTL),
		},
	}
	for _, s := range sources {
		b.state.Sources = append(b.state.Sources, s.status)
	}
	// Written before the collection starts, so that a restart can tell it was cut short.
	if err := os.Mkdir(b.dir, 0o700); err != nil {
		cancel(nil)
		return nil, fmt.Errorf("failed to create support bundle directory: %w", err)
	}
	if err := writeMetadata(b.dir, &b.state); err != nil {
		cancel(nil)
		_ = os.RemoveAll(b.dir)
		return nil, fmt.Errorf("failed to write support bundle metadata: %w", err)
	}
	m.bundles[id] = b
	m.collecting++
	m.logger.Info("Collecting support bundle", "id", id, "createdBy", requestedBy)
	go func() {
		defer close(b.done)
		defer func() {
			m.mutex.Lock()
			defer m.mutex.Unlock()
			m.collecting--
		}()
		m.collect(ctx, b, requester{username: requestedBy, bundleID: id}, request, since, sources)
	}()
	state := b.snapshot()
	return &state, nil
}

func (m *manager) List() []apisv1.SupportBundle {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	items := make([]apisv1.SupportBundle, 0, len(m.bundles))
	for _, b := range m.bundles {
		items = append(items, b.snapshot())
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func (m *manager) Get(id string) (*apisv1.SupportBundle, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	b, ok := m.bundles[id]
	if !ok {
		return nil, ErrNotFound
	}
	state := b.snapshot()
	return &state, nil
}

func (m *manager) Open(id string) (io.ReadSeekCloser, *apisv1.SupportBundle, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	b, ok := m.bundles[id]
	if !ok {
		return nil, nil, ErrNotFound
	}
	if b.state.Status != apisv1.SupportBundleStatusCollected {
		return nil, nil, ErrNotCollected
	}
	// Opened under the lock, so that a concurrent Delete cannot remove the file in between.
	// Once open, the file stays readable after it is removed.
	f, err := os.Open(filepath.Join(b.dir, bundleFileName))
	if err != nil {
		return nil, nil, err
	}
	state := b.snapshot()
	return f, &state, nil
}

func (m *manager) Delete(id string) error {
	m.mutex.Lock()
	b, ok := m.bundles[id]
	if ok {
		delete(m.bundles, id)
	}
	m.mutex.Unlock()
	if !ok {
		return ErrNotFound
	}
	m.remove(b, errDeleted)
	return nil
}

// remove cancels the collection of a bundle already dropped from the index, and removes its files
// once the collection has stopped writing them.
func (m *manager) remove(b *bundle, cause error) {
	b.cancel(cause)
	go func() {
		<-b.done
		if err := os.RemoveAll(b.dir); err != nil {
			m.logger.Error(err, "Failed to remove support bundle", "id", b.state.ID)
			return
		}
		m.budget.release(b.workBytes.Load() + b.tarBytes.Load())
	}()
}

func (m *manager) Run(stopCh <-chan struct{}) {
	// A collection cut short by a graceful shutdown is recorded the same way as one cut short by a
	// crash.
	defer m.cancel(errors.New(errInterrupted))
	ticker := time.NewTicker(gcInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			m.gc()
		}
	}
}

func (m *manager) gc() {
	now := time.Now()
	var expired []*bundle
	m.mutex.Lock()
	for id, b := range m.bundles {
		if !now.Before(b.state.ExpiresAt) {
			delete(m.bundles, id)
			expired = append(expired, b)
		}
	}
	m.mutex.Unlock()
	for _, b := range expired {
		m.logger.Info("Support bundle expired", "id", b.state.ID)
		m.remove(b, errExpired)
	}
}
