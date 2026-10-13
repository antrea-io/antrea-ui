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
	"fmt"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-logr/logr/testr"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	testOwner    = "session:1/alice"
	testInterval = 10 * time.Second
	// eventTimeout is fake time: it only bounds a test whose tap never sends the event it waits
	// for, which would otherwise run for ever as the tickers of the tap keep the clock going.
	eventTimeout = time.Hour
)

type tapFixture struct {
	manager    Manager
	controller *fakeTargetGroup
	agent      *fakeTargetGroup
	observer   *recordingObserver
}

// newTapFixture creates a manager with a controller target group, an agent target group and a
// reserved Flow Aggregator target group. Scrapes succeed with the given families until a test says
// otherwise.
func newTapFixture(t *testing.T, configure ...func(*ManagerOptions)) *tapFixture {
	f := &tapFixture{observer: &recordingObserver{}}
	scrape := func(context.Context, string) ([]*dto.MetricFamily, error) {
		return gaugeFamilies(1, "metric_a", "metric_b", "metric_c"), nil
	}
	f.controller = newFakeTargetGroup(TargetGroupController, false, scrape)
	f.agent = newFakeTargetGroup(TargetGroupAgent, true, scrape)
	registry := NewRegistry(f.controller, f.agent)
	registry.Reserve(TargetGroupFlowAggregator, flowAggregatorReason)
	options := ManagerOptions{
		MinScrapeInterval: testMinScrapeInterval,
		MaxScrapeInterval: 5 * time.Minute,
		MaxTaps:           10,
		MaxTargetsPerTap:  4,
		Observer:          f.observer,
	}
	for _, fn := range configure {
		fn(&options)
	}
	f.manager = NewManager(testr.New(t), registry, options)
	return f
}

func (f *tapFixture) open(t *testing.T, ctx context.Context, options TapOptions) (string, <-chan Event) {
	t.Helper()
	if options.Owner == "" {
		options.Owner = testOwner
	}
	if options.User == "" {
		options.User = options.Owner
	}
	if options.Selections == nil {
		options.Selections = []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")}
	}
	if options.Interval == 0 {
		options.Interval = testInterval
	}
	id, events, err := f.manager.Open(ctx, options)
	require.NoError(t, err)
	return id, events
}

func selection(target string, metrics ...string) apisv1.MetricsSelection {
	return apisv1.MetricsSelection{Target: target, Metrics: metrics}
}

// nextEvent returns the next event of a tap, which must not have ended.
func nextEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		require.True(t, ok, "the tap ended")
		return event
	case <-time.After(eventTimeout):
		require.FailNow(t, "no event from the tap")
		return Event{}
	}
}

func nextTapEvent(t *testing.T, events <-chan Event) apisv1.MetricsTapEvent {
	t.Helper()
	event := nextEvent(t, events)
	require.Equal(t, EventTap, event.Name)
	return event.Data.(apisv1.MetricsTapEvent)
}

func nextScrapeEvent(t *testing.T, events <-chan Event) apisv1.MetricsScrapeEvent {
	t.Helper()
	event := nextEvent(t, events)
	require.Equal(t, EventScrape, event.Name, "%+v", event.Data)
	return event.Data.(apisv1.MetricsScrapeEvent)
}

func nextScrapeErrorEvent(t *testing.T, events <-chan Event) apisv1.MetricsScrapeErrorEvent {
	t.Helper()
	event := nextEvent(t, events)
	require.Equal(t, EventScrapeError, event.Name, "%+v", event.Data)
	return event.Data.(apisv1.MetricsScrapeErrorEvent)
}

// requireClosed asserts that the tap ends without sending anything else.
func requireClosed(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case event, ok := <-events:
		require.False(t, ok, "unexpected event: %+v", event)
	case <-time.After(eventTimeout):
		require.FailNow(t, "the tap did not end")
	}
}

func TestTapFirstScrapeAndCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		start := time.Now()
		selections := []apisv1.MetricsSelection{selection("antrea-controller", "metric_b", "metric_a")}
		id, events := f.open(t, t.Context(), TapOptions{Selections: selections})

		tapEvent := nextTapEvent(t, events)
		assert.Equal(t, id, tapEvent.ID)
		assert.Len(t, id, 32)
		assert.Equal(t, "10s", tapEvent.Interval)
		assert.Equal(t, selections, tapEvent.Selections)
		assert.Equal(t, 1, f.observer.openTaps())

		// The first scrape does not wait for the first tick.
		for tick := range 3 {
			scrape := nextScrapeEvent(t, events)
			elapsed := time.Duration(tick) * testInterval
			assert.Equal(t, elapsed, time.Since(start), "tick %d", tick)
			assert.Equal(t, "antrea-controller", scrape.Target)
			assert.Equal(t, start.Add(elapsed).UTC(), scrape.Timestamp)
			// Only what was selected, in the order it was selected in.
			assert.Equal(t, []apisv1.MetricFamilySamples{
				{Name: "metric_b", Type: "gauge", Samples: []apisv1.MetricSample{{Value: "1"}}},
				{Name: "metric_a", Type: "gauge", Samples: []apisv1.MetricSample{{Value: "1"}}},
			}, scrape.Families)
			assert.False(t, scrape.Truncated)
		}
		assert.Equal(t, 3, f.controller.scrapeCount(""))
	})
}

