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
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/version"
)

// maxParallelSources bounds how many sources one bundle collects from at once.
const maxParallelSources = 4

var (
	errDeleted = errors.New("support bundle deleted")
	errExpired = errors.New("support bundle expired")
)

// bundleManifest is manifest.json, at the root of the tarball.
type bundleManifest struct {
	apisv1.SupportBundle
	AntreaUIVersion string `json:"antreaUIVersion"`
}

func sourceDirName(kind string) string {
	if kind == apisv1.SupportBundleSourceKindPlugin {
		return "plugins"
	}
	return "extra"
}

// collect fills b and records the outcome, unless b was deleted (or expired) in the meantime.
func (m *manager) collect(ctx context.Context, b *bundle, r requester, request *apisv1.SupportBundleRequest, since time.Duration, sources []pendingSource) {
	err := m.doCollect(ctx, b, r, request, since, sources)
	if err != nil {
		// A failed bundle has nothing to download, so its files only hold on to the budget. Its
		// metadata stays, for the bundle to still be listed after a restart.
		if rmErr := removeContent(b.dir); rmErr != nil {
			m.logger.Error(rmErr, "Failed to remove files of failed support bundle", "id", b.state.ID)
		} else {
			m.budget.release(b.workBytes.Swap(0) + b.tarBytes.Swap(0))
		}
	}

	m.mutex.Lock()
	if m.bundles[b.state.ID] != b {
		m.mutex.Unlock()
		m.logger.Info("Support bundle was removed before its collection finished", "id", r.bundleID, "createdBy", r.username)
		return
	}
	if err != nil {
		m.logger.Error(err, "Failed to collect support bundle", "id", r.bundleID, "createdBy", r.username)
		failBundle(&b.state, err.Error())
	} else {
		m.logger.Info("Collected support bundle", "id", r.bundleID, "createdBy", r.username)
		b.state.Status = apisv1.SupportBundleStatusCollected
		b.state.Size = b.tarBytes.Load()
	}
	state := b.snapshot()
	m.mutex.Unlock()
	// A concurrent Delete only removes the directory once this has returned (see remove). If this
	// fails, a restart reports the bundle as interrupted.
	if err := writeMetadata(b.dir, &state); err != nil {
		m.logger.Error(err, "Failed to write support bundle metadata", "id", b.state.ID)
	}
}

func (m *manager) doCollect(ctx context.Context, b *bundle, r requester, request *apisv1.SupportBundleRequest, since time.Duration, sources []pendingSource) error {
	workDir := filepath.Join(b.dir, workDirName)
	// The collection timeout bounds the backend and the sources, not the assembly of what they
	// produced: a source that times out fails on its own, and the bundle is still delivered. The
	// bundle fails if the timeout expires while the backend's own diagnostics are collected.
	collectCtx, cancel := context.WithTimeout(ctx, m.config.CollectionTimeout)
	defer cancel()

	create := func(name string) (io.WriteCloser, error) {
		return createChargedFile(filepath.Join(workDir, name), m.budget, &b.workBytes)
	}
	if err := collectBackend(collectCtx, m.backend, since, create); err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return fmt.Errorf("failed to collect antrea-ui diagnostics: %w", err)
	}

	if request == nil {
		request = &apisv1.SupportBundleRequest{}
	}
	// A source that exceeds the budget fails the bundle, so the other sources are given up on.
	sourcesCtx, cancelSources := context.WithCancelCause(collectCtx)
	defer cancelSources(nil)
	var budgetExceeded atomic.Bool
	var g errgroup.Group
	g.SetLimit(maxParallelSources)
	for i := range sources {
		s := sources[i].source
		if s == nil {
			continue
		}
		g.Go(func() error {
			size, err := m.collectSource(sourcesCtx, b, s, r, request, workDir)
			if errors.Is(err, errBudgetExceeded) {
				budgetExceeded.Store(true)
				cancelSources(errBudgetExceeded)
			}
			m.mutex.Lock()
			defer m.mutex.Unlock()
			status := &b.state.Sources[i]
			if err != nil {
				m.logger.Info("Failed to collect support bundle source", "id", r.bundleID, "createdBy", r.username, "source", s.name(), "kind", s.kind(), "err", err.Error())
				status.Status = apisv1.SupportBundleStatusFailed
				status.Error = err.Error()
				return nil
			}
			m.logger.Info("Collected support bundle source", "id", r.bundleID, "createdBy", r.username, "source", s.name(), "kind", s.kind(), "size", size)
			status.Status = apisv1.SupportBundleStatusCollected
			status.Size = size
			return nil
		})
	}
	_ = g.Wait()

	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if budgetExceeded.Load() {
		return errBudgetExceeded
	}
	return m.assemble(ctx, b, workDir)
}

