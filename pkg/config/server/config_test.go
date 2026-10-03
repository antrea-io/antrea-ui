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
	"strings"
	"testing"
	"time"

	"github.com/madflojo/testcerts"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func validConfig() *Config {
	c := &Config{}
	c.Auth.Basic.Enabled = true
	c.Session.IdleTimeout = DefaultSessionIdleTimeout
	c.Session.MaxLifetime = DefaultSessionMaxLifetime
	c.Session.MaxSessions = DefaultMaxSessions
	c.Session.MaxSessionsPerUser = DefaultMaxSessionsPerUser
	c.Log = LogConfig{Directory: DefaultLogDirectory, MaxSizeMB: DefaultLogMaxSizeMB, MaxBackups: DefaultLogMaxBackups}
	c.SupportBundle = SupportBundleConfig{
		Enabled:           true,
		Directory:         DefaultSupportBundleDirectory,
		MaxBundles:        DefaultSupportBundleMaxBundles,
		MaxConcurrent:     DefaultSupportBundleMaxConcurrent,
		MaxTotalBytes:     DefaultSupportBundleMaxTotalBytes,
		MaxSourceBytes:    DefaultSupportBundleMaxSourceBytes,
		TTL:               DefaultSupportBundleTTL,
		CollectionTimeout: DefaultSupportBundleCollectionTimeout,
	}
	return c
}

func TestValidateSupportBundleConfig(t *testing.T) {
	ca, _, err := testcerts.GenerateCerts()
	require.NoError(t, err)

	httpsSource := func(name, url string) SupportBundleExtraSource {
		return SupportBundleExtraSource{Name: name, HTTPS: &SupportBundleHTTPSSource{URL: url}}
	}
	apiServerSource := func(name, path string) SupportBundleExtraSource {
		return SupportBundleExtraSource{Name: name, APIServer: &apisv1.APIServerSourceSpec{Path: path}}
	}

	testCases := []struct {
		name        string
		mutate      func(c *Config)
		expectedErr string
	}{
		{name: "defaults", mutate: func(c *Config) {}},
		{
			name: "disabled skips validation",
			mutate: func(c *Config) {
				c.SupportBundle = SupportBundleConfig{Enabled: false}
			},
		},
		{
			name: "valid extra sources",
			mutate: func(c *Config) {
				foo := httpsSource("foo", "https://foo.ns.svc/api/v1")
				foo.HTTPS.CAData = string(ca)
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{foo, apiServerSource("bar", "/apis/bar.example.com/v1alpha1")}
			},
		},
		{name: "no directory", mutate: func(c *Config) { c.SupportBundle.Directory = "" }, expectedErr: "supportBundle.directory"},
		{name: "zero maxBundles", mutate: func(c *Config) { c.SupportBundle.MaxBundles = 0 }, expectedErr: "supportBundle.maxBundles"},
		{name: "zero maxConcurrent", mutate: func(c *Config) { c.SupportBundle.MaxConcurrent = 0 }, expectedErr: "supportBundle.maxConcurrent"},
		{name: "zero maxTotalBytes", mutate: func(c *Config) { c.SupportBundle.MaxTotalBytes = 0 }, expectedErr: "supportBundle.maxTotalBytes"},
		{name: "zero maxSourceBytes", mutate: func(c *Config) { c.SupportBundle.MaxSourceBytes = 0 }, expectedErr: "supportBundle.maxSourceBytes must be positive"},
		{
			name:        "maxSourceBytes above half of maxTotalBytes",
			mutate:      func(c *Config) { c.SupportBundle.MaxSourceBytes = c.SupportBundle.MaxTotalBytes/2 + 1 },
			expectedErr: "supportBundle.maxSourceBytes must be <= half",
		},
		{
			name:   "maxSourceBytes at half of maxTotalBytes",
			mutate: func(c *Config) { c.SupportBundle.MaxSourceBytes = c.SupportBundle.MaxTotalBytes / 2 },
		},
		{name: "zero ttl", mutate: func(c *Config) { c.SupportBundle.TTL = 0 }, expectedErr: "supportBundle.ttl"},
		{name: "ttl not greater than collection timeout", mutate: func(c *Config) { c.SupportBundle.TTL = c.SupportBundle.CollectionTimeout }, expectedErr: "supportBundle.ttl must be greater than supportBundle.collectionTimeout"},
		{name: "zero collectionTimeout", mutate: func(c *Config) { c.SupportBundle.CollectionTimeout = 0 }, expectedErr: "supportBundle.collectionTimeout"},
		{
			name: "invalid name",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("../foo", "https://foo")}
			},
			expectedErr: "is invalid",
		},
		{
			name: "duplicate name",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("foo", "https://foo"), apiServerSource("foo", "/apis/foo.example.com/v1")}
			},
			expectedErr: "is not unique",
		},
		{
			name: "no variant",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{{Name: "foo"}}
			},
			expectedErr: "exactly one of https and apiServer",
		},
		{
			name: "both variants",
			mutate: func(c *Config) {
				s := httpsSource("foo", "https://foo")
				s.APIServer = &apisv1.APIServerSourceSpec{Path: "/apis/foo.example.com/v1"}
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{s}
			},
			expectedErr: "exactly one of https and apiServer",
		},
		{
			name: "invalid apiServer path",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{apiServerSource("foo", "/api/v1/namespaces")}
			},
			expectedErr: "invalid apiServer.path",
		},
		{
			name: "http URL",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("foo", "http://foo")}
			},
			expectedErr: "absolute https:// URL",
		},
		{
			name: "relative URL",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("foo", "/api/v1")}
			},
			expectedErr: "absolute https:// URL",
		},
		{
			name: "URL with user info",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("foo", "https://user:pass@foo")}
			},
			expectedErr: "must not have user info",
		},
		{
			name: "URL with query",
			mutate: func(c *Config) {
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{httpsSource("foo", "https://foo/?x=y")}
			},
			expectedErr: "must not have user info, a query",
		},
		{
			name: "invalid CA",
			mutate: func(c *Config) {
				s := httpsSource("foo", "https://foo")
				s.HTTPS.CAData = "not a cert"
				c.SupportBundle.ExtraSources = []SupportBundleExtraSource{s}
			},
			expectedErr: "https.caData",
		},
		{name: "log maxSizeMB", mutate: func(c *Config) { c.Log.MaxSizeMB = 0 }, expectedErr: "log.maxSizeMB"},
		{name: "log maxBackups", mutate: func(c *Config) { c.Log.MaxBackups = 0 }, expectedErr: "log.maxBackups"},
		{
			name: "log limits ignored when file logging is disabled",
			mutate: func(c *Config) {
				c.Log = LogConfig{}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			err := validateConfig(c)
			if tc.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.expectedErr)
			}
		})
	}
}

