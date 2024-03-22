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

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const (
	DefaultMaxLoginsPerSecond   = 1
	DefaultMaxTraceflowsPerHour = 100
)

type Config struct {
	Addr   string
	URL    string
	Auth   AuthConfig
	Limits struct {
		MaxLoginsPerSecond   int
		MaxTraceflowsPerHour int
	}
	LogVerbosity    int
	AntreaNamespace string
}

type AuthConfig struct {
	Basic struct {
		Enabled bool
	}
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
	}
	JWTKeyPath   string
	CookieSecure bool
}

func validateConfig(config *Config) error {
	if config.LogVerbosity < 0 || config.LogVerbosity >= 128 {
		return fmt.Errorf("invalid verbosity level %d: it should be >= 0 and < 128", config.LogVerbosity)
	}

	if config.Auth.OIDC.Enabled && config.URL == "" {
		return fmt.Errorf("URL is required when enabling OIDC authentication")
	}

	if !config.Auth.Basic.Enabled && !config.Auth.OIDC.Enabled {
		return fmt.Errorf("at least one of auth.basic.enabled and auth.oidc.enabled must be true")
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

	// You can set defaults for configuration parameters here
	v.SetDefault("limits.maxLoginsPerSecond", DefaultMaxLoginsPerSecond)
	v.SetDefault("limits.maxTraceflowsPerHour", DefaultMaxTraceflowsPerHour)
	v.SetDefault("auth.cookieSecure", true)
	v.SetDefault("auth.basic.enabled", true)
	v.SetDefault("auth.oidc.enabled", false)
	v.SetDefault("antreaNamespace", "kube-system")

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
