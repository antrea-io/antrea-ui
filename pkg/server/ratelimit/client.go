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

package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// ClientRateLimiter enforces rate-limiting on a per-client basis. A ClientKeyFn function is used to
// map a request to a client "key". ClientKeyIP can be used to map requests to the origin IP (it
// uses a heuristic to determine the IP based on HTTP headers, to account for reverse proxies). To
// avoid unbounded memory usage, the ClientRateLimiter uses a LRU cache with a pre-determined size.
type ClientRateLimiter struct {
	config *config
	cache  *lru.Cache[string, *rate.Limiter]
	keyFn  ClientKeyFn
}

type ClientKeyFn func(*http.Request) string

func NewClientRateLimiter(rateStr string, burstSize int, maxSize int, keyFn ClientKeyFn) (*ClientRateLimiter, error) {
	config, err := newConfig(rateStr, burstSize)
	if err != nil {
		return nil, err
	}
	cache, err := lru.New[string, *rate.Limiter](maxSize)
	if err != nil {
		return nil, fmt.Errorf("error when initializing LRU client cache: %w", err)
	}
	return &ClientRateLimiter{
		config: config,
		cache:  cache,
		keyFn:  keyFn,
	}, nil
}

func NewClientRateLimiterOrDie(rate string, burstSize int, maxSize int, keyFn ClientKeyFn) *ClientRateLimiter {
	l, err := NewClientRateLimiter(rate, burstSize, maxSize, keyFn)
	if err != nil {
		panic(err)
	}
	return l
}

func (l *ClientRateLimiter) Allow(t time.Time, req *http.Request) bool {
	key := l.keyFn(req)
	rl, ok := l.cache.Get(key)
	if !ok {
		rl = rate.NewLimiter(rate.Limit(l.config.ratePerSecond), l.config.burstSize)
		previous, ok, _ := l.cache.PeekOrAdd(key, rl)
		if ok {
			rl = previous
		}
	}
	return rl.AllowN(t, 1)
}

// ClientKeyIP tries to determine the origin client IP for the provided request. It looks at headers
// X-Forwarded-For and X-Real-IP to try to find the correct IP, accounting for reverse proxies. It
// should be robust to attackers spoofing HTTP headers to "hide" their real IP and bypass
// rate-limiting, as long as the last reverse proxy in the network path is a trusted proxy which
// updates X-Forwarded-For correctly (appends the remote address to the existing header) and X-Real-IP..
//
// loosely inspired from https://github.com/ulule/limiter
func ClientKeyIP(req *http.Request) string {
	getIPFromXFFHeader := func() net.IP {
		values := req.Header.Values("X-Forwarded-For")
		if len(values) == 0 {
			return nil
		}
		parts := make([]string, 0)
		for _, v := range values {
			parts = append(parts, strings.Split(v, ",")...)
		}
		for i := len(parts) - 1; i >= 0; i-- {
			part := strings.TrimSpace(parts[i])
			ip := net.ParseIP(part)
			if ip != nil && !ip.IsPrivate() {
				return ip
			}
		}
		return nil
	}

	getIPFromHeader := func(name string) net.IP {
		value := req.Header.Get(name)
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		return net.ParseIP(value)
	}

	// we first look for the rightmost non-public IP address in X-Forwarded-For
	// if none is found, we look for an IP address in X-Real-IP
	// if this fails as well, we use the "real" IP
	// see https://adam-p.ca/blog/2022/03/x-forwarded-for/ for the rationale
	if ip := getIPFromXFFHeader(); ip != nil {
		return ip.String()
	}
	if ip := getIPFromHeader("X-Real-IP"); ip != nil {
		return ip.String()
	}
	remoteAddr := strings.TrimSpace(req.RemoteAddr)
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
