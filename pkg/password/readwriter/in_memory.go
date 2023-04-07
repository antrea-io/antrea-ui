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

package readwriter

import (
	"context"
	"sync"
)

type InMemory struct {
	sync.Mutex
	set  bool
	hash []byte
	salt []byte
}

func (rw *InMemory) Read(ctx context.Context) (bool, []byte, []byte, error) {
	rw.Lock()
	defer rw.Unlock()
	if !rw.set {
		return false, nil, nil, nil
	}
	return true, rw.hash, rw.salt, nil
}

func (rw *InMemory) Write(ctx context.Context, hash []byte, salt []byte) error {
	rw.Lock()
	defer rw.Unlock()
	rw.hash = hash
	rw.salt = salt
	rw.set = true
	return nil
}

func NewInMemory() *InMemory {
	return &InMemory{}
}
