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
	Auth   AuthConfig
	Limits struct {
		MaxLoginsPerSecond   int
		MaxTraceflowsPerHour int
	}
	LogVerbosity int
}

type AuthConfig struct {
	Basic struct {
		JWTKeyPath string
	}
	CookieSecure bool
}

func validateConfig(config *Config) error {
	if config.LogVerbosity < 0 || config.LogVerbosity >= 128 {
		return fmt.Errorf("invalid verbosity level %d: it should be >= 0 and < 128", config.LogVerbosity)
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

	// These are legacy flags, introduced before config file support was added.
	flags.String("jwt-key", "", "Path to PEM private key file to generate JWT tokens; if omitted one will be automatically generated")
	mustBindPFlag("auth.basic.jwtKeyPath", "jwt-key")
	flags.Bool("cookie-secure", false, "Set the Secure attribute for authentication cookie, which requires HTTPS")
	mustBindPFlag("auth.cookieSecure", "cookie-secure")
	flags.Int("max-traceflows-per-hour", DefaultMaxTraceflowsPerHour, "Rate limit the number of Traceflow requests (across all clients); use -1 to remove rate-limiting")
	mustBindPFlag("limits.maxTraceflowsPerHour", "max-traceflows-per-hour")
	flags.Int("max-logins-per-second", DefaultMaxLoginsPerSecond, "Rate limit the number of login attempts (per client IP); use -1 to remove rate-limiting")
	mustBindPFlag("limits.maxLoginsPerSecond", "max-logins-per-second")

	if err := flags.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	// You can set defaults for configuration parameters here
	// v.SetDefault(<key>, <value>)

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
