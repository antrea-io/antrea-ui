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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestGlobalRateLimiter(t *testing.T) {
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl, err := NewGlobalRateLimiter("3/h", 2)
	require.NoError(t, err)

	testAllow := func() bool {
		// we can use nil as the global limiter does not need an actual request
		return rl.Allow(clock.Now(), nil)
	}

	assert.True(t, testAllow())
	assert.True(t, testAllow())
	// we have exceeded burst size
	assert.False(t, testAllow())
	// we should get about 1 token every 20 minutes, so should still fail after 15 minutes...
	clock.SetTime(start.Add(15 * time.Minute))
	assert.False(t, testAllow())
	// ... but should succeed after 25 minutes
	clock.SetTime(start.Add(25 * time.Minute))
	assert.True(t, testAllow())
}
