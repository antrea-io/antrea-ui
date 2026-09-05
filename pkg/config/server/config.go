// Copyright 2023 Antrea Authors.
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
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	DefaultMaxLoginsPerSecond   = 1
	DefaultMaxTraceflowsPerHour = 100

	DefaultSessionIdleTimeout = 30 * time.Minute
	DefaultSessionMaxLifetime = 12 * time.Hour
	DefaultMaxSessions        = 1000
	DefaultMaxSessionsPerUser = 10

	DefaultMaxConfigMapPlugins = 10
	DefaultMaxDirectoryPlugins = 10

	DefaultMaxBundleBytes = 10 * 1024 * 1024 // 10MiB
)

type FlowAggregatorConfig struct {
	Enabled bool
	Address string
	// CAConfigMap is the name of the ConfigMap (in Namespace) containing the CA
	// certificate (key: ca.crt) used to verify the FlowStreamService server cert.
	// When empty, server certificate verification is skipped (dev/test only).
	CAConfigMap string
	// Namespace is the Kubernetes namespace where the Flow Aggregator is installed.
	Namespace string
	// ServerName overrides the TLS server name used for certificate verification.
	// Useful when dialing via kubectl port-forward, where the address is loopback
	// but the server cert is issued for the in-cluster Service DNS name.
	// If empty, the hostname from Address is used.
	ServerName string
	// InsecureSkipVerify disables TLS server certificate verification.
	// This should only be used for development/testing and must never be enabled in production.
	InsecureSkipVerify bool
}

type Config struct {
	Addr           string
	URL            string
	Auth           AuthConfig
	Session        SessionConfig
	FlowAggregator FlowAggregatorConfig
	Limits         struct {
		MaxLoginsPerSecond   int
		MaxTraceflowsPerHour int
	}
	LogVerbosity    int
	AntreaNamespace string
	Plugins         PluginsConfig
}

type PluginsConfig struct {
	// LabelSelector selects the ConfigMaps (in Namespace) that the backend watches for
	// frontend plugins, e.g. "ui.antrea.io/plugin=true".
	LabelSelector string
	// Namespace is the Kubernetes namespace the backend watches for plugin ConfigMaps.
	// Empty means antrea-ui's own namespace (see env.GetNamespace()).
	Namespace string
	// Directory, if set, is a filesystem path the backend also watches for plugin bundles - one
	// subdirectory per plugin, each holding a manifest.json plus a bundle.zip with the files it
	// references, the on-disk mirror of a plugin ConfigMap's Data/BinaryData (see
	// pkg/plugins/disk.go). Meant for local development, and as a fallback if a deployment's
	// plugins outgrow a ConfigMap's 1MiB cap and it mounts a shared volume here instead. Unset
	// (the default) disables directory-based loading entirely; ConfigMap-based loading is
	// unaffected either way.
	Directory string
	// MaxConfigMapPlugins/MaxDirectoryPlugins cap how many plugins each source may register at
	// once. A new (not already-tracked) plugin past the cap is rejected and logged; updates to
	// an already-tracked plugin are never blocked by it. Neither source otherwise bounds a
	// plugin's on-disk footprint (see docs/plugins.md), so this is what actually bounds a
	// deployment's total exposure to a misbehaving or numerous set of plugins. Zero means
	// unbounded.
	MaxConfigMapPlugins int
	MaxDirectoryPlugins int
	// MaxBundleBytes bounds how much a single plugin's bundle.zip may decompress to in total,
	// once extracted to disk - shared by both sources rather than a separate limit each, since a
	// plugin directory carries about as much trust as a plugin ConfigMap - see
	// pkg/plugins/registry.go's extractZip. A ConfigMap's own ~1MiB etcd size limit only bounds
	// the compressed bytes, not what they decompress to, and a plugin directory has no equivalent
	// limit at all, so without this a small, maliciously high-ratio archive ("zip bomb") could
	// still exhaust the backend's disk. Zero means unbounded.
	MaxBundleBytes int64
}

// SessionConfig configures the server-side session store, which holds the Kubernetes credential
// for every logged-in user.
type SessionConfig struct {
	// IdleTimeout is how long a session survives with no request. The frontend pings
	// /auth/session while a tab is visible, so "idle" means no open visible tab.
	IdleTimeout time.Duration
	// MaxLifetime caps a session's lifetime regardless of activity.
	MaxLifetime time.Duration
	// MaxSessions bounds the number of concurrent sessions the store will hold.
	MaxSessions int
	// MaxSessionsPerUser bounds how many of those one identity may hold, so that a single user
	// cannot fill the store and deny logins to everyone else. Logging in past the cap evicts
	// that user's own least-recently-seen session.
	MaxSessionsPerUser int
}

