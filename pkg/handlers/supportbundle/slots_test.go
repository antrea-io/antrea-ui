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
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A waiter reads the limit again each time a slot is released, so a lowered limit applies to it.
func TestSourceSlotsWaiterGetsLoweredLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slots := newSourceSlots()
		var limit atomic.Int32
		limit.Store(2)
		limitFunc := func() int { return int(limit.Load()) }
		releaseFirst, err := slots.acquire(t.Context(), "plugin/foo", limitFunc, nil)
		require.NoError(t, err)
		releaseSecond, err := slots.acquire(t.Context(), "plugin/foo", limitFunc, nil)
		require.NoError(t, err)

		var acquired atomic.Bool
		go func() {
			release, err := slots.acquire(t.Context(), "plugin/foo", limitFunc, nil)
			if err != nil {
				return
			}
			acquired.Store(true)
			release()
		}()
		synctest.Wait()
		assert.False(t, acquired.Load())

		limit.Store(1)
		releaseFirst()
		synctest.Wait()
		assert.False(t, acquired.Load(), "waiter got a slot beyond the lowered limit")

		releaseSecond()
		synctest.Wait()
		assert.True(t, acquired.Load())
		assert.Empty(t, slots.inUse)
	})
}