func TestTapSelectionUpdate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		start := time.Now()
		var authorized [][]string
		id, events := f.open(t, t.Context(), TapOptions{
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
			Authorizer: func(_ context.Context, targetGroups []string) error {
				authorized = append(authorized, targetGroups)
				return nil
			},
		})
		nextTapEvent(t, events)
		nextScrapeEvent(t, events)

		// Partway through the interval.
		time.Sleep(3 * time.Second)
		updated := []apisv1.MetricsSelection{
			selection("antrea-agent/node-1", "metric_c"),
			selection("antrea-controller", "metric_b"),
		}
		assert.True(t, f.manager.HasTap(id, testOwner))
		require.NoError(t, f.manager.UpdateSelections(id, testOwner, updated))

		// The update is announced right away, on the same stream and with the same ID...
		tapEvent := nextTapEvent(t, events)
		assert.Equal(t, 3*time.Second, time.Since(start))
		assert.Equal(t, id, tapEvent.ID)
		assert.Equal(t, updated, tapEvent.Selections)

		// ... and applies from the next tick, which comes when it would have come anyway.
		scrapes := map[string]apisv1.MetricsScrapeEvent{}
		for range 2 {
			scrape := nextScrapeEvent(t, events)
			assert.Equal(t, testInterval, time.Since(start))
			scrapes[scrape.Target] = scrape
		}
		require.Contains(t, scrapes, "antrea-agent/node-1")
		require.Contains(t, scrapes, "antrea-controller")
		assert.Equal(t, "metric_c", scrapes["antrea-agent/node-1"].Families[0].Name)
		assert.Equal(t, "metric_b", scrapes["antrea-controller"].Families[0].Name)
		assert.Equal(t, 1, f.agent.scrapeCount("node-1"))

		// The periodic check is made for what the tap selects now.
		time.Sleep(reauthorizeInterval - time.Since(start))
		synctest.Wait()
		assert.Equal(t, [][]string{{TargetGroupAgent, TargetGroupController}}, authorized)
	})
}

// A result which the tap has sent for the metrics of one selection is new for the metrics of the
// next one: the tick which follows an update sends it, although its scrape is not new.
func TestTapSelectionUpdateOfMetricsAfterLateTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// As in TestTapDoesNotSendAResultTwice, the access check delays a tick, and the next
		// one is handed the same result.
		const checkDuration = 3 * time.Second
		f := newTapFixture(t)
		start := time.Now()
		id, events := f.open(t, t.Context(), TapOptions{
			Interval:   testMinScrapeInterval,
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
			Authorizer: func(context.Context, []string) error {
				time.Sleep(checkDuration)
				return nil
			},
		})
		nextTapEvent(t, events)
		for range int(reauthorizeInterval / testMinScrapeInterval) {
			nextScrapeEvent(t, events)
		}
		late := nextScrapeEvent(t, events)
		require.Equal(t, reauthorizeInterval+checkDuration, time.Since(start))
		assert.Equal(t, "metric_a", late.Families[0].Name)

		// The update comes between the late tick and the next one.
		time.Sleep(time.Second)
		updated := []apisv1.MetricsSelection{selection("antrea-controller", "metric_b")}
		require.NoError(t, f.manager.UpdateSelections(id, testOwner, updated))
		assert.Equal(t, updated, nextTapEvent(t, events).Selections)

		next := nextScrapeEvent(t, events)
		assert.Equal(t, reauthorizeInterval+testMinScrapeInterval, time.Since(start))
		assert.Equal(t, "metric_b", next.Families[0].Name)
		assert.Equal(t, late.Timestamp, next.Timestamp)
	})
}

// An update which is pending when a tick is due is used by that tick, and still announced before
// it.
func TestTapSelectionUpdateOnTick(t *testing.T) {
	// Without the rule, the loop takes the tick or the update first at random: the scenario is
	// played several times so that a tick which does not apply the update first is not missed.
	for range 8 {
		synctest.Test(t, func(t *testing.T) {
			f := newTapFixture(t)
			id, events := f.open(t, t.Context(), TapOptions{
				Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
			})
			// Nothing reads the events: the tap fills its buffer, and then waits in the middle
			// of a tick with one more event, for long enough that the next tick is due.
			time.Sleep(time.Duration(tapEventBuffer+5) * testInterval)
			synctest.Wait()
			require.Len(t, events, tapEventBuffer)
			updated := []apisv1.MetricsSelection{selection("antrea-controller", "metric_b")}
			require.NoError(t, f.manager.UpdateSelections(id, testOwner, updated))

			// What the tap had to say before the update.
			nextTapEvent(t, events)
			for range tapEventBuffer {
				assert.Equal(t, "metric_a", nextScrapeEvent(t, events).Families[0].Name)
			}
			// The tick which is due does not scrape the old selection once more.
			assert.Equal(t, updated, nextTapEvent(t, events).Selections)
			assert.Equal(t, "metric_b", nextScrapeEvent(t, events).Families[0].Name)
		})
	}
}

func TestTapScrapeErrorIsNotTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		start := time.Now()
		f.agent.setScrape(func(context.Context, string) ([]*dto.MetricFamily, error) {
			return nil, newError(ErrorCodeNotFound, `no Antrea agent on Node "node-1"`, errors.New("antreaagentinfos.crd.antrea.io \"node-1\" not found"))
		})
		_, events := f.open(t, t.Context(), TapOptions{
			Selections: []apisv1.MetricsSelection{selection("antrea-agent/node-1", "metric_a")},
		})
		nextTapEvent(t, events)

		for tick := range 2 {
			scrapeErr := nextScrapeErrorEvent(t, events)
			assert.Equal(t, apisv1.MetricsScrapeErrorEvent{
				Target:    "antrea-agent/node-1",
				Timestamp: start.Add(time.Duration(tick) * testInterval).UTC(),
				Code:      ErrorCodeNotFound,
				// The message for clients, without the underlying error.
				Message: `no Antrea agent on Node "node-1"`,
			}, scrapeErr)
		}

		// The Node joins: the same tap picks it up.
		f.agent.setScrape(func(context.Context, string) ([]*dto.MetricFamily, error) {
			return gaugeFamilies(3, "metric_a"), nil
		})
		scrape := nextScrapeEvent(t, events)
		assert.Equal(t, 2*testInterval, time.Since(start))
		assert.Equal(t, "3", scrape.Families[0].Samples[0].Value)
	})
}

