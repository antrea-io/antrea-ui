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
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

// GlobalRateLimiter applies the same rate limit to all HTTP requests, regardless of the client.
type GlobalRateLimiter struct {
	rl *rate.Limiter
}

func NewGlobalRateLimiter(rateStr string, burstSize int) (*GlobalRateLimiter, error) {
	config, err := newConfig(rateStr, burstSize)
	if err != nil {
		return nil, err
	}
	return &GlobalRateLimiter{
		rl: rate.NewLimiter(rate.Limit(config.ratePerSecond), config.burstSize),
	}, nil
}

func NewGlobalRateLimiterOrDie(rate string, burstSize int) *GlobalRateLimiter {
	l, err := NewGlobalRateLimiter(rate, burstSize)
	if err != nil {
		panic(err)
	}
	return l
}

func (l *GlobalRateLimiter) Allow(t time.Time, req *http.Request) bool {
	return l.rl.AllowN(t, 1)
}
