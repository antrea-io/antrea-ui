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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// validConfig returns a configuration which passes validation, with metrics enabled.
func validConfig() *Config {
	config := &Config{}
	config.Auth.Basic.Enabled = true
	config.Session = SessionConfig{
		IdleTimeout:        DefaultSessionIdleTimeout,
		MaxLifetime:        DefaultSessionMaxLifetime,
		MaxSessions:        DefaultMaxSessions,
		MaxSessionsPerUser: DefaultMaxSessionsPerUser,
	}
	config.Metrics = MetricsConfig{
		Enabled:              true,
		MinScrapeInterval:    DefaultMetricsMinScrapeInterval,
		MaxTaps:              DefaultMetricsMaxTaps,
		MaxTargetsPerTap:     DefaultMetricsMaxTargetsPerTap,
		MaxRequestsPerSecond: DefaultMetricsMaxRequestsPerSecond,
	}
	return config
}

func TestValidateMetricsConfig(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(m *MetricsConfig)
		err    string
	}{
		{name: "defaults", mutate: func(*MetricsConfig) {}},
		{name: "shortest interval", mutate: func(m *MetricsConfig) { m.MinScrapeInterval = time.Second }},
		{name: "longest interval", mutate: func(m *MetricsConfig) { m.MinScrapeInterval = MetricsMaxScrapeInterval }},
		{name: "interval not set", mutate: func(m *MetricsConfig) { m.MinScrapeInterval = 0 }, err: "metrics.minScrapeInterval must be >= 1s"},
		{name: "interval too short", mutate: func(m *MetricsConfig) { m.MinScrapeInterval = 999 * time.Millisecond }, err: "metrics.minScrapeInterval must be >= 1s"},
		{name: "interval too long", mutate: func(m *MetricsConfig) { m.MinScrapeInterval = MetricsMaxScrapeInterval + time.Second }, err: "metrics.minScrapeInterval must be <= 5m0s"},
		{name: "no tap", mutate: func(m *MetricsConfig) { m.MaxTaps = 0 }, err: "metrics.maxTaps must be positive"},
		{name: "no target", mutate: func(m *MetricsConfig) { m.MaxTargetsPerTap = -1 }, err: "metrics.maxTargetsPerTap must be positive"},
		{name: "no rate limit", mutate: func(m *MetricsConfig) { m.MaxRequestsPerSecond = -1 }},
		{name: "no request", mutate: func(m *MetricsConfig) { m.MaxRequestsPerSecond = 0 }, err: "metrics.maxRequestsPerSecond must be positive, or negative to disable the limit"},
		{
			// The limits are not looked at when the feature is off.
			name:   "disabled",
			mutate: func(m *MetricsConfig) { *m = MetricsConfig{} },
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config := validConfig()
			tc.mutate(&config.Metrics)
			err := validateConfig(config)
			if tc.err == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tc.err)
			}
		})
	}
}
