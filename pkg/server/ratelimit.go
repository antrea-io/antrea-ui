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

package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"k8s.io/utils/clock"
)

func rateLimiterWithClock(maxRequestsPerHour int, burstSize int, clock clock.Clock) gin.HandlerFunc {
	if maxRequestsPerHour <= 0 {
		panic("Max requests per hour should be positive for rate limiter")
	}
	if burstSize <= 0 {
		panic("Burst size should be positive for rate limiter")
	}
	rl := rate.NewLimiter(rate.Limit(maxRequestsPerHour)/3600.0, burstSize)
	return func(c *gin.Context) {
		if rl.AllowN(clock.Now(), 1) {
			return
		}
		c.AbortWithStatus(http.StatusTooManyRequests)
	}
}

func rateLimiter(maxRequestsPerHour int, burstSize int) gin.HandlerFunc {
	return rateLimiterWithClock(maxRequestsPerHour, burstSize, &clock.RealClock{})
}