// A tap does not wait for a target longer than its own interval, and a slow target does not hold
// up the others.
func TestTapScrapeWaitTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		start := time.Now()
		scrapeEnded := make(chan time.Duration, 1)
		f.agent.setScrape(func(ctx context.Context, _ string) ([]*dto.MetricFamily, error) {
			<-ctx.Done()
			scrapeEnded <- time.Since(start)
			return nil, newError(ErrorCodeTimeout, "the scrape timed out", ctx.Err())
		})
		const interval = 6 * time.Second
		_, events := f.open(t, t.Context(), TapOptions{
			Interval: interval,
			Selections: []apisv1.MetricsSelection{
				selection("antrea-agent/node-1", "metric_a"),
				selection("antrea-controller", "metric_a"),
			},
		})
		nextTapEvent(t, events)

		scrape := nextScrapeEvent(t, events)
		assert.Equal(t, "antrea-controller", scrape.Target)
		assert.Zero(t, time.Since(start))

		scrapeErr := nextScrapeErrorEvent(t, events)
		assert.Equal(t, interval, time.Since(start))
		assert.Equal(t, "antrea-agent/node-1", scrapeErr.Target)
		assert.Equal(t, ErrorCodeTimeout, scrapeErr.Code)

		// The scrape is not this tap's to cancel: it goes on until its own timeout, for the
		// taps which can wait longer.
		assert.Equal(t, maxScrapeTimeout, <-scrapeEnded)
	})
}

// Targets which do not answer hold up no other target, however many they are: not in the tap
// which selects them, and not in the tap of someone else. Every tick is on time.
func TestTapSlowTargetsHoldUpNoOther(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const slowTargets = 19
		const ticks = 4
		f := newTapFixture(t, func(o *ManagerOptions) { o.MaxTargetsPerTap = slowTargets + 1 })
		f.agent.setScrape(func(ctx context.Context, instance string) ([]*dto.MetricFamily, error) {
			if instance == "healthy" {
				return gaugeFamilies(1, "metric_a"), nil
			}
			<-ctx.Done()
			return nil, newError(ErrorCodeTimeout, "the scrape timed out", ctx.Err())
		})
		start := time.Now()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		selections := []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")}
		for i := range slowTargets {
			selections = append(selections, selection(fmt.Sprintf("antrea-agent/node-%d", i), "metric_a"))
		}
		_, slowEvents := f.open(t, ctx, TapOptions{Interval: testMinScrapeInterval, Selections: selections})
		// The scrapes of the one target of that tap which answers, among the errors of the others.
		var mutex sync.Mutex
		var controllerScrapes []time.Duration
		go func() {
			for event := range slowEvents {
				if scrape, ok := event.Data.(apisv1.MetricsScrapeEvent); ok && scrape.Target == "antrea-controller" {
					mutex.Lock()
					controllerScrapes = append(controllerScrapes, time.Since(start))
					mutex.Unlock()
				}
			}
		}()

		_, events := f.open(t, ctx, TapOptions{
			Owner:      "user:bob",
			Interval:   testMinScrapeInterval,
			Selections: []apisv1.MetricsSelection{selection("antrea-agent/healthy", "metric_a")},
		})
		nextTapEvent(t, events)
		var expected []time.Duration
		for tick := range ticks {
			expected = append(expected, time.Duration(tick)*testMinScrapeInterval)
			assert.Equal(t, "antrea-agent/healthy", nextScrapeEvent(t, events).Target)
			assert.Equal(t, expected[tick], time.Since(start))
		}
		synctest.Wait()
		mutex.Lock()
		assert.Equal(t, expected, controllerScrapes)
		mutex.Unlock()

		// The taps end, and the scrapes which are still in flight with them.
		cancel()
		time.Sleep(maxScrapeTimeout)
	})
}

// A tick which follows a late one too closely is handed the result the tap has sent already, and
// does not send it again.
func TestTapDoesNotSendAResultTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// The access check delays the tick which is due with it, by less than the interval
		// and by enough for the next tick to come while the result is still reused.
		const checkDuration = 3 * time.Second
		require.Less(t, testMinScrapeInterval-checkDuration, testMinScrapeInterval/2)
		f := newTapFixture(t)
		start := time.Now()
		_, events := f.open(t, t.Context(), TapOptions{
			Interval:   testMinScrapeInterval,
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
			Authorizer: func(context.Context, []string) error {
				time.Sleep(checkDuration)
				return nil
			},
		})
		nextTapEvent(t, events)
		for tick := range int(reauthorizeInterval / testMinScrapeInterval) {
			nextScrapeEvent(t, events)
			assert.Equal(t, time.Duration(tick)*testMinScrapeInterval, time.Since(start))
		}
		late := nextScrapeEvent(t, events)
		assert.Equal(t, reauthorizeInterval+checkDuration, time.Since(start))

		// Nothing is sent for the tick which is due one interval after the check was.
		next := nextScrapeEvent(t, events)
		assert.Equal(t, reauthorizeInterval+2*testMinScrapeInterval, time.Since(start))
		assert.NotEqual(t, late.Timestamp, next.Timestamp)
	})
}

func TestTapEndsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		id, events := f.open(t, ctx, TapOptions{
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
		})
		nextTapEvent(t, events)
		nextScrapeEvent(t, events)
		assert.True(t, f.manager.HasTap(id, testOwner))

		cancel()
		requireClosed(t, events)
		synctest.Wait()
		assert.False(t, f.manager.HasTap(id, testOwner))
		err := f.manager.UpdateSelections(id, testOwner, []apisv1.MetricsSelection{selection("antrea-controller", "metric_b")})
		assert.ErrorIs(t, err, ErrTapNotFound)
		assert.Zero(t, f.observer.openTaps())
	})
}

// A client which stops reading does not make the tap drop events, and does not keep it from ending.
func TestTapEndsWithContextWhileBlockedOnClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		id, events := f.open(t, ctx, TapOptions{
			Interval:   testMinScrapeInterval,
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
		})
		time.Sleep(time.Duration(tapEventBuffer+5) * testMinScrapeInterval)
		synctest.Wait()
		assert.Len(t, events, tapEventBuffer)

		cancel()
		synctest.Wait()
		assert.False(t, f.manager.HasTap(id, testOwner))
	})
}

func TestTapReauthorization(t *testing.T) {
	selections := []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")}
	// drain reads events until the tap ends, and returns the last one. Every event before the
	// last must be a scrape.
	drain := func(t *testing.T, events <-chan Event) Event {
		t.Helper()
		var last Event
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return last
				}
				if last.Name != "" {
					require.Equal(t, EventScrape, last.Name)
				}
				last = event
			case <-time.After(eventTimeout):
				require.FailNow(t, "the tap did not end")
			}
		}
	}

	t.Run("revoked grant ends the tap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newTapFixture(t)
			start := time.Now()
			checks := 0
			id, events := f.open(t, t.Context(), TapOptions{
				Selections: selections,
				Authorizer: func(_ context.Context, targetGroups []string) error {
					assert.Equal(t, []string{TargetGroupController}, targetGroups)
					checks++
					if checks < 3 {
						return nil
					}
					return fmt.Errorf("review denied: %w", ErrForbidden)
				},
			})
			nextTapEvent(t, events)

			last := drain(t, events)
			assert.Equal(t, 3*reauthorizeInterval, time.Since(start))
			assert.Equal(t, Event{Name: EventError, Data: apisv1.MetricsTapErrorEvent{
				Code:    TapErrorCodeForbidden,
				Message: "access to the selected metrics was revoked",
			}}, last)
			synctest.Wait()
			assert.False(t, f.manager.HasTap(id, testOwner))
		})
	})

	// A check and a tick are due together every minute. The check comes first, so that the
	// tick scrapes nothing for an owner whose grant was removed.
	t.Run("a check which is due comes before the tick", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			const checks = 10
			const ticksPerCheck = int(reauthorizeInterval / testInterval)
			f := newTapFixture(t)
			var mutex sync.Mutex
			var order []string
			record := func(what string) {
				mutex.Lock()
				defer mutex.Unlock()
				order = append(order, what)
			}
			f.controller.setScrape(func(context.Context, string) ([]*dto.MetricFamily, error) {
				record("scrape")
				return gaugeFamilies(1, "metric_a"), nil
			})
			made := 0
			_, events := f.open(t, t.Context(), TapOptions{
				Selections: selections,
				Authorizer: func(context.Context, []string) error {
					record("check")
					made++
					if made < checks {
						return nil
					}
					return ErrForbidden
				},
			})
			nextTapEvent(t, events)
			last := drain(t, events)
			assert.Equal(t, EventError, last.Name)

			// The first scrape and the ticks of the first minute, then each check followed by
			// the ticks of its minute, and nothing after the check which ends the tap.
			var expected []string
			for range ticksPerCheck {
				expected = append(expected, "scrape")
			}
			for range checks - 1 {
				expected = append(expected, "check")
				for range ticksPerCheck {
					expected = append(expected, "scrape")
				}
			}
			expected = append(expected, "check")
			mutex.Lock()
			defer mutex.Unlock()
			assert.Equal(t, expected, order)
		})
	})

	t.Run("rejected credential ends the tap", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newTapFixture(t)
			start := time.Now()
			_, events := f.open(t, t.Context(), TapOptions{
				Selections: selections,
				Authorizer: func(context.Context, []string) error { return ErrUnauthenticated },
			})
			nextTapEvent(t, events)

			last := drain(t, events)
			assert.Equal(t, reauthorizeInterval, time.Since(start))
			require.Equal(t, EventError, last.Name)
			tapErr := last.Data.(apisv1.MetricsTapErrorEvent)
			assert.Equal(t, TapErrorCodeUnauthenticated, tapErr.Code)
			assert.False(t, tapErr.Retryable)
		})
	})

	t.Run("a check without an answer keeps the last one, for a while", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newTapFixture(t)
			start := time.Now()
			checks := 0
			_, events := f.open(t, t.Context(), TapOptions{
				Selections: selections,
				Authorizer: func(context.Context, []string) error {
					checks++
					// One answer in the middle of a run of failures starts the count again.
					if checks == maxAuthorizationFailures+1 {
						return nil
					}
					return errors.New("the API server is having a bad day")
				},
			})
			nextTapEvent(t, events)

			last := drain(t, events)
			// maxAuthorizationFailures failures, one answer, and then one failure more
			// than the tap tolerates.
			wantChecks := 2*maxAuthorizationFailures + 2
			assert.Equal(t, wantChecks, checks)
			assert.Equal(t, time.Duration(wantChecks)*reauthorizeInterval, time.Since(start))
			assert.Equal(t, Event{Name: EventError, Data: apisv1.MetricsTapErrorEvent{
				Code:      TapErrorCodeAuthorization,
				Message:   "could not verify access to the selected metrics",
				Retryable: true,
			}}, last)
		})
	})

	t.Run("a check is bounded", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newTapFixture(t)
			var waited time.Duration
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, events := f.open(t, ctx, TapOptions{
				Selections: selections,
				Authorizer: func(ctx context.Context, _ []string) error {
					begin := time.Now()
					<-ctx.Done()
					waited = time.Since(begin)
					return ctx.Err()
				},
			})
			nextTapEvent(t, events)
			time.Sleep(reauthorizeInterval + authorizationTimeout)
			synctest.Wait()
			assert.Equal(t, authorizationTimeout, waited)
		})
	})
}

func TestTapCaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const maxTaps = maxTapsPerUser + 2
		f := newTapFixture(t, func(o *ManagerOptions) { o.MaxTaps = maxTaps })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		selections := []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")}
		// The cap is per user and not per owner: the sessions of a user share it.
		const alice = "user:alice"
		aliceCtx, cancelAlice := context.WithCancel(ctx)
		_, aliceEvents := f.open(t, aliceCtx, TapOptions{Owner: "session:0/alice", User: alice})
		for i := 1; i < maxTapsPerUser; i++ {
			f.open(t, ctx, TapOptions{Owner: fmt.Sprintf("session:%d/alice", i), User: alice})
		}
		_, _, err := f.manager.Open(ctx, TapOptions{Owner: "session:new/alice", User: alice, Interval: testInterval, Selections: selections})
		assert.ErrorIs(t, err, ErrTooManyTapsForUser)

		// A tap which ends gives its place back to its user.
		nextTapEvent(t, aliceEvents)
		nextScrapeEvent(t, aliceEvents)
		cancelAlice()
		requireClosed(t, aliceEvents)
		f.open(t, ctx, TapOptions{Owner: "session:new/alice", User: alice})
		_, _, err = f.manager.Open(ctx, TapOptions{Owner: "session:newer/alice", User: alice, Interval: testInterval, Selections: selections})
		assert.ErrorIs(t, err, ErrTooManyTapsForUser)

		// Other users are not affected, until the global cap.
		bobCtx, cancelBob := context.WithCancel(ctx)
		_, bobEvents := f.open(t, bobCtx, TapOptions{Owner: "user:bob"})
		f.open(t, ctx, TapOptions{Owner: "user:carol"})
		_, _, err = f.manager.Open(ctx, TapOptions{Owner: "user:dave", User: "user:dave", Interval: testInterval, Selections: selections})
		assert.ErrorIs(t, err, ErrTooManyTaps)
		assert.Equal(t, maxTaps, f.observer.openTaps())

		// A tap which ends frees its slot.
		nextTapEvent(t, bobEvents)
		nextScrapeEvent(t, bobEvents)
		cancelBob()
		requireClosed(t, bobEvents)
		synctest.Wait()
		f.open(t, ctx, TapOptions{Owner: "user:dave"})
		assert.Equal(t, maxTaps, f.observer.openTaps())
	})
}

func TestTapOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		id, events := f.open(t, ctx, TapOptions{
			Selections: []apisv1.MetricsSelection{selection("antrea-controller", "metric_a")},
		})
		nextTapEvent(t, events)
		nextScrapeEvent(t, events)

		assert.False(t, f.manager.HasTap(id, "session:2/alice"))
		assert.False(t, f.manager.HasTap("0123456789abcdef0123456789abcdef", testOwner))
		err := f.manager.UpdateSelections(id, "session:2/alice", []apisv1.MetricsSelection{selection("antrea-controller", "metric_b")})
		assert.ErrorIs(t, err, ErrTapNotFound)

		// The tap of another owner was not updated: it goes on with its own selection.
		const ticks = 2
		time.Sleep(ticks * testInterval)
		synctest.Wait()
		require.Len(t, events, ticks)
		for range ticks {
			assert.Equal(t, "metric_a", nextScrapeEvent(t, events).Families[0].Name)
		}
	})
}

// However many taps read a target, it is scraped once per tick, and every tap gets a new result on
// every tick, including at the minimum interval.
func TestTapsShareScrapes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		var mutex sync.Mutex
		value := 0.0
		f.agent.setScrape(func(context.Context, string) ([]*dto.MetricFamily, error) {
			mutex.Lock()
			defer mutex.Unlock()
			value++
			return gaugeFamilies(value, "metric_a"), nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const taps = 3
		var streams []<-chan Event
		for i := range taps {
			_, events := f.open(t, ctx, TapOptions{
				Owner:      fmt.Sprintf("user:%d", i),
				Interval:   testMinScrapeInterval,
				Selections: []apisv1.MetricsSelection{selection("antrea-agent/node-1", "metric_a")},
			})
			nextTapEvent(t, events)
			streams = append(streams, events)
		}

		const ticks = 4
		for tick := range ticks {
			for _, events := range streams {
				scrape := nextScrapeEvent(t, events)
				assert.Equal(t, fmt.Sprint(tick+1), scrape.Families[0].Samples[0].Value)
			}
		}
		assert.Equal(t, ticks, f.agent.scrapeCount("node-1"))
		assert.Len(t, f.observer.scrapeOutcomes(), ticks)
	})
}

// A result is identified by its timestamp, and is sent to a tap once.
func TestTapSkipsResultAlreadySent(t *testing.T) {
	now := time.Now()
	tp := &tap{lastEmitted: make(map[string]time.Time)}
	assert.True(t, tp.isNew("antrea-controller", now))
	assert.False(t, tp.isNew("antrea-controller", now))
	assert.True(t, tp.isNew("antrea-agent/node-1", now))
	assert.True(t, tp.isNew("antrea-controller", now.Add(time.Second)))
}

