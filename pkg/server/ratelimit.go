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
