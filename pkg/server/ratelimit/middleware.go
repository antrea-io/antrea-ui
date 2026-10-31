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
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

func Middleware(rateLimiter Interface) gin.HandlerFunc {
	return func(c *gin.Context) {
		ok, retryAfter := rateLimiter.Allow(time.Now(), c.Request)
		if ok {
			return
		}
		if retryAfter > 0 {
			// Retry-After is expressed in whole seconds (RFC 9110). Round up, with a minimum of 1,
			// so that clients do not retry straight into another 429.
			seconds := max(1, int64((retryAfter+time.Second-1)/time.Second))
			c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		}
		c.AbortWithStatus(http.StatusTooManyRequests)
	}
}
