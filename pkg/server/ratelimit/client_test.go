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
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientKeyIP(t *testing.T) {
	testCases := []struct {
		name       string
		headers    map[string][]string
		remoteAddr string
		expectedIP string
	}{
		{
			name:       "no headers, remote addr is ip",
			remoteAddr: "192.168.1.1",
			expectedIP: "192.168.1.1",
		},
		{
			name:       "no headers, remote addr is ip+port",
			remoteAddr: "192.168.1.1:32167",
			expectedIP: "192.168.1.1",
		},
		{
			name: "real IP header",
			headers: map[string][]string{
				"X-Real-IP": {"192.168.1.1"},
			},
			remoteAddr: "10.0.1.1",
			expectedIP: "192.168.1.1",
		},
		{
			name: "xff one private",
			headers: map[string][]string{
				"X-Forwarded-For": {"192.168.1.1"},
			},
			remoteAddr: "10.0.1.1",
			expectedIP: "10.0.1.1",
		},
		{
			name: "xff one public",
			headers: map[string][]string{
				"X-Forwarded-For": {"8.8.8.8"},
			},
			remoteAddr: "10.0.1.1",
			expectedIP: "8.8.8.8",
		},
		{
			name: "xff list",
			headers: map[string][]string{
				"X-Forwarded-For": {"10.10.0.1", "8.8.8.8", "10.0.10.10", "192.168.2.1"},
				"X-Real-IP":       {"192.168.1.1"},
			},
			remoteAddr: "10.0.1.1",
			expectedIP: "8.8.8.8",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := &http.Request{
				RemoteAddr: tc.remoteAddr,
				Header:     make(http.Header),
			}
			// cannot use the tc.headers map directly, as the keys should be in canonical form
			for key, values := range tc.headers {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}
			clientIP := ClientKeyIP(req)
			assert.Equal(t, tc.expectedIP, clientIP)
		})
	}
}

// testAllow sends a request from clientIP to rl, at the current time. In a synctest bubble,
// time.Now follows the fake clock, which advances when every goroutine is durably blocked.
func testAllow(rl *ClientRateLimiter, clientIP string) bool {
	ok, _ := testAllowWithDelay(rl, clientIP)
	return ok
}

func testAllowWithDelay(rl *ClientRateLimiter, clientIP string) (bool, time.Duration) {
	req := &http.Request{
		RemoteAddr: clientIP,
		Header:     make(http.Header),
	}
	return rl.Allow(time.Now(), req)
}

func TestClientRateLimiterRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewClientRateLimiter("3/h", 1, 10, ClientKeyIP)
		require.NoError(t, err)
		const client1, client2 = "10.0.0.1:1234", "10.0.0.2:1234"

		ok, delay := testAllowWithDelay(rl, client1)
		assert.True(t, ok)
		assert.Zero(t, delay)
		// 1 token every 20 minutes
		ok, delay = testAllowWithDelay(rl, client1)
		assert.False(t, ok)
		assert.Equal(t, 20*time.Minute, delay)
		// the delay is tracked per client
		ok, _ = testAllowWithDelay(rl, client2)
		assert.True(t, ok)

		time.Sleep(15 * time.Minute)
		ok, delay = testAllowWithDelay(rl, client1)
		assert.False(t, ok)
		assert.Equal(t, 5*time.Minute, delay)

		// a rejected request does not consume a token, so waiting for the hint is enough
		time.Sleep(delay)
		ok, _ = testAllowWithDelay(rl, client1)
		assert.True(t, ok)
	})
}

func TestClientRateLimiterZeroRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewClientRateLimiter("0/s", 1, 10, ClientKeyIP)
		require.NoError(t, err)
		ok, _ := testAllowWithDelay(rl, "10.0.0.1:1234")
		assert.True(t, ok)
		// the limiter never refills: rejected, with no meaningful delay
		ok, delay := testAllowWithDelay(rl, "10.0.0.1:1234")
		assert.False(t, ok)
		assert.Zero(t, delay)
	})
}

func TestClientRateLimiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cacheSize = 10
		rl := NewClientRateLimiterOrDie("3/h", 2, cacheSize, ClientKeyIP)

		const client1 = "192.168.1.1"
		const client2 = "192.168.1.2"

		assert.True(t, testAllow(rl, client1))
		assert.True(t, testAllow(rl, client1))
		// we have exceeded burst size for client1
		assert.False(t, testAllow(rl, client1))
		// client2 can send requests
		assert.True(t, testAllow(rl, client2))

		// we should get about 1 token every 20 minutes, so client1 should still fail after 15 minutes...
		time.Sleep(15 * time.Minute)
		assert.False(t, testAllow(rl, client1))
		// ... but not client2
		assert.True(t, testAllow(rl, client2))
		// ... except once we exceed the burst size
		assert.False(t, testAllow(rl, client2))

		// ... but should succeed after 25 minutes
		time.Sleep(10 * time.Minute)
		assert.True(t, testAllow(rl, client1))
		assert.True(t, testAllow(rl, client2))
	})
}

func TestClientRateLimiterMaxCacheSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cacheSize = 1
		rl := NewClientRateLimiterOrDie("3/h", 1, cacheSize, ClientKeyIP)

		const client1 = "192.168.1.1"
		const client2 = "192.168.1.2"

		assert.True(t, testAllow(rl, client1))
		assert.False(t, testAllow(rl, client1))
		assert.True(t, testAllow(rl, client2))
		// the cache size is 1, so client1 was previously evicted by client2
		assert.True(t, testAllow(rl, client1))
	})
}

func TestClientRateLimiterConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const cacheSize = 10
		const numClients = 10
		const numRequestsPerClient = 100
		const burstSize = 100
		rl := NewClientRateLimiterOrDie("1/s", burstSize, cacheSize, ClientKeyIP)
		resultsCh := make(chan bool, numClients*numRequestsPerClient)

		clientIP := func(i int) string {
			return fmt.Sprintf("192.168.1.%d", i+1)
		}

		var wg sync.WaitGroup
		for i := range numClients {
			ip := clientIP(i)
			for range numRequestsPerClient {
				wg.Go(func() {
					resultsCh <- testAllow(rl, ip)
				})
			}
		}
		wg.Wait()
		close(resultsCh)

		for result := range resultsCh {
			assert.True(t, result)
		}

		for i := range numClients {
			assert.False(t, testAllow(rl, clientIP(i)))
		}
	})
}
