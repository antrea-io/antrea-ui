package auth

import (
	"time"
)

type Token struct {
	Raw       string
	ExpiresIn time.Duration
	ExpiresAt time.Time
}

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go

type TokenManager interface {
	GetToken() (*Token, error)
	VerifyToken(rawToken string) error
	GetRefreshToken() (*Token, error)
	VerifyRefreshToken(rawToken string) error
	DeleteRefreshToken(rawToken string)
}
