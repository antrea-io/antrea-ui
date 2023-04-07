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
	"time"
)

type Token struct {
	Raw       string
	ExpiresIn time.Duration
	ExpiresAt time.Time
}

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type TokenManager interface {
	GetToken() (*Token, error)
	VerifyToken(rawToken string) error
	GetRefreshToken() (*Token, error)
	VerifyRefreshToken(rawToken string) error
	DeleteRefreshToken(rawToken string)
}
