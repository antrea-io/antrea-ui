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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlobalRateLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewGlobalRateLimiter("3/h", 2)
		require.NoError(t, err)

		testAllow := func() bool {
			// we can use nil as the global limiter does not need an actual request
			ok, _ := rl.Allow(time.Now(), nil)
			return ok
		}

		assert.True(t, testAllow())
		assert.True(t, testAllow())
		// we have exceeded burst size
		assert.False(t, testAllow())
		// we should get about 1 token every 20 minutes, so should still fail after 15 minutes...
		time.Sleep(15 * time.Minute)
		assert.False(t, testAllow())
		// ... but should succeed after 25 minutes
		time.Sleep(10 * time.Minute)
		assert.True(t, testAllow())
	})
}

func TestGlobalRateLimiterRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewGlobalRateLimiter("3/h", 1)
		require.NoError(t, err)

		ok, delay := rl.Allow(time.Now(), nil)
		assert.True(t, ok)
		assert.Zero(t, delay)
		ok, delay = rl.Allow(time.Now(), nil)
		assert.False(t, ok)
		assert.Equal(t, 20*time.Minute, delay)
		time.Sleep(15 * time.Minute)
		ok, delay = rl.Allow(time.Now(), nil)
		assert.False(t, ok)
		assert.Equal(t, 5*time.Minute, delay)
	})
}

func TestGlobalRateLimiterConcurrentRejections(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewGlobalRateLimiter("1/s", 1)
		require.NoError(t, err)

		ok, _ := rl.Allow(time.Now(), nil)
		require.True(t, ok)

		// Many overlapping rejected requests must not change the state of the limiter.
		const numGoroutines = 64
		const numRequests = 100
		var maxDelay atomic.Int64
		var wg sync.WaitGroup
		for range numGoroutines {
			wg.Go(func() {
				for range numRequests {
					ok, delay := rl.Allow(time.Now(), nil)
					assert.False(t, ok)
					assert.Positive(t, delay)
					for {
						cur := maxDelay.Load()
						if int64(delay) <= cur || maxDelay.CompareAndSwap(cur, int64(delay)) {
							break
						}
					}
				}
			})
		}
		wg.Wait()

		// The hinted delay never exceeds the refill time of a single token...
		assert.LessOrEqual(t, time.Duration(maxDelay.Load()), time.Second)
		// ... and a request is allowed once that delay has passed.
		time.Sleep(time.Second)
		ok, _ = rl.Allow(time.Now(), nil)
		assert.True(t, ok)
	})
}
