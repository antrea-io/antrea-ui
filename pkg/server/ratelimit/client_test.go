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
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
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
		tc := tc
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

type testClientRateLimiter struct {
	*ClientRateLimiter
	clock clock.Clock
}

func newTestClientRateLimiter(clock clock.Clock, rateStr string, burstSize int, maxSize int) *testClientRateLimiter {
	return &testClientRateLimiter{
		ClientRateLimiter: NewClientRateLimiterOrDie(rateStr, burstSize, maxSize, ClientKeyIP),
		clock:             clock,
	}
}

func (l *testClientRateLimiter) testAllow(clientIP string) bool {
	req := &http.Request{
		RemoteAddr: clientIP,
		Header:     make(http.Header),
	}
	return l.Allow(l.clock.Now(), req)
}

func TestClientRateLimiter(t *testing.T) {
	const cacheSize = 10
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl := newTestClientRateLimiter(clock, "3/h", 2, cacheSize)

	const client1 = "192.168.1.1"
	const client2 = "192.168.1.2"

	assert.True(t, rl.testAllow(client1))
	assert.True(t, rl.testAllow(client1))
	// we have exceeded burst size for client1
	assert.False(t, rl.testAllow(client1))
	// client2 can send requests
	assert.True(t, rl.testAllow(client2))

	// we should get about 1 token every 20 minutes, so client1 should still fail after 15 minutes...
	clock.SetTime(start.Add(15 * time.Minute))
	assert.False(t, rl.testAllow(client1))
	// ... but not client2
	assert.True(t, rl.testAllow(client2))
	// ... except once we exceed the burst size
	assert.False(t, rl.testAllow(client2))

	// ... but should succeed after 25 minutes
	clock.SetTime(start.Add(25 * time.Minute))
	assert.True(t, rl.testAllow(client1))
	assert.True(t, rl.testAllow(client2))
}

func TestClientRateLimiterMaxCacheSize(t *testing.T) {
	const cacheSize = 1
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl := newTestClientRateLimiter(clock, "3/h", 1, cacheSize)

	const client1 = "192.168.1.1"
	const client2 = "192.168.1.2"

	assert.True(t, rl.testAllow(client1))
	assert.False(t, rl.testAllow(client1))
	assert.True(t, rl.testAllow(client2))
	// the cache size is 1, so client1 was previously evicted by client2
	assert.True(t, rl.testAllow(client1))
}

func TestClientRateLimiterConcurrent(t *testing.T) {
	const cacheSize = 10
	const numClients = 10
	const numRequestsPerClient = 100
	const burstSize = 100
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl := newTestClientRateLimiter(clock, "1/s", burstSize, cacheSize)
	resultsCh := make(chan bool, numClients*numRequestsPerClient)

	clientIP := func(i int) string {
		return fmt.Sprintf("192.168.1.%d", i+1)
	}

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		ip := clientIP(i)
		for j := 0; j < numRequestsPerClient; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resultsCh <- rl.testAllow(ip)
			}()
		}
	}
	wg.Wait()
	close(resultsCh)

	for result := range resultsCh {
		assert.True(t, result)
	}

	for i := 0; i < numClients; i++ {
		assert.False(t, rl.testAllow(clientIP(i)))
	}
}