func TestParseInterval(t *testing.T) {
	f := newTapFixture(t)
	testCases := []struct {
		interval string
		expected time.Duration
		err      string
	}{
		{interval: "", expected: DefaultScrapeInterval},
		{interval: "5s", expected: 5 * time.Second},
		{interval: "5m", expected: 5 * time.Minute},
		{interval: "1m30s", expected: 90 * time.Second},
		{interval: "4999ms", err: "must be between 5s and 5m0s"},
		{interval: "5m1s", err: "must be between 5s and 5m0s"},
		{interval: "-10s", err: "must be between"},
		{interval: "10", err: "expected a duration"},
		{interval: "soon", err: "expected a duration"},
	}
	for _, tc := range testCases {
		t.Run(tc.interval, func(t *testing.T) {
			d, err := f.manager.ParseInterval(tc.interval)
			if tc.err == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, d)
				return
			}
			requestErr, ok := errors.AsType[*RequestError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, RequestErrorInvalid, requestErr.Kind)
			assert.Contains(t, requestErr.Message, tc.err)
		})
	}

	// The default follows a minimum which is above it.
	slow := newTapFixture(t, func(o *ManagerOptions) { o.MinScrapeInterval = time.Minute })
	d, err := slow.manager.ParseInterval("")
	require.NoError(t, err)
	assert.Equal(t, time.Minute, d)
}

func TestValidateSelections(t *testing.T) {
	f := newTapFixture(t)
	manyMetrics := make([]string, maxMetricsPerSelection+1)
	for i := range manyMetrics {
		manyMetrics[i] = fmt.Sprintf("metric_%d", i)
	}
	testCases := []struct {
		name         string
		selections   []apisv1.MetricsSelection
		targetGroups []string
		kind         RequestErrorKind
		err          string
	}{
		{name: "no target", selections: nil, kind: RequestErrorInvalid, err: "no target is selected"},
		{name: "empty list of targets", selections: []apisv1.MetricsSelection{}, kind: RequestErrorInvalid, err: "no target is selected"},
		{
			name: "several targets of several target groups",
			selections: []apisv1.MetricsSelection{
				selection("antrea-controller", "metric_a"),
				selection("antrea-agent/node-2", "metric_a", "metric_b"),
				selection("antrea-agent/node-1", "metric_a"),
			},
			targetGroups: []string{TargetGroupAgent, TargetGroupController},
		},
		{
			name: "too many targets",
			selections: []apisv1.MetricsSelection{
				selection("antrea-agent/node-1", "m"), selection("antrea-agent/node-2", "m"), selection("antrea-agent/node-3", "m"),
				selection("antrea-agent/node-4", "m"), selection("antrea-agent/node-5", "m"),
			},
			kind: RequestErrorInvalid,
			err:  "at most 4",
		},
		{name: "empty target", selections: []apisv1.MetricsSelection{selection("", "m")}, kind: RequestErrorInvalid, err: "invalid target"},
		{name: "unknown target group", selections: []apisv1.MetricsSelection{selection("kubelet", "m")}, kind: RequestErrorUnknownTargetGroup, err: `unknown target group "kubelet"`},
		{name: "unavailable target group", selections: []apisv1.MetricsSelection{selection("flow-aggregator", "m")}, kind: RequestErrorUnavailableTargetGroup, err: "not supported yet"},
		{name: "agent without a Node", selections: []apisv1.MetricsSelection{selection("antrea-agent", "m")}, kind: RequestErrorInvalid, err: "expected antrea-agent/<instance>"},
		{name: "agent with an empty Node", selections: []apisv1.MetricsSelection{selection("antrea-agent/", "m")}, kind: RequestErrorInvalid, err: "invalid target"},
		{name: "agent with an invalid Node", selections: []apisv1.MetricsSelection{selection("antrea-agent/node 1", "m")}, kind: RequestErrorInvalid, err: "invalid target"},
		{name: "agent with a path", selections: []apisv1.MetricsSelection{selection("antrea-agent/node-1/metrics", "m")}, kind: RequestErrorInvalid, err: "invalid target"},
		{name: "controller with an instance", selections: []apisv1.MetricsSelection{selection("antrea-controller/0", "m")}, kind: RequestErrorInvalid, err: "has a single target"},
		{
			name:       "target selected twice",
			selections: []apisv1.MetricsSelection{selection("antrea-controller", "a"), selection("antrea-controller", "b")},
			kind:       RequestErrorInvalid,
			err:        "more than once",
		},
		{name: "no metric", selections: []apisv1.MetricsSelection{selection("antrea-controller")}, kind: RequestErrorInvalid, err: "names no metric"},
		{name: "too many metrics", selections: []apisv1.MetricsSelection{selection("antrea-controller", manyMetrics...)}, kind: RequestErrorInvalid, err: "too many metrics"},
		{name: "empty metric name", selections: []apisv1.MetricsSelection{selection("antrea-controller", "a", "")}, kind: RequestErrorInvalid, err: "invalid metric name"},
		{name: "metric selected twice", selections: []apisv1.MetricsSelection{selection("antrea-controller", "a", "a")}, kind: RequestErrorInvalid, err: "more than once"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			targetGroups, err := f.manager.ValidateSelections(tc.selections)
			if tc.err == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.targetGroups, targetGroups)
				return
			}
			requestErr, ok := errors.AsType[*RequestError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, tc.kind, requestErr.Kind)
			assert.Contains(t, requestErr.Message, tc.err)

			// Opening a tap checks the same things.
			_, _, err = f.manager.Open(t.Context(), TapOptions{Owner: testOwner, Interval: testInterval, Selections: tc.selections})
			assert.Equal(t, requestErr, err)
		})
	}
}

