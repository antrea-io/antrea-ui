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
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

func TestMiddleware(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewGlobalRateLimiter("2/h", 1)
		require.NoError(t, err)
		middleware := Middleware(rl)

		var retryAfter string
		sendRequest := func() int {
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			middleware(c)
			retryAfter = rr.Header().Get("Retry-After")
			return rr.Code
		}

		assert.Equal(t, http.StatusOK, sendRequest())
		assert.Empty(t, retryAfter)
		// we have exceeded burst size of 1
		assert.Equal(t, http.StatusTooManyRequests, sendRequest())
		assert.Equal(t, "1800", retryAfter)
		// we should get about 1 token every 30 minutes, so should still fail after 25 minutes...
		time.Sleep(25 * time.Minute)
		assert.Equal(t, http.StatusTooManyRequests, sendRequest())
		assert.Equal(t, "300", retryAfter)
		// ... but should succeed after 35 minutes
		time.Sleep(10 * time.Minute)
		assert.Equal(t, http.StatusOK, sendRequest())
	})
}

func TestMiddlewareRetryAfterRounding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 1 token every 400ms: the delay is rounded up to 1 second, never 0
		rl, err := NewGlobalRateLimiter("150/m", 1)
		require.NoError(t, err)
		middleware := Middleware(rl)
		send := func() *httptest.ResponseRecorder {
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			middleware(c)
			return rr
		}
		assert.Equal(t, http.StatusOK, send().Code)
		rr := send()
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
		assert.Equal(t, "1", rr.Header().Get("Retry-After"))
	})
}

func TestMiddlewareZeroRateOmitsRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rl, err := NewGlobalRateLimiter("0/s", 1)
		require.NoError(t, err)
		middleware := Middleware(rl)
		send := func() *httptest.ResponseRecorder {
			rr := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rr)
			middleware(c)
			return rr
		}
		assert.Equal(t, http.StatusOK, send().Code)
		rr := send()
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
		_, present := rr.Header()["Retry-After"]
		assert.False(t, present)
	})
}
