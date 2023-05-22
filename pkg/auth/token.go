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

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/clock"
)

const (
	Issuer               = "ui.antrea.io"
	Audience             = "ui.antrea.io"
	tokenLifetime        = 10 * time.Minute
	refreshTokenGCPeriod = 1 * time.Minute
	tokenIDLength        = 20
)

type tokenManager struct {
	signingKeyID       string
	signingKey         *rsa.PrivateKey
	signingMethod      jwt.SigningMethod
	refreshTokensMutex sync.RWMutex
	refreshTokens      map[string]time.Time
	clock              clock.Clock
	jwtParser          *jwt.Parser
}

type JWTAccessClaims struct {
	jwt.RegisteredClaims
}

func newTokenManagerWithClock(keyID string, key *rsa.PrivateKey, clock clock.Clock) *tokenManager {
	signingMethod := jwt.SigningMethodRS512
	return &tokenManager{
		signingKeyID:  keyID,
		signingKey:    key,
		signingMethod: signingMethod,
		refreshTokens: make(map[string]time.Time),
		clock:         clock,
		jwtParser: jwt.NewParser(
			jwt.WithValidMethods([]string{signingMethod.Name}),
			jwt.WithIssuer(Issuer),
			jwt.WithAudience(Audience),
			jwt.WithTimeFunc(clock.Now),
		),
	}
}

func NewTokenManager(keyID string, key *rsa.PrivateKey) *tokenManager {
	return newTokenManagerWithClock(keyID, key, &clock.RealClock{})
}

func (m *tokenManager) Run(stopCh <-chan struct{}) {
	go m.runRefreshTokenGC(stopCh)
	<-stopCh
}

func getTokenID() (string, error) {
	tokenID := make([]byte, tokenIDLength)
	_, err := rand.Read(tokenID)
	if err != nil {
		return "", fmt.Errorf("error when generating random token ID: %w", err)
	}
	return hex.EncodeToString(tokenID), nil
}

func (m *tokenManager) getToken(expiresIn time.Duration, subject string) (*Token, error) {
	createdAt := m.clock.Now()
	expiresAt := createdAt.Add(expiresIn)
	tokenID, err := getTokenID()
	if err != nil {
		return nil, err
	}
	claims := &JWTAccessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{Audience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(createdAt),
			ID:        tokenID,
		},
	}

	token := jwt.NewWithClaims(m.signingMethod, claims)
	if m.signingKeyID != "" {
		token.Header["kid"] = m.signingKeyID
	}

	access, err := token.SignedString(m.signingKey)
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
	return m.getToken(tokenLifetime, "")
}

func (m *tokenManager) GetRefreshToken(lifetime time.Duration, subject string) (*Token, error) {
	token, err := m.getToken(lifetime, subject)
	if err != nil {
		return nil, err
	}
	m.refreshTokensMutex.Lock()
	defer m.refreshTokensMutex.Unlock()
	m.refreshTokens[token.Raw] = token.ExpiresAt
	return token, nil
}

func (m *tokenManager) verifyToken(rawToken string) error {
	_, err := m.jwtParser.Parse(
		rawToken,
		func(token *jwt.Token) (interface{}, error) {
			return &m.signingKey.PublicKey, nil
		},
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

	// we delete the tokens by batches to avoid holding the lock for too long at one time.
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
	//lint:ignore SA1019 apimachinery doesn't provide a correct alternative yet
	wait.BackoffUntil(m.doRefreshTokenGC, wait.NewJitteredBackoffManager(refreshTokenGCPeriod, 0.0, m.clock), true, stopCh)
}