func TestResolveTarget(t *testing.T) {
	f := newTapFixture(t)
	for id, targetGroup := range map[string]string{
		"antrea-controller":   TargetGroupController,
		"antrea-agent/node-1": TargetGroupAgent,
		// Not available, which is not for this call to say: the caller authorizes the request
		// against the target group first.
		"flow-aggregator": TargetGroupFlowAggregator,
	} {
		got, err := f.manager.ResolveTarget(id)
		require.NoError(t, err, id)
		assert.Equal(t, targetGroup, got)
	}
	for id, kind := range map[string]RequestErrorKind{
		"":                    RequestErrorInvalid,
		"/node-1":             RequestErrorInvalid,
		"antrea-agent":        RequestErrorInvalid,
		"antrea-controller/x": RequestErrorInvalid,
		"kubelet":             RequestErrorUnknownTargetGroup,
		"kubelet/node-1":      RequestErrorUnknownTargetGroup,
	} {
		_, err := f.manager.ResolveTarget(id)
		requestErr, ok := errors.AsType[*RequestError](err)
		require.True(t, ok, "%q: %v", id, err)
		assert.Equal(t, kind, requestErr.Kind, id)
	}
}

func TestTargetGroupsAndTargets(t *testing.T) {
	f := newTapFixture(t)
	assert.Equal(t, []TargetGroupStatus{
		{Name: TargetGroupController, Available: true},
		{Name: TargetGroupAgent, Available: true},
		{Name: TargetGroupFlowAggregator, Available: false, Reason: "Flow Aggregator metrics are not supported yet"},
	}, f.manager.TargetGroups())

	f.controller.targets = []apisv1.MetricsTarget{{ID: TargetGroupController}}
	targets, err := f.manager.Targets(t.Context(), TargetGroupController)
	require.NoError(t, err)
	assert.Equal(t, f.controller.targets, targets)

	// No agent yet: an empty list and not null.
	targets, err = f.manager.Targets(t.Context(), TargetGroupAgent)
	require.NoError(t, err)
	assert.NotNil(t, targets)
	assert.Empty(t, targets)

	f.agent.targetErr = newError(ErrorCodeUnavailable, "failed to list Antrea agents", errors.New("forbidden"))
	_, err = f.manager.Targets(t.Context(), TargetGroupAgent)
	assert.Equal(t, "failed to list Antrea agents", ErrorMessage(err))

	_, err = f.manager.Targets(t.Context(), TargetGroupFlowAggregator)
	assert.Error(t, err)
}

// testFamilies is one family of each kind, with the values JSON numbers cannot carry.
func testFamilies() []*dto.MetricFamily {
	label := func(name, value string) *dto.LabelPair {
		return &dto.LabelPair{Name: new(name), Value: new(value)}
	}
	return []*dto.MetricFamily{
		{
			Name: new("requests_total"),
			Help: new("Number of requests."),
			Type: dto.MetricType_COUNTER.Enum(),
			Metric: []*dto.Metric{
				{Label: []*dto.LabelPair{label("code", "200"), label("method", "GET")}, Counter: &dto.Counter{Value: new(1027.0)}},
				{Label: []*dto.LabelPair{label("code", "500")}, Counter: &dto.Counter{Value: new(3.0)}},
			},
		},
		{
			Name:   new("pod_count"),
			Help:   new("Number of Pods."),
			Type:   dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{{Gauge: &dto.Gauge{Value: new(7.5)}}},
		},
		{
			Name: new("latency_seconds"),
			Help: new("Latency."),
			Type: dto.MetricType_HISTOGRAM.Enum(),
			Metric: []*dto.Metric{{
				Label: []*dto.LabelPair{label("operation", "add")},
				Histogram: &dto.Histogram{
					SampleCount: new(uint64(4)),
					SampleSum:   new(0.9),
					Bucket: []*dto.Bucket{
						{UpperBound: new(0.1), CumulativeCount: new(uint64(1))},
						{UpperBound: new(1.0), CumulativeCount: new(uint64(4))},
						{UpperBound: new(math.Inf(1)), CumulativeCount: new(uint64(4))},
					},
				},
			}, {
				Label: []*dto.LabelPair{label("operation", "delete")},
				Histogram: &dto.Histogram{
					SampleCount: new(uint64(0)),
					SampleSum:   new(0.0),
					Bucket:      []*dto.Bucket{{UpperBound: new(math.Inf(1)), CumulativeCount: new(uint64(0))}},
				},
			}},
		},
		{
			Name: new("gc_duration_seconds"),
			Help: new("GC duration."),
			Type: dto.MetricType_SUMMARY.Enum(),
			Metric: []*dto.Metric{{Summary: &dto.Summary{
				SampleCount: new(uint64(0)),
				SampleSum:   new(0.0),
				// The quantiles of a summary which has observed nothing are NaN.
				Quantile: []*dto.Quantile{{Quantile: new(0.5), Value: new(math.NaN())}},
			}}},
		},
	}
}

func testScrapeResult(families []*dto.MetricFamily) *scrapeResult {
	result := &scrapeResult{families: families, byName: make(map[string]*dto.MetricFamily)}
	for _, family := range families {
		result.byName[family.GetName()] = family
	}
	return result
}

