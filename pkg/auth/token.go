package auth

import (
	"crypto/rsa"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
)

const (
	Issuer               = "ui.antrea.io"
	tokenLifetime        = 10 * time.Minute
	refreshTokenLifetime = 24 * time.Hour
	refreshTokenGCPeriod = 1 * time.Minute
)

type tokenManager struct {
	SigningKeyID       string
	SigningKey         *rsa.PrivateKey
	SigningMethod      jwt.SigningMethod
	refreshTokensMutex sync.RWMutex
	refreshTokens      map[string]time.Time
	clock              clock.Clock
}

type JWTAccessClaims struct {
	jwt.RegisteredClaims
}

func newTokenManagerWithClock(keyID string, key *rsa.PrivateKey, clock clock.Clock) *tokenManager {
	return &tokenManager{
		SigningKeyID:  keyID,
		SigningKey:    key,
		SigningMethod: jwt.SigningMethodRS512,
		refreshTokens: make(map[string]time.Time),
		clock:         clock,
	}
}

func NewTokenManager(keyID string, key *rsa.PrivateKey) *tokenManager {
	return newTokenManagerWithClock(keyID, key, &clock.RealClock{})
}

func (m *tokenManager) Run(stopCh <-chan struct{}) {
	go m.runRefreshTokenGC(stopCh)
	<-stopCh
}

func (m *tokenManager) getToken(expiresIn time.Duration) (*Token, error) {
	createdAt := m.clock.Now()
	expiresAt := createdAt.Add(expiresIn)
	claims := &JWTAccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			Issuer:    Issuer,
			IssuedAt:  jwt.NewNumericDate(createdAt),
		},
	}

	token := jwt.NewWithClaims(m.SigningMethod, claims)
	if m.SigningKeyID != "" {
		token.Header["kid"] = m.SigningKeyID
	}

	access, err := token.SignedString(m.SigningKey)
	if err != nil {
		return nil, err
	}
	return &Token{
		Raw:       access,
		ExpiresIn: expiresIn,
		ExpiresAt: expiresAt,
	}, nil
}

func (m *tokenManager) GetToken() (*Token, error) {
	return m.getToken(tokenLifetime)
}

func (m *tokenManager) GetRefreshToken() (*Token, error) {
	token, err := m.getToken(refreshTokenLifetime)
	if err != nil {
		return nil, err
	}
	m.refreshTokensMutex.Lock()
	defer m.refreshTokensMutex.Unlock()
	m.refreshTokens[token.Raw] = token.ExpiresAt
	return token, nil
}

func (m *tokenManager) verifyToken(rawToken string) error {
	_, err := jwt.Parse(
		rawToken,
		func(token *jwt.Token) (interface{}, error) {
			return &m.SigningKey.PublicKey, nil
		},
		jwt.WithTimeFunc(m.clock.Now),
	)
	if err != nil {
		return err
	}
	return nil
}

func (m *tokenManager) VerifyToken(rawToken string) error {
	return m.verifyToken(rawToken)
}

func (m *tokenManager) VerifyRefreshToken(rawToken string) error {
	if err := m.verifyToken(rawToken); err != nil {
		return err
	}
	m.refreshTokensMutex.RLock()
	defer m.refreshTokensMutex.RUnlock()
	if _, ok := m.refreshTokens[rawToken]; !ok {
		return fmt.Errorf("not a valid refresh token")
	}
	return nil
}

func (m *tokenManager) DeleteRefreshToken(rawToken string) {
	m.refreshTokensMutex.Lock()
	defer m.refreshTokensMutex.Unlock()
	delete(m.refreshTokens, rawToken)
}

func (m *tokenManager) doRefreshTokenGC() {
	expiredTokens := func() []string {
		tokens := make([]string, 0)
		now := m.clock.Now()
		m.refreshTokensMutex.RLock()
		defer m.refreshTokensMutex.RUnlock()
		for token, expiresAt := range m.refreshTokens {
			if expiresAt.Before(now) {
				tokens = append(tokens, token)
			}
		}
		return tokens
	}()

	const batchSize = 100
	idx := 0
	for idx < len(expiredTokens) {
		func() {
			m.refreshTokensMutex.Lock()
			defer m.refreshTokensMutex.Unlock()
			for k := 0; k < batchSize && idx < len(expiredTokens); k++ {
				delete(m.refreshTokens, expiredTokens[idx])
				idx++
			}
		}()
	}
}

func (m *tokenManager) runRefreshTokenGC(stopCh <-chan struct{}) {
	wait.BackoffUntil(m.doRefreshTokenGC, wait.NewJitteredBackoffManager(refreshTokenGCPeriod, 0.0, m.clock), true, stopCh)
}
