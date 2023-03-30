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

	"github.com/gin-gonic/gin"
	"k8s.io/utils/clock"
)

func Middleware(rateLimiter Interface) gin.HandlerFunc {
	return MiddlewareWithClock(rateLimiter, &clock.RealClock{})
}

func MiddlewareWithClock(rateLimiter Interface, clock clock.Clock) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rateLimiter.Allow(clock.Now(), c.Request) {
			return
		}
		// TODO: consider including the X-Rate-Limit-* headers (and/or Retry-After)
		c.AbortWithStatus(http.StatusTooManyRequests)
	}
}