func TestFlattenFamilies(t *testing.T) {
	result := testScrapeResult(testFamilies())

	t.Run("counter, gauge and filtering", func(t *testing.T) {
		families, truncated := flattenFamilies(result, []string{"pod_count", "not_exposed", "requests_total"}, maxSamplesPerEvent)
		assert.False(t, truncated)
		// A metric the target does not expose is left out. The others keep the order of the
		// selection.
		assert.Equal(t, []apisv1.MetricFamilySamples{
			{Name: "pod_count", Type: "gauge", Samples: []apisv1.MetricSample{{Value: "7.5"}}},
			{Name: "requests_total", Type: "counter", Samples: []apisv1.MetricSample{
				{Labels: map[string]string{"code": "200", "method": "GET"}, Value: "1027"},
				{Labels: map[string]string{"code": "500"}, Value: "3"},
			}},
		}, families)
	})

	t.Run("histogram", func(t *testing.T) {
		families, truncated := flattenFamilies(result, []string{"latency_seconds"}, maxSamplesPerEvent)
		assert.False(t, truncated)
		require.Len(t, families, 1)
		assert.Equal(t, "histogram", families[0].Type)
		assert.ElementsMatch(t, []apisv1.MetricSample{
			{Name: "latency_seconds_bucket", Labels: map[string]string{"operation": "add", "le": "0.1"}, Value: "1"},
			{Name: "latency_seconds_bucket", Labels: map[string]string{"operation": "add", "le": "1"}, Value: "4"},
			{Name: "latency_seconds_bucket", Labels: map[string]string{"operation": "add", "le": "+Inf"}, Value: "4"},
			{Name: "latency_seconds_sum", Labels: map[string]string{"operation": "add"}, Value: "0.9"},
			{Name: "latency_seconds_count", Labels: map[string]string{"operation": "add"}, Value: "4"},
			{Name: "latency_seconds_bucket", Labels: map[string]string{"operation": "delete", "le": "+Inf"}, Value: "0"},
			{Name: "latency_seconds_sum", Labels: map[string]string{"operation": "delete"}, Value: "0"},
			{Name: "latency_seconds_count", Labels: map[string]string{"operation": "delete"}, Value: "0"},
		}, families[0].Samples)
	})

	t.Run("summary", func(t *testing.T) {
		families, _ := flattenFamilies(result, []string{"gc_duration_seconds"}, maxSamplesPerEvent)
		require.Len(t, families, 1)
		assert.Equal(t, "summary", families[0].Type)
		assert.ElementsMatch(t, []apisv1.MetricSample{
			// The quantiles carry the name of the family, hence no name.
			{Labels: map[string]string{"quantile": "0.5"}, Value: "NaN"},
			{Name: "gc_duration_seconds_sum", Value: "0"},
			{Name: "gc_duration_seconds_count", Value: "0"},
		}, families[0].Samples)
	})

	t.Run("truncation", func(t *testing.T) {
		// 2 samples for the counter, then room for 3 of the 8 of the histogram. The gauge
		// which follows is left out altogether.
		families, truncated := flattenFamilies(result, []string{"requests_total", "latency_seconds", "pod_count"}, 5)
		assert.True(t, truncated)
		require.Len(t, families, 2)
		assert.Len(t, families[0].Samples, 2)
		assert.Len(t, families[1].Samples, 3)

		// Exactly at the cap is not a truncation.
		families, truncated = flattenFamilies(result, []string{"requests_total", "pod_count"}, 3)
		assert.False(t, truncated)
		assert.Len(t, families, 2)
	})

	t.Run("family without series", func(t *testing.T) {
		empty := testScrapeResult([]*dto.MetricFamily{{Name: new("nothing_yet"), Type: dto.MetricType_COUNTER.Enum()}})
		families, truncated := flattenFamilies(empty, []string{"nothing_yet"}, maxSamplesPerEvent)
		assert.False(t, truncated)
		// An empty list and not null.
		assert.Equal(t, []apisv1.MetricFamilySamples{{Name: "nothing_yet", Type: "counter", Samples: []apisv1.MetricSample{}}}, families)
	})
}

func TestFamilies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTapFixture(t)
		f.agent.setScrape(func(_ context.Context, instance string) ([]*dto.MetricFamily, error) {
			if instance != "node-1" {
				return nil, newError(ErrorCodeNotFound, fmt.Sprintf("no Antrea agent on Node %q", instance), nil)
			}
			return testFamilies(), nil
		})

		list, err := f.manager.Families(t.Context(), "antrea-agent/node-1")
		require.NoError(t, err)
		assert.Equal(t, &apisv1.MetricFamilyList{
			Target:    "antrea-agent/node-1",
			Timestamp: time.Now().UTC(),
			// Sorted by name, and without any value.
			Families: []apisv1.MetricFamilyInfo{
				{Name: "gc_duration_seconds", Type: "summary", Help: "GC duration.", LabelNames: []string{}, SeriesCount: 1},
				// Two label sets, not eight flattened series.
				{Name: "latency_seconds", Type: "histogram", Help: "Latency.", LabelNames: []string{"operation"}, SeriesCount: 2},
				{Name: "pod_count", Type: "gauge", Help: "Number of Pods.", LabelNames: []string{}, SeriesCount: 1},
				{Name: "requests_total", Type: "counter", Help: "Number of requests.", LabelNames: []string{"code", "method"}, SeriesCount: 2},
			},
		}, list)

		_, err = f.manager.Families(t.Context(), "antrea-agent/node-2")
		assert.Equal(t, ErrorCodeNotFound, ErrorCode(err))

		_, err = f.manager.Families(t.Context(), "flow-aggregator")
		assert.Equal(t, ErrorCodeUnsupported, ErrorCode(err))
		assert.Equal(t, "Flow Aggregator metrics are not supported yet", ErrorMessage(err))

		_, err = f.manager.Families(t.Context(), "kubelet")
		_, ok := errors.AsType[*RequestError](err)
		assert.True(t, ok, "%v", err)
	})
}
