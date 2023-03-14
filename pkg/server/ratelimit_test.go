package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestRateLimiter(t *testing.T) {
	start := time.Now()
	clock := clocktesting.NewFakeClock(start)
	rl := rateLimiterWithClock(3, 2, clock)

	sendRequest := func() int {
		rr := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rr)
		rl(c)
		return rr.Code
	}

	assert.Equal(t, http.StatusOK, sendRequest())
	assert.Equal(t, http.StatusOK, sendRequest())
	// we have exceeded burst size
	assert.Equal(t, http.StatusTooManyRequests, sendRequest())
	// we should get about 1 token every 20 minutes, so should still fail after 15 minutes...
	clock.SetTime(start.Add(15 * time.Minute))
	assert.Equal(t, http.StatusTooManyRequests, sendRequest())
	// ... but should succeed after 25 minutes
	clock.SetTime(start.Add(25 * time.Minute))
	assert.Equal(t, http.StatusOK, sendRequest())
}
