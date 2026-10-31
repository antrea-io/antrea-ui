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
	// When the request is rejected, retryAfter is a hint for how long the client should wait
	// before retrying, computed in the same call as the decision. It is 0 when the request is
	// allowed, or when there is no meaningful delay (e.g. the limiter never refills).
	Allow(t time.Time, req *http.Request) (ok bool, retryAfter time.Duration)
}

// reserve consumes a token from the limiter if one is available at time t. Otherwise it returns
// the time until the next token becomes available, without consuming anything.
func reserve(rl *rate.Limiter, t time.Time) (bool, time.Duration) {
	r := rl.ReserveN(t, 1)
	if !r.OK() {
		return false, 0
	}
	delay := r.DelayFrom(t)
	if delay == 0 {
		return true, 0
	}
	// We are not going to wait for the token: give it back.
	r.CancelAt(t)
	if delay == rate.InfDuration {
		// The limiter never refills (rate of 0), so there is no meaningful delay.
		return false, 0
	}
	return false, delay
}
