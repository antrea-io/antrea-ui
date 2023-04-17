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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clocktesting "k8s.io/utils/clock/testing"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

func TestMiddleware(t *testing.T) {
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl, err := NewGlobalRateLimiter("2/h", 1)
	require.NoError(t, err)
	middleware := MiddlewareWithClock(rl, clock)

	sendRequest := func() int {
		rr := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rr)
		middleware(c)
		return rr.Code
	}

	assert.Equal(t, http.StatusOK, sendRequest())
	// we have exceeded burst size of 1
	assert.Equal(t, http.StatusTooManyRequests, sendRequest())
	// we should get about 1 token every 30 minutes, so should still fail after 25 minutes...
	clock.SetTime(start.Add(25 * time.Minute))
	assert.Equal(t, http.StatusTooManyRequests, sendRequest())
	// ... but should succeed after 35 minutes
	clock.SetTime(start.Add(35 * time.Minute))
	assert.Equal(t, http.StatusOK, sendRequest())
}