// collectSource runs the source protocol against s, and stores the tarball it returns as-is: it is
// untrusted, so it is never extracted.
func (m *manager) collectSource(ctx context.Context, b *bundle, s source, r requester, request *apisv1.SupportBundleRequest, workDir string) (int64, error) {
	p := &protocolClient{conn: s.connect(r), maxBytes: m.config.MaxSourceBytes}
	id, wait, err := p.create(ctx, request)
	if err != nil {
		return 0, fmt.Errorf("failed to request a bundle: %w", err)
	}
	logger := m.logger.WithValues("id", r.bundleID, "createdBy", r.username, "source", s.name(), "kind", s.kind(), "remoteID", id)
	logger.V(2).Info("Support bundle source started collecting")
	defer func() {
		if err := p.delete(id); err != nil {
			logger.V(2).Info("Failed to delete bundle from support bundle source", "err", err.Error())
			return
		}
		logger.V(2).Info("Deleted bundle from support bundle source")
	}()

	var lastTransient error
	for {
		select {
		case <-ctx.Done():
			if lastTransient != nil {
				return 0, fmt.Errorf("gave up waiting for the bundle: %w (last status error: %v)", context.Cause(ctx), lastTransient)
			}
			return 0, fmt.Errorf("gave up waiting for the bundle: %w", context.Cause(ctx))
		case <-time.After(wait):
		}
		status, next, err := p.status(ctx, id)
		var transient *transientError
		if errors.As(err, &transient) {
			m.logger.V(2).Info("Retrying support bundle source status", "id", r.bundleID, "source", s.name(), "kind", s.kind(), "err", err.Error())
			lastTransient = err
			wait = transient.retryAfter
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("failed to get the bundle status: %w", err)
		}
		if status.Status == apisv1.SupportBundleStatusFailed {
			return 0, fmt.Errorf("source failed to collect its bundle: %s", sourceMessage(status.Error))
		}
		if status.Status == apisv1.SupportBundleStatusCollected {
			break
		}
		if status.Status != apisv1.SupportBundleStatusCollecting {
			return 0, fmt.Errorf("source returned an unknown bundle status %q", status.Status)
		}
		wait = next
	}

	f, err := createChargedFile(filepath.Join(workDir, sourceDirName(s.kind()), s.name()+".tar.gz"), m.budget, &b.workBytes)
	if err != nil {
		return 0, err
	}
	n, err := p.download(ctx, id, f)
	if err != nil {
		f.discard()
		return 0, fmt.Errorf("failed to download the bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		f.discard()
		return 0, err
	}
	return n, nil
}

// assemble streams the work directory into the bundle tarball, then removes it. Each work file is
// removed (and its bytes released) as soon as it is in the tarball: source tarballs are already
// compressed, so holding on to them until the end would charge most of the bundle twice.
func (m *manager) assemble(ctx context.Context, b *bundle, workDir string) error {
	m.mutex.Lock()
	state := b.snapshot()
	m.mutex.Unlock()
	state.Status = apisv1.SupportBundleStatusCollected
	manifest, err := json.MarshalIndent(bundleManifest{SupportBundle: state, AntreaUIVersion: version.GetFullVersion()}, "", "  ")
	if err != nil {
		return err
	}

	f, err := createChargedFile(filepath.Join(b.dir, bundleFileName), m.budget, &b.tarBytes)
	if err != nil {
		return err
	}
	consumed := func(n int64) {
		b.workBytes.Add(-n)
		m.budget.release(n)
	}
	if err := writeTarball(ctx, f, workDir, manifest, consumed); err != nil {
		f.discard()
		return fmt.Errorf("failed to assemble the bundle: %w", err)
	}
	if err := f.Close(); err != nil {
		f.discard()
		return err
	}
	if err := os.RemoveAll(workDir); err != nil {
		return err
	}
	m.budget.release(b.workBytes.Swap(0))
	return nil
}

// writeTarball writes manifest.json and the contents of workDir to w. Every regular file is
// removed once it is copied, and consumed is called with its size.
func writeTarball(ctx context.Context, w io.Writer, workDir string, manifest []byte, consumed func(n int64)) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "manifest.json",
		Mode:     0o644,
		Size:     int64(len(manifest)),
		ModTime:  now,
	}); err != nil {
		return err
	}
	if _, err := tw.Write(manifest); err != nil {
		return err
	}
	// The work directory only holds files this process wrote, but walking it through an os.Root
	// still guarantees that nothing outside it is ever read into the bundle.
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer root.Close()
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return context.Cause(ctx)
		}
		if name == "." {
			return nil
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: now})
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		src, err := rootFS.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return err
		}
		if err := root.Remove(name); err != nil {
			return err
		}
		consumed(info.Size())
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