func TestRedacted(t *testing.T) {
	c := validConfig()
	c.Auth.OIDC.ClientSecret = "very-secret"
	r := c.Redacted()
	assert.Equal(t, "<redacted>", r.Auth.OIDC.ClientSecret)
	assert.Equal(t, "very-secret", c.Auth.OIDC.ClientSecret, "the original is left alone")

	c.Auth.OIDC.ClientSecret = ""
	assert.Empty(t, c.Redacted().Auth.OIDC.ClientSecret, "an unset secret stays visibly unset")
}

// The extra source union is decoded by viper from the same camelCase keys the Helm chart renders,
// including nested pointers.
func TestDecodeSupportBundleConfig(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
limits:
  maxSupportBundlesPerHour: 3
log:
  directory: /logs
  maxSizeMB: 20
  maxBackups: 2
supportBundle:
  enabled: true
  directory: /bundles
  maxBundles: 4
  maxConcurrent: 1
  maxTotalBytes: 2048
  maxSourceBytes: 1024
  ttl: 1h
  collectionTimeout: 5m
  extraSources:
    - name: foo
      https:
        url: https://foo.ns.svc/api/v1
        caData: "PEM"
        serverName: foo.example.com
        insecureSkipVerify: true
    - name: bar
      apiServer:
        path: /apis/bar.example.com/v1alpha1
`)))
	var c Config
	require.NoError(t, v.Unmarshal(&c))
	assert.Equal(t, 3, c.Limits.MaxSupportBundlesPerHour)
	assert.Equal(t, LogConfig{Directory: "/logs", MaxSizeMB: 20, MaxBackups: 2}, c.Log)
	assert.Equal(t, SupportBundleConfig{
		Enabled:           true,
		Directory:         "/bundles",
		MaxBundles:        4,
		MaxConcurrent:     1,
		MaxTotalBytes:     2048,
		MaxSourceBytes:    1024,
		TTL:               time.Hour,
		CollectionTimeout: 5 * time.Minute,
		ExtraSources: []SupportBundleExtraSource{
			{Name: "foo", HTTPS: &SupportBundleHTTPSSource{URL: "https://foo.ns.svc/api/v1", CAData: "PEM", ServerName: "foo.example.com", InsecureSkipVerify: true}},
			{Name: "bar", APIServer: &apisv1.APIServerSourceSpec{Path: "/apis/bar.example.com/v1alpha1"}},
		},
	}, c.SupportBundle)
}
