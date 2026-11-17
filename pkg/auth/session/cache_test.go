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
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingCache is a keyedCache whose values are their own keys. It records the keys whose values
// the cache released.
type recordingCache struct {
	cache    keyedCache[string]
	released []string
}

func (c *recordingCache) add(t *testing.T, key string) {
	t.Helper()
	value, err := c.cache.getOrBuild(key, func() (string, func(), error) {
		return key, func() { c.released = append(c.released, key) }, nil
	})
	require.NoError(t, err)
	require.Equal(t, key, value)
}

func (c *recordingCache) keys() []string {
	return slices.Sorted(maps.Keys(c.cache.entries))
}

// A versioned key ("<upstream>/<version>") supersedes the older versions of the same upstream, and
// leaves unrelated keys alone. The Antrea Service rebuilds its factory, and its key, whenever the
// Antrea CA bundle rotates; without this the transport built against the previous bundle would sit
// in the session for the rest of its life, unusable, and for a KindCert credential still holding a
// connection pool that only its cleanup closes.
func TestKeyedCacheVersionSupersedesOlderVersions(t *testing.T) {
	var c recordingCache
	c.add(t, "up/1")
	c.add(t, "other/1")
	c.add(t, "unversioned")

	c.add(t, "up/2")
	assert.Equal(t, []string{"up/1"}, c.released, "only the superseded value should be released")
	assert.Equal(t, []string{"other/1", "unversioned", "up/2"}, c.keys())
}

// A request that read generation 1 just before it was replaced can reach the cache after
// generation 2 is cached. It must not release the value of the newer generation.
func TestKeyedCacheKeepsNewerVersionWhenAnOlderOneArrivesLate(t *testing.T) {
	var c recordingCache
	c.add(t, "up/2")
	c.add(t, "up/1")
	assert.Empty(t, c.released, "an older version must not supersede a newer one")

	// The late value does not outlive the next rotation.
	c.add(t, "up/3")
	assert.ElementsMatch(t, []string{"up/1", "up/2"}, c.released)
	assert.Equal(t, []string{"up/3"}, c.keys())
}

// Versions that are not integers cannot be ordered, so any other version of the upstream is
// treated as older.
func TestKeyedCacheSupersedesNonNumericVersions(t *testing.T) {
	var c recordingCache
	c.add(t, "up/b")
	c.add(t, "up/a")
	assert.Equal(t, []string{"up/b"}, c.released)
}

// A value under an unversioned key is never superseded, whatever else is cached.
func TestKeyedCacheUnversionedKeySupersedesNothing(t *testing.T) {
	var c recordingCache
	c.add(t, "up/1")
	c.add(t, "plain")
	assert.Empty(t, c.released)
}
