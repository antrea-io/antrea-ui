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

package hasher

import (
	"golang.org/x/crypto/argon2"
)

type Argon2id struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
}

func NewArgon2id() *Argon2id {
	return &Argon2id{
		time:    2,
		memory:  19 * 1024,
		threads: 1,
		keyLen:  32,
	}
}

func (h *Argon2id) Hash(password []byte, salt []byte) ([]byte, error) {
	hash := argon2.IDKey(password, salt, h.time, h.memory, h.threads, h.keyLen)
	return hash, nil
}
