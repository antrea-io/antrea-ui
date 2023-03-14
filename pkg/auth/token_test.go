package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

func getTokenManager(t *testing.T, clock clock.Clock) *tokenManager {
	privateKey, err := LoadPrivateKeyFromBytes([]byte(sampleKey))
	require.NoError(t, err, "failed to load key from PEM data")
	return newTokenManagerWithClock("test-key", privateKey, clock)
}

func TestToken(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		now := time.Now()
		m := getTokenManager(t, clocktesting.NewFakeClock(now))
		token, err := m.GetToken()
		require.NoError(t, err, "failed to generate token")
		assert.Equal(t, tokenLifetime, token.ExpiresIn)
		assert.Equal(t, now.Add(tokenLifetime), token.ExpiresAt)
		assert.NoError(t, m.VerifyToken(token.Raw), "failed to validate token")
	})

	t.Run("invalid - expired", func(t *testing.T) {
		now := time.Now()
		clock := clocktesting.NewFakeClock(now)
		m := getTokenManager(t, clock)
		token, err := m.GetToken()
		require.NoError(t, err, "failed to generate token")
		clock.Step(tokenLifetime + 1*time.Second)
		assert.Error(t, m.VerifyToken(token.Raw), "token should have expired")
	})

	t.Run("invalid - garbage", func(t *testing.T) {
		now := time.Now()
		m := getTokenManager(t, clocktesting.NewFakeClock(now))
		assert.Error(t, m.VerifyToken("garbage"))
	})
}

func TestRefreshToken(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		now := time.Now()
		m := getTokenManager(t, clocktesting.NewFakeClock(now))
		token, err := m.GetRefreshToken()
		require.NoError(t, err, "failed to generate token")
		assert.Equal(t, refreshTokenLifetime, token.ExpiresIn)
		assert.Equal(t, now.Add(refreshTokenLifetime), token.ExpiresAt)
		assert.NoError(t, m.VerifyRefreshToken(token.Raw), "failed to validate token")
	})

	t.Run("invalid - expired", func(t *testing.T) {
		now := time.Now()
		clock := clocktesting.NewFakeClock(now)
		m := getTokenManager(t, clock)
		token, err := m.GetRefreshToken()
		require.NoError(t, err, "failed to generate token")
		clock.Step(refreshTokenLifetime + 1*time.Second)
		assert.Error(t, m.VerifyRefreshToken(token.Raw), "token should have expired")
	})

	t.Run("invalid - garbage", func(t *testing.T) {
		now := time.Now()
		m := getTokenManager(t, clocktesting.NewFakeClock(now))
		assert.Error(t, m.VerifyRefreshToken("garbage"))
	})
}

func TestDeleteRefreshToken(t *testing.T) {
	now := time.Now()
	m := getTokenManager(t, clocktesting.NewFakeClock(now))
	token, err := m.GetRefreshToken()
	require.NoError(t, err, "failed to generate token")
	require.NoError(t, m.VerifyRefreshToken(token.Raw), "failed to validate token")
	m.DeleteRefreshToken(token.Raw)
	assert.Error(t, m.VerifyRefreshToken(token.Raw), "token should no longer be valid")
}

func TestRefreshTokenGC(t *testing.T) {
	now := time.Now()
	clock := clocktesting.NewFakeClock(now)
	m := getTokenManager(t, clock)
	stopCh := make(chan struct{})
	defer close(stopCh)
	go m.Run(stopCh)
	token, err := m.GetRefreshToken()
	require.NoError(t, err, "failed to generate token")
	require.NoError(t, m.VerifyRefreshToken(token.Raw), "failed to validate token")
	clock.Step(refreshTokenLifetime + refreshTokenGCPeriod)
	assert.Error(t, m.VerifyRefreshToken(token.Raw))
	assert.Eventually(t, func() bool {
		m.refreshTokensMutex.RLock()
		defer m.refreshTokensMutex.RUnlock()
		_, ok := m.refreshTokens[token.Raw]
		return !ok
	}, 10*time.Second, 100*time.Millisecond, "token should be deleted by GC")
}
