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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// Every bundle directory holds a metadata file next to the tarball, so that bundles survive a
// restart of the backend container (the emptyDir holding them only goes away with the Pod). It is
// small enough not to be charged to the budget.
const (
	metadataFileName      = "bundle.json"
	metadataFormatVersion = 1
)

const errInterrupted = "interrupted by a restart of antrea-ui"

type bundleMetadata struct {
	FormatVersion int                  `json:"formatVersion"`
	Bundle        apisv1.SupportBundle `json:"bundle"`
}

// writeMetadata writes the metadata file of the bundle in dir, atomically: a crash leaves either
// the previous version or the new one.
func writeMetadata(dir string, state *apisv1.SupportBundle) error {
	data, err := json.Marshal(&bundleMetadata{FormatVersion: metadataFormatVersion, Bundle: *state})
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, metadataFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, metadataFileName))
}

// removeContent removes the files of a bundle that make it downloadable, keeping its metadata.
func removeContent(dir string) error {
	if err := os.RemoveAll(filepath.Join(dir, workDirName)); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, bundleFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func isBundleID(name string) bool {
	id, err := uuid.Parse(name)
	return err == nil && id.String() == name
}

// restore loads the bundles left in the bundle directory by a previous run. Entries not named like
// a bundle are left alone: the directory is configurable, and may not be dedicated to bundles. A
// bundle whose metadata cannot be read is removed; one that was still collecting, or whose tarball
// is gone, is restored as failed.
func (m *manager) restore() error {
	entries, err := os.ReadDir(m.config.Directory)
	if err != nil {
		return fmt.Errorf("failed to read support bundle directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isBundleID(entry.Name()) {
			m.logger.Info("Ignoring unexpected entry in the support bundle directory", "name", entry.Name())
			continue
		}
		dir := filepath.Join(m.config.Directory, entry.Name())
		b, err := m.restoreBundle(dir, entry.Name())
		if err != nil {
			m.logger.Error(err, "Removing support bundle that could not be restored", "id", entry.Name())
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("failed to remove support bundle %s: %w", entry.Name(), err)
			}
			continue
		}
		m.bundles[b.state.ID] = b
		m.logger.Info("Restored support bundle", "id", b.state.ID, "status", b.state.Status)
	}
	return nil
}

func (m *manager) restoreBundle(dir, id string) (*bundle, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := root.ReadFile(metadataFileName)
	if err != nil {
		return nil, err
	}
	var metadata bundleMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("invalid metadata: %w", err)
	}
	if metadata.FormatVersion != metadataFormatVersion {
		return nil, fmt.Errorf("unsupported metadata format version %d", metadata.FormatVersion)
	}
	state := metadata.Bundle
	if state.ID != id {
		return nil, fmt.Errorf("metadata is for bundle %q", state.ID)
	}

	done := make(chan struct{})
	close(done)
	b := &bundle{dir: dir, cancel: func(error) {}, done: done}
	switch state.Status {
	case apisv1.SupportBundleStatusCollecting:
		failBundle(&state, errInterrupted)
	case apisv1.SupportBundleStatusCollected:
		info, err := root.Stat(bundleFileName)
		if err != nil || !info.Mode().IsRegular() || info.Size() != state.Size {
			state.Status = apisv1.SupportBundleStatusFailed
			state.Error = "the bundle file was missing or incomplete after a restart of antrea-ui"
			state.Size = 0
		} else {
			b.tarBytes.Store(info.Size())
			m.budget.charge(info.Size())
		}
	case apisv1.SupportBundleStatusFailed:
	default:
		return nil, fmt.Errorf("unknown status %q", state.Status)
	}

	if state.Status == apisv1.SupportBundleStatusFailed {
		if err := removeContent(dir); err != nil {
			return nil, err
		}
		if state.Status != metadata.Bundle.Status {
			if err := writeMetadata(dir, &state); err != nil {
				return nil, err
			}
		}
	}
	b.state = state
	return b, nil
}

// failBundle marks a bundle as failed with reason, along with the sources it was still collecting:
// those never complete once the bundle has failed.
func failBundle(state *apisv1.SupportBundle, reason string) {
	state.Status = apisv1.SupportBundleStatusFailed
	state.Error = reason
	for i := range state.Sources {
		if state.Sources[i].Status == apisv1.SupportBundleStatusCollecting {
			state.Sources[i].Status = apisv1.SupportBundleStatusFailed
			state.Sources[i].Error = reason
		}
	}
}
