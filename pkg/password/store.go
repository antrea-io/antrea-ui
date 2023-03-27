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

package password

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"

	"golang.org/x/exp/slices"

	"antrea.io/antrea-ui/pkg/password/hasher"
	"antrea.io/antrea-ui/pkg/password/readwriter"
)

const (
	defaultPassword = "admin"
	saltLength      = 16
)

var (
	NotInitializedErr  = fmt.Errorf("not initialized")
	InvalidPasswordErr = fmt.Errorf("invalid password")
)

type store struct {
	sync.RWMutex
	cachedSalt []byte
	cachedHash []byte
	rw         readwriter.Interface
	hasher     hasher.Interface
}

func NewStore(rw readwriter.Interface, hasher hasher.Interface) *store {
	return &store{
		rw:     rw,
		hasher: hasher,
	}
}

func (s *store) Init(ctx context.Context) error {
	ok, hash, salt, err := s.rw.Read(ctx)
	if err != nil {
		return err
	}
	s.Lock()
	defer s.Unlock()
	if ok {
		s.cachedSalt = salt
		s.cachedHash = hash
		return nil
	}
	salt = make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("error when generation random salt: %w", err)
	}
	hash, err = s.hasher.Hash([]byte(defaultPassword), salt)
	if err != nil {
		return err
	}
	if err := s.rw.Write(ctx, hash, salt); err != nil {
		return err
	}
	s.cachedSalt = salt
	s.cachedHash = hash
	return nil
}

func (s *store) Update(ctx context.Context, password []byte) error {
	s.Lock()
	defer s.Unlock()
	if s.cachedSalt == nil {
		return NotInitializedErr
	}
	hash, err := s.hasher.Hash(password, s.cachedSalt)
	if err != nil {
		return err
	}
	if err := s.rw.Write(ctx, hash, s.cachedSalt); err != nil {
		return err
	}
	s.cachedHash = hash
	return nil
}

func (s *store) Compare(ctx context.Context, password []byte) error {
	s.RLock()
	defer s.RUnlock()
	if s.cachedSalt == nil {
		return NotInitializedErr
	}
	hash, err := s.hasher.Hash(password, s.cachedSalt)
	if err != nil {
		return err
	}
	if !slices.Equal(hash, s.cachedHash) {
		return InvalidPasswordErr
	}
	return nil
}