// AuthConfig enables the supported login modes. They are independent: any combination may be
// enabled.
type AuthConfig struct {
	// Basic is the static admin password (mode 4). K8s calls are impersonated as the
	// antrea-ui-admin ServiceAccount, so every mode-4 user has the same access.
	Basic struct {
		Enabled bool
	}
	// OIDC (mode 1) requires the kube-apiserver to trust the same issuer, since the
	// id_token is what antrea-ui presents to it.
	OIDC struct {
		Enabled      bool
		ProviderName string
		ClientID     string
		ClientSecret string
		IssuerURL    string
		// See https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc#InsecureIssuerURLContext
		// In the general case, it is not recommended to use this
		DiscoveryURL string
		LogoutURL    string
		// Scopes requested from the provider. "offline_access" is required to obtain a
		// refresh token; "groups" to populate group-based RBAC.
		Scopes []string
	}
	// Kubeconfig lets a user upload their own kubeconfig (mode 3).
	Kubeconfig struct {
		Enabled bool
	}
	// Token lets a user paste a bearer token (mode 5) on the login page, which
	// creates a session like any other login mode.
	Token struct {
		Enabled bool
	}
	// BearerToken gates the "Authorization: Bearer <k8s-token>" fallback: a non-browser client
	// (a script, a controller, the e2e suite) authenticating each API request with a Kubernetes
	// token instead of holding a session cookie.
	//
	// It is deliberately separate from Token even though both accept the same
	// credential. They are different exposures: the login mode is a page a human uses, while
	// this one is an authentication path on every API route, used by clients that are not
	// browsers and therefore not covered by the cross-origin gate. A deployment that wants the
	// paste-a-token login for its users without leaving a header-authenticated API open (or the
	// reverse - API clients, no token login page) can have either one on its own.
	BearerToken struct {
		Enabled bool
	}
	CookieSecure bool
}

// anyModeEnabled reports whether any *login* mode is enabled, i.e. whether a user can obtain a
// session. BearerToken is not one: it authenticates individual API requests and creates no
// session, so a deployment with only that enabled has a working API and a login page with nothing
// on it, which is a misconfiguration rather than a supported topology.
func (a *AuthConfig) anyModeEnabled() bool {
	return a.Basic.Enabled || a.OIDC.Enabled || a.Kubeconfig.Enabled || a.Token.Enabled
}

func validateConfig(config *Config) error {
	if config.LogVerbosity < 0 || config.LogVerbosity >= 128 {
		return fmt.Errorf("invalid verbosity level %d: it should be >= 0 and < 128", config.LogVerbosity)
	}

	if config.Auth.OIDC.Enabled && config.URL == "" {
		return fmt.Errorf("URL is required when enabling OIDC authentication")
	}

	if !config.Auth.anyModeEnabled() {
		return fmt.Errorf("at least one authentication mode must be enabled (auth.basic, auth.oidc, auth.kubeconfig, auth.token)")
	}

	if config.Session.IdleTimeout <= 0 {
		return fmt.Errorf("session.idleTimeout must be positive")
	}
	if config.Session.MaxLifetime <= 0 {
		return fmt.Errorf("session.maxLifetime must be positive")
	}
	if config.Session.MaxLifetime < config.Session.IdleTimeout {
		return fmt.Errorf("session.maxLifetime must be >= session.idleTimeout")
	}
	if config.Session.MaxSessions <= 0 {
		return fmt.Errorf("session.maxSessions must be positive")
	}
	if config.Session.MaxSessionsPerUser <= 0 {
		return fmt.Errorf("session.maxSessionsPerUser must be positive")
	}
	if config.Session.MaxSessionsPerUser > config.Session.MaxSessions {
		return fmt.Errorf("session.maxSessionsPerUser must be <= session.maxSessions")
	}

	if config.Plugins.MaxConfigMapPlugins < 0 {
		return fmt.Errorf("plugins.maxConfigMapPlugins must be >= 0 (0 means unbounded)")
	}
	if config.Plugins.MaxDirectoryPlugins < 0 {
		return fmt.Errorf("plugins.maxDirectoryPlugins must be >= 0 (0 means unbounded)")
	}
	if config.Plugins.MaxBundleBytes < 0 {
		return fmt.Errorf("plugins.maxBundleBytes must be >= 0 (0 means unbounded)")
	}

	return nil
}

