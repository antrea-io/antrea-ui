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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
	"antrea.io/antrea-ui/pkg/version"
)

const (
	// LogFileName is the name of the backend's current log file in LogDirectory.
	LogFileName = "antrea-ui.log"

	backendDirName = "antrea-ui-backend"
)

// BackendOptions configures the collection of antrea-ui's own diagnostics.
type BackendOptions struct {
	// LogDirectory is where the backend writes its log files. Empty means file logging is
	// disabled.
	LogDirectory string
	// Config is the loaded configuration. Secrets are redacted before it is written.
	Config *serverconfig.Config
	// Plugins returns the current plugin manifests.
	Plugins func() []apisv1.PluginManifest
}

type createFileFunc func(name string) (io.WriteCloser, error)

func writeFile(create createFileFunc, name string, write func(w io.Writer) error) error {
	w, err := create(name)
	if err != nil {
		return err
	}
	if err := write(w); err != nil {
		w.Close()
		return fmt.Errorf("failed to write %s: %w", name, err)
	}
	return w.Close()
}

func writeBytes(create createFileFunc, name string, data []byte) error {
	return writeFile(create, name, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// collectBackend writes antrea-ui's own diagnostics under antrea-ui-backend/.
func collectBackend(ctx context.Context, o *BackendOptions, since time.Duration, create createFileFunc) error {
	path := func(name string) string { return filepath.Join(backendDirName, name) }

	if err := writeBytes(create, path("version.txt"), []byte(version.GetFullVersionWithRuntimeInfo()+"\n")); err != nil {
		return err
	}
	if o.Config != nil {
		data, err := yaml.Marshal(o.Config.Redacted())
		if err != nil {
			return fmt.Errorf("failed to marshal config: %w", err)
		}
		if err := writeBytes(create, path("config.yaml"), data); err != nil {
			return err
		}
	}
	plugins := []apisv1.PluginManifest{}
	if o.Plugins != nil {
		plugins = o.Plugins()
	}
	data, err := json.MarshalIndent(plugins, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal plugins: %w", err)
	}
	if err := writeBytes(create, path("plugins.json"), data); err != nil {
		return err
	}
	if err := writeFile(create, path("goroutines.txt"), func(w io.Writer) error {
		return pprof.Lookup("goroutine").WriteTo(w, 2)
	}); err != nil {
		return err
	}
	return collectLogs(ctx, o.LogDirectory, since, func(name string) (io.WriteCloser, error) {
		return create(filepath.Join(backendDirName, "logs", name))
	})
}

// isLogFile tells whether name is the current log file, or one lumberjack rotated it to
// (antrea-ui-<timestamp>.log, compressed or not). The log directory is configurable, and may not be
// dedicated to logs: nothing else in it is collected.
func isLogFile(name string) bool {
	if name == LogFileName {
		return true
	}
	ext := filepath.Ext(LogFileName)
	rotated := strings.TrimSuffix(name, ".gz")
	return strings.HasPrefix(rotated, strings.TrimSuffix(LogFileName, ext)+"-") && strings.HasSuffix(rotated, ext)
}

// collectLogs copies the current log file and the rotated ones. With since set, a rotated file is
// only copied if it was last written within that window.
func collectLogs(ctx context.Context, dir string, since time.Duration, create createFileFunc) error {
	if dir == "" {
		return writeBytes(create, "README.txt", []byte("File logging is disabled or could not be set up (see the backend's stderr): no backend logs were collected.\n"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read log directory: %w", err)
	}
	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.Type().IsRegular() || !isLogFile(entry.Name()) {
			continue
		}
		// The rotated file is still being compressed: the .gz is incomplete, and the file it is
		// compressed from is collected instead. A rotation that lands between ReadDir and Open can
		// still leave its file out.
		if base, ok := strings.CutSuffix(entry.Name(), ".gz"); ok && names[base] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if entry.Name() != LogFileName && info.ModTime().Before(cutoff) {
			continue
		}
		if err := copyLogFile(filepath.Join(dir, entry.Name()), info.Size(), entry.Name(), create); err != nil {
			return err
		}
	}
	return nil
}

func copyLogFile(path string, size int64, name string, create createFileFunc) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Rotated away (and possibly compressed) since the directory was read.
			return nil
		}
		return err
	}
	defer f.Close()
	// Bounded by the size read from the directory: the current file keeps growing while it is
	// copied, with this very collection's own log lines among others.
	return writeFile(create, name, func(w io.Writer) error {
		_, err := io.CopyN(w, f, size)
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	})
}
