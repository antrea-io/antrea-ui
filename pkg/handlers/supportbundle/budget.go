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

package supportbundle

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
)

// errBudgetExceeded fails the whole bundle: it means the disk is (by configuration) full, not
// that one source misbehaved.
var errBudgetExceeded = errors.New("support bundle storage budget (supportBundle.maxTotalBytes) exceeded")

// budget is the byte budget shared by every bundle on disk.
type budget struct {
	max  int64
	used atomic.Int64
}

func (b *budget) reserve(n int64) bool {
	for {
		used := b.used.Load()
		if used+n > b.max {
			return false
		}
		if b.used.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

// charge accounts for n bytes already on disk, even past the maximum: a restored bundle is kept even
// if the budget shrank across the restart, and new bundles are refused until there is room again.
func (b *budget) charge(n int64) {
	b.used.Add(n)
}

func (b *budget) release(n int64) {
	b.used.Add(-n)
}

// chargedFile is a file whose every write is charged to the shared budget and to a per-bundle
// counter, so that the bytes can be released when the file (or the whole bundle) is removed.
type chargedFile struct {
	f       *os.File
	budget  *budget
	counter *atomic.Int64
	n       int64
}

func createChargedFile(path string, b *budget, counter *atomic.Int64) (*chargedFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &chargedFile{f: f, budget: b, counter: counter}, nil
}

func (c *chargedFile) Write(p []byte) (int, error) {
	n := int64(len(p))
	if !c.budget.reserve(n) {
		return 0, errBudgetExceeded
	}
	c.counter.Add(n)
	c.n += n
	return c.f.Write(p)
}

func (c *chargedFile) Close() error {
	return c.f.Close()
}

// discard closes and removes the file, and releases what it was charged.
func (c *chargedFile) discard() {
	c.f.Close()
	if err := os.Remove(c.f.Name()); err != nil && !os.IsNotExist(err) {
		// The bytes stay charged: they are still on disk.
		return
	}
	c.budget.release(c.n)
	c.counter.Add(-c.n)
	c.n = 0
}