func LoadConfig() (*Config, error) {
	v := viper.New()

	flags := pflag.NewFlagSet("server", pflag.ExitOnError)

	var configPath string
	flags.StringVarP(&configPath, "config", "c", "", "Path to config file")

	// mustBindPFlag panics if binding the flag to the configuration parameter fails: this can
	// only happen because of a bug in the code (invalid flag name).
	mustBindPFlag := func(key string, flag string) {
		if err := v.BindPFlag(key, flags.Lookup(flag)); err != nil {
			panic(fmt.Sprintf("Failed to bind flag '%s' to configuration key '%s'", flag, key))
		}
	}

	flags.IntP("verbosity", "v", 0, "Log verbosity")
	mustBindPFlag("logVerbosity", "verbosity")

	flags.String("addr", ":8080", "Listening address for server")
	mustBindPFlag("addr", "addr")

	if err := flags.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	// Configuration variables that can be set through environment
	v.MustBindEnv("auth.oidc.clientId", "ANTREA_UI_AUTH_OIDC_CLIENT_ID")
	v.MustBindEnv("auth.oidc.clientSecret", "ANTREA_UI_AUTH_OIDC_CLIENT_SECRET")
	// A path, not a secret, but still machine-specific rather than something to check into a
	// shared config file - env var is the convenient way to point a local dev server at it.
	v.MustBindEnv("plugins.directory", "ANTREA_UI_PLUGINS_DIRECTORY")

	// You can set defaults for configuration parameters here
	v.SetDefault("limits.maxLoginsPerSecond", DefaultMaxLoginsPerSecond)
	v.SetDefault("limits.maxTraceflowsPerHour", DefaultMaxTraceflowsPerHour)
	v.SetDefault("auth.cookieSecure", true)
	v.SetDefault("auth.basic.enabled", true)
	v.SetDefault("auth.oidc.enabled", false)
	// Keep in sync with server.DefaultOIDCScopes and the chart's auth.oidc.scopes. Because a
	// default is always set here, the fallback in NewOIDCProvider never fires, so omitting a
	// scope here omits it for every deployment that does not spell the list out.
	v.SetDefault("auth.oidc.scopes", []string{"openid", "email", "groups", "offline_access"})
	v.SetDefault("auth.kubeconfig.enabled", false)
	v.SetDefault("auth.token.enabled", true)
	v.SetDefault("auth.bearerToken.enabled", true)
	v.SetDefault("session.idleTimeout", DefaultSessionIdleTimeout)
	v.SetDefault("session.maxLifetime", DefaultSessionMaxLifetime)
	v.SetDefault("session.maxSessions", DefaultMaxSessions)
	v.SetDefault("session.maxSessionsPerUser", DefaultMaxSessionsPerUser)
	v.SetDefault("antreaNamespace", "kube-system")
	v.SetDefault("plugins.labelSelector", "ui.antrea.io/plugin=true")
	v.SetDefault("plugins.maxConfigMapPlugins", DefaultMaxConfigMapPlugins)
	v.SetDefault("plugins.maxDirectoryPlugins", DefaultMaxDirectoryPlugins)
	v.SetDefault("plugins.maxBundleBytes", DefaultMaxBundleBytes)
	v.SetDefault("flowAggregator.enabled", false)
	v.SetDefault("flowAggregator.address", "flow-aggregator.flow-aggregator.svc:14740")
	v.SetDefault("flowAggregator.caConfigMap", "flow-aggregator-ca")
	v.SetDefault("flowAggregator.namespace", "flow-aggregator")
	v.SetDefault("flowAggregator.serverName", "")
	v.SetDefault("flowAggregator.insecureSkipVerify", false)

	// By default, look for a file named config (any supported extension) in the working directory.
	v.AddConfigPath(".")
	v.SetConfigName("config")

	if configPath != "" {
		v.SetConfigFile(configPath)
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error when reading config: %w", err)
		}
		// Otherwise, we ignore the error.
	}

	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("error when unmarshalling config: %w", err)
	}

	if err := validateConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}
