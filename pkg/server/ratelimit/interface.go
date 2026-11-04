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

// Interface defines the interface for an HTTP request rate-limiter.
type Interface interface {
	// Allow returns true if and only if the current request should be allowed to go through.
	// retryAfter is never negative. It is 0 if and only if the request is allowed, or if
	// waiting cannot help (e.g. the limiter never refills). Otherwise it is a positive hint
	// for how long the client should wait before retrying, computed in the same call as the
	// decision.
	Allow(t time.Time, req *http.Request) (ok bool, retryAfter time.Duration)
}

// allow consumes a token from the limiter if one is available at time t. Otherwise it returns
// the time until the next token becomes available. A rejected request never changes the state of
// the limiter, so that rejected requests cannot drain it.
func allow(rl *rate.Limiter, t time.Time) (bool, time.Duration) {
	if rl.AllowN(t, 1) {
		return true, 0
	}
	limit := rl.Limit()
	if limit <= 0 {
		// The limiter never refills, so there is no meaningful delay.
		return false, 0
	}
	delay := time.Duration((1 - rl.TokensAt(t)) / float64(limit) * float64(time.Second))
	// TokensAt is read after AllowN: a request allowed in between, at a later time, can leave
	// enough tokens to make the delay 0 or negative. The request was still rejected, so return
	// the smallest positive delay, which the middleware rounds up to 1s.
	return false, max(delay, time.Nanosecond)
}
