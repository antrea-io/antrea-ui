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

package metricstap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr/testr"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMinScrapeInterval = 5 * time.Second

type recordingObserver struct {
	mutex    sync.Mutex
	outcomes []string
	open     int
}

func (o *recordingObserver) ObserveScrape(targetGroup, outcome string) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.outcomes = append(o.outcomes, targetGroup+":"+outcome)
}

func (o *recordingObserver) TapOpened() {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.open++
}

func (o *recordingObserver) TapClosed() {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	o.open--
}

func (o *recordingObserver) scrapeOutcomes() []string {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	return append([]string(nil), o.outcomes...)
}

func (o *recordingObserver) openTaps() int {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	return o.open
}

var testAgentTarget = target{id: "antrea-agent/node-1", targetGroup: TargetGroupAgent, instance: "node-1"}

func TestCoalescerSharesConcurrentScrapes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		targetGroup := newFakeTargetGroup(TargetGroupAgent, true, func(context.Context, string) ([]*dto.MetricFamily, error) {
			<-release
			return gaugeFamilies(1, "a_metric"), nil
		})
		observer := &recordingObserver{}
		c := newCoalescer(testr.New(t), testMinScrapeInterval, observer)

		const callers = 5
		results := make(chan *scrapeResult, callers)
		for range callers {
			go func() {
				result, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
				assert.NoError(t, err)
				results <- result
			}()
		}
		// Every caller is now waiting on the one scrape in flight.
		synctest.Wait()
		assert.Equal(t, 1, targetGroup.scrapeCount("node-1"))
		close(release)

		first := <-results
		require.NoError(t, first.err)
		assert.Contains(t, first.byName, "a_metric")
		for range callers - 1 {
			assert.Same(t, first, <-results)
		}
		assert.Equal(t, 1, targetGroup.scrapeCount("node-1"))
		assert.Equal(t, []string{"antrea-agent:success"}, observer.scrapeOutcomes())
	})
}

// The caller which happens to start a scrape is not special: when it goes away, the scrape goes on
// for the others.
func TestCoalescerCanceledCallerDoesNotFailOthers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var scrapeCtxErr error
		targetGroup := newFakeTargetGroup(TargetGroupAgent, true, func(ctx context.Context, _ string) ([]*dto.MetricFamily, error) {
			<-release
			scrapeCtxErr = ctx.Err()
			return gaugeFamilies(1, "a_metric"), nil
		})
		c := newCoalescer(testr.New(t), testMinScrapeInterval, nil)

		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		leaderErr := make(chan error, 1)
		go func() {
			_, err := c.scrape(leaderCtx, targetGroup, testAgentTarget)
			leaderErr <- err
		}()
		// The first caller is the one whose call runs the scrape.
		synctest.Wait()
		followerResult := make(chan *scrapeResult, 1)
		go func() {
			result, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
			assert.NoError(t, err)
			followerResult <- result
		}()
		synctest.Wait()

		cancelLeader()
		assert.ErrorIs(t, <-leaderErr, context.Canceled)

		close(release)
		result := <-followerResult
		require.NoError(t, result.err)
		assert.NoError(t, scrapeCtxErr, "the scrape must not be canceled along with the caller which started it")
		assert.Equal(t, 1, targetGroup.scrapeCount("node-1"))
	})
}

func TestCoalescerReusesResultWhileFresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		targetGroup := newFakeTargetGroup(TargetGroupAgent, true, func(context.Context, string) ([]*dto.MetricFamily, error) {
			return gaugeFamilies(1, "a_metric"), nil
		})
		c := newCoalescer(testr.New(t), testMinScrapeInterval, nil)
		freshness := testMinScrapeInterval / 2

		first, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.Equal(t, time.Now(), first.timestamp)

		time.Sleep(freshness - time.Nanosecond)
		reused, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.Same(t, first, reused)
		assert.Equal(t, 1, targetGroup.scrapeCount("node-1"))

		// The window is counted from the scrape, not from the last use.
		time.Sleep(time.Nanosecond)
		second, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.NotSame(t, first, second)
		assert.Equal(t, freshness, second.timestamp.Sub(first.timestamp))
		assert.Equal(t, 2, targetGroup.scrapeCount("node-1"))

		// Another target has its own result.
		other := target{id: "antrea-agent/node-2", targetGroup: TargetGroupAgent, instance: "node-2"}
		_, err = c.scrape(t.Context(), targetGroup, other)
		require.NoError(t, err)
		assert.Equal(t, 1, targetGroup.scrapeCount("node-2"))

		// Results which are no longer fresh are not kept, whether or not another scrape
		// follows.
		c.mutex.Lock()
		assert.Len(t, c.results, 2)
		c.mutex.Unlock()
		time.Sleep(freshness)
		synctest.Wait()
		c.mutex.Lock()
		defer c.mutex.Unlock()
		assert.Empty(t, c.results)
	})
}

// A target which is down must not cost more than one which is up.
func TestCoalescerSharesFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scrapeErr := newError(ErrorCodeUnreachable, "failed to connect to the target", errors.New("dial tcp 192.0.2.10:10350: connection refused"))
		targetGroup := newFakeTargetGroup(TargetGroupAgent, true, func(context.Context, string) ([]*dto.MetricFamily, error) {
			return nil, scrapeErr
		})
		observer := &recordingObserver{}
		c := newCoalescer(testr.New(t), testMinScrapeInterval, observer)

		first, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.Same(t, scrapeErr, first.err)
		second, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.Same(t, first, second)
		assert.Equal(t, 1, targetGroup.scrapeCount("node-1"))
		assert.Equal(t, []string{"antrea-agent:unreachable"}, observer.scrapeOutcomes())
	})
}

func TestCoalescerBoundsScrape(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		targetGroup := newFakeTargetGroup(TargetGroupAgent, true, func(ctx context.Context, _ string) ([]*dto.MetricFamily, error) {
			<-ctx.Done()
			return nil, newError(ErrorCodeTimeout, "the scrape timed out", ctx.Err())
		})
		c := newCoalescer(testr.New(t), testMinScrapeInterval, nil)

		start := time.Now()
		result, err := c.scrape(t.Context(), targetGroup, testAgentTarget)
		require.NoError(t, err)
		assert.Equal(t, ErrorCodeTimeout, ErrorCode(result.err))
		assert.Equal(t, maxScrapeTimeout, time.Since(start))
	})
}
