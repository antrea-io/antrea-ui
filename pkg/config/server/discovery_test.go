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

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFlowDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		enabled, discovery                bool
		namespace, address, ca, wantError string
	}{
		{name: "disabled ignores endpoint", discovery: true},
		{name: "discovery needs no namespace or address", enabled: true, discovery: true, ca: "fa-ca"},
		{name: "discovery ignores static namespace", enabled: true, discovery: true, namespace: "ignored/namespace", ca: "fa-ca"},
		{name: "discovery uses system trust", enabled: true, discovery: true},
		{name: "static requires address", enabled: true, wantError: "address must not be empty"},
		{name: "static CA requires namespace", enabled: true, address: "fa.example:14740", ca: "fa-ca", wantError: "namespace must be a valid Kubernetes namespace name"},
		{name: "static rejects invalid namespace", enabled: true, address: "fa.example:14740", namespace: "bad/name", ca: "fa-ca", wantError: "namespace must be a valid Kubernetes namespace name"},
		{name: "static with CA", enabled: true, address: "fa.example:14740", namespace: "networking", ca: "fa-ca"},
		{name: "static system trust needs no namespace", enabled: true, address: "fa.example:14740"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := validConfig()
			config.FlowAggregator.Enabled = tc.enabled
			config.FlowAggregator.Discovery.Enabled = tc.discovery
			config.FlowAggregator.Namespace = tc.namespace
			config.FlowAggregator.Address = tc.address
			config.FlowAggregator.CAConfigMap = tc.ca
			err := validateConfig(config)
			if tc.wantError != "" {
				assert.ErrorContains(t, err, tc.wantError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestFlowDiscoveryIsOptIn(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit discovery %t", enabled), func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			endpoint := map[string]any{"enabled": true, "address": "127.0.0.1:24740"}
			if enabled {
				endpoint["discovery"] = map[string]any{"enabled": true}
			}
			data, err := json.Marshal(map[string]any{"flowAggregator": endpoint})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(configPath, data, 0600))
			os.Args = []string{"server", "--config", configPath}
			config, err := LoadConfig()
			require.NoError(t, err)
			require.Equal(t, enabled, config.FlowAggregator.Discovery.Enabled)
			require.Equal(t, "127.0.0.1:24740", config.FlowAggregator.Address)
		})
	}
}
