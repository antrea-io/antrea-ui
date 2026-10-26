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
	"context"
	"sync"
)

// sourceSlots counts the collections running against each source, across bundles, and bounds them
// for the sources that declare a maxConcurrent. Waiters are not served in order.
type sourceSlots struct {
	mutex sync.Mutex
	inUse map[string]int
	// released is closed, and replaced, whenever a slot is released.
	released chan struct{}
}

func newSourceSlots() *sourceSlots {
	return &sourceSlots{inUse: make(map[string]int), released: make(chan struct{})}
}

// acquire takes one of the slots of the source identified by key, waiting until one is free or ctx
// is done. limit returns how many slots the source has, 0 meaning no limit, but the collection is
// still counted. It is called each time acquire looks for a free slot, so a source whose limit
// changed (e.g. a plugin upgrade, including one that adds a limit) gets the new one, counting the
// collections still running under the old one; a raised limit lets waiters in on the next release.
// waiting, if not nil, is called once, with the limit, if the caller has to wait.
func (s *sourceSlots) acquire(ctx context.Context, key string, limit func() int, waiting func(limit int)) (release func(), err error) {
	for {
		// Called without the lock: it may take other locks (e.g. the plugin registry's).
		l := limit()
		s.mutex.Lock()
		if l <= 0 || s.inUse[key] < l {
			s.inUse[key]++
			s.mutex.Unlock()
			return func() { s.release(key) }, nil
		}
		// Read under the same lock as inUse, so that a release in between cannot be missed.
		released := s.released
		s.mutex.Unlock()
		if waiting != nil {
			waiting(l)
			waiting = nil
		}
		select {
		case <-released:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

func (s *sourceSlots) release(key string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.inUse[key]--
	if s.inUse[key] <= 0 {
		delete(s.inUse, key)
	}
	close(s.released)
	s.released = make(chan struct{})
}
