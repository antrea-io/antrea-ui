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

package session

import (
	"strconv"
	"strings"
)

type cachedEntry[T any] struct {
	value   T
	cleanup func()
}

// keyedCache holds the per-upstream clients of one session (transports, gRPC connections), one
// per key, each with the cleanup that releases it. The zero value is an empty cache.
//
// It has no lock of its own: building a value reads the session's credential, so the Session
// guards the cache with its own mutex, and every method must be called with that mutex held (a
// read lock is enough for get).
//
// A key is either a bare name for an upstream whose clients never go stale on their own, or
// "<upstream>/<version>" for one whose settings can change under a live session, such as the CA
// bundle that verifies the server. Building a version releases the older versions of the same
// upstream (see dropSuperseded).
type keyedCache[T any] struct {
	entries map[string]cachedEntry[T]
}

// get returns the value cached under key, if any.
func (c *keyedCache[T]) get(key string) (T, bool) {
	entry, ok := c.entries[key]
	return entry.value, ok
}

// getOrBuild returns the value cached under key, or builds and caches one with build. Before
// building a versioned key, it releases the older versions of the same upstream. Nothing is cached
// if build fails.
func (c *keyedCache[T]) getOrBuild(key string, build func() (T, func(), error)) (T, error) {
	if entry, ok := c.entries[key]; ok {
		return entry.value, nil
	}
	c.dropSuperseded(key)
	value, cleanup, err := build()
	if err != nil {
		var zero T
		return zero, err
	}
	if c.entries == nil {
		c.entries = make(map[string]cachedEntry[T])
	}
	c.entries[key] = cachedEntry[T]{value: value, cleanup: cleanup}
	return value, nil
}

// dropSuperseded releases every value cached for the same upstream as key but an older version of
// it. Without this, a session that outlives a change of an upstream's settings keeps the value it
// built for the previous ones for the rest of its life: unusable, and still holding whatever its
// cleanup releases, such as an idle connection pool or a gRPC connection.
//
// Versions are compared as integers (generations). A request that read an older generation just
// before it was replaced can arrive after a newer value is cached: it must not release that value.
// The value it builds stays cached until the next version, a credential refresh or the end of the
// session releases it. Versions that are not integers cannot be ordered, and every other version of
// the upstream is then treated as older.
func (c *keyedCache[T]) dropSuperseded(key string) {
	upstream, versioned := keyUpstream(key)
	if !versioned {
		return
	}
	for k, entry := range c.entries {
		if k == key {
			continue
		}
		if u, versioned := keyUpstream(k); !versioned || u != upstream {
			continue
		}
		if isNewerVersion(k, key) {
			continue
		}
		if entry.cleanup != nil {
			entry.cleanup()
		}
		delete(c.entries, k)
	}
}

// dropAll releases every cached value.
func (c *keyedCache[T]) dropAll() {
	for k, entry := range c.entries {
		if entry.cleanup != nil {
			entry.cleanup()
		}
		delete(c.entries, k)
	}
}

// keyUpstream is the part of a cache key before the first "/". A key with no "/" names an
// upstream that never supersedes anything, and reports false.
func keyUpstream(key string) (string, bool) {
	upstream, _, versioned := strings.Cut(key, "/")
	return upstream, versioned
}

// isNewerVersion reports whether the version of key a is a larger integer than that of key b.
// It is false if either version is not an integer.
func isNewerVersion(a, b string) bool {
	_, va, _ := strings.Cut(a, "/")
	_, vb, _ := strings.Cut(b, "/")
	na, errA := strconv.Atoi(va)
	nb, errB := strconv.Atoi(vb)
	return errA == nil && errB == nil && na > nb
}
