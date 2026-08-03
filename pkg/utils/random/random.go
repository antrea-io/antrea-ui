// Copyright 2026 Antrea Authors.
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

// Package random provides helpers to generate cryptographically-secure random values.
package random

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Bytes returns n cryptographically-secure random bytes.
func Bytes(n int) ([]byte, error) {
	r := make([]byte, n)
	if _, err := rand.Read(r); err != nil {
		return nil, fmt.Errorf("error when generating random data: %w", err)
	}
	return r, nil
}

// HexString returns the hex encoding of n cryptographically-secure random bytes, i.e. a string
// of length 2*n.
func HexString(n int) (string, error) {
	r, err := Bytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(r), nil
}
