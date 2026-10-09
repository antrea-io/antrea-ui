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
	"time"

	"github.com/go-logr/logr"
	dto "github.com/prometheus/client_model/go"
	"golang.org/x/sync/singleflight"
)

// maxScrapeTimeout bounds a scrape, the resolution of its target included. A caller can give up
// on it earlier through its own context, which does not stop the scrape for the others.
const maxScrapeTimeout = 10 * time.Second

// ScrapeObserver is told about every scrape which is actually performed, as opposed to a result
// which is shared or reused. outcome is "success" or the code of the failure.
type ScrapeObserver interface {
	ObserveScrape(targetGroup, outcome string)
}

// scrapeResult is the outcome of one scrape of one target. It is shared by every tap which asked
// for the target at about the same time, so it must be treated as read-only.
type scrapeResult struct {
	// timestamp is when the scrape was performed. It identifies the scrape: a tap uses it to
	// tell a result it has already sent from a new one.
	timestamp time.Time
	families  []*dto.MetricFamily
	// byName indexes families.
	byName map[string]*dto.MetricFamily
	// err is set when the scrape failed. A failure is shared and reused like a success: a
	// target which is down is tried once at a time, however many taps select it.
	err error
}

// coalescer makes scrapes independent of how many taps are open: concurrent requests for a target
// share one scrape, and a result is reused for as long as it is fresh.
//
// Sharing is sound because a scrape carries no user identity: every caller would have sent the
// same request, with the same credential.
type coalescer struct {
	// freshness is how long a result is reused. It is half of the minimum scrape interval:
	// antrea-ui then performs at most one scrape per target per half minimum interval, while a
	// tap does not get the same result on two consecutive ticks for as long as they are on
	// time, which puts them at least a full minimum interval apart.
	freshness time.Duration
	logger    logr.Logger
	observer  ScrapeObserver

	group singleflight.Group
	// mutex guards results. It is never held across a scrape.
	//
	// This is a plain map whose entries are removed by a timer each, and not an expiring cache
	// with a janitor goroutine: such a goroutine never stops, which would hang every
	// testing/synctest bubble the coalescer is used in.
	mutex   sync.Mutex
	results map[string]*scrapeResult
}

func newCoalescer(logger logr.Logger, minScrapeInterval time.Duration, observer ScrapeObserver) *coalescer {
	return &coalescer{
		logger:    logger,
		freshness: minScrapeInterval / 2,
		observer:  observer,
		results:   make(map[string]*scrapeResult),
	}
}

// scrape returns a result for the target t of targetGroup which is younger than the freshness
// window, performing a scrape if there is none. ctx only bounds how long this caller waits. The
// returned error is the caller's own (its context ended); the failure of the scrape is in the
// result.
func (c *coalescer) scrape(ctx context.Context, targetGroup TargetGroup, t target) (*scrapeResult, error) {
	if result, ok := c.fresh(t.id); ok {
		return result, nil
	}
	ch := c.group.DoChan(t.id, func() (any, error) {
		// Another caller may have completed a scrape between the check above and this call
		// becoming the leader.
		if result, ok := c.fresh(t.id); ok {
			return result, nil
		}
		// The leader is whichever caller arrived first. Its context must not bound the
		// scrape: closing that tap mid-scrape would fail every other tap waiting on the same
		// call.
		scrapeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), maxScrapeTimeout)
		defer cancel()
		result := &scrapeResult{timestamp: time.Now()}
		result.families, result.err = targetGroup.Scrape(scrapeCtx, t.instance)
		if result.err != nil {
			// This is the only place the cause of a failure is reported: clients are told
			// its class and nothing else.
			c.logger.V(1).Info("Metrics scrape failed", "target", t.id, "code", ErrorCode(result.err), "error", result.err.Error())
		} else {
			result.byName = make(map[string]*dto.MetricFamily, len(result.families))
			for _, family := range result.families {
				result.byName[family.GetName()] = family
			}
		}
		if c.observer != nil {
			c.observer.ObserveScrape(targetGroup.Name(), scrapeOutcome(result.err))
		}
		c.store(t.id, result)
		return result, nil
	})
	select {
	case r := <-ch:
		return r.Val.(*scrapeResult), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *coalescer) fresh(id string) (*scrapeResult, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	result, ok := c.results[id]
	if !ok || time.Since(result.timestamp) >= c.freshness {
		return nil, false
	}
	return result, true
}

func (c *coalescer) store(id string, result *scrapeResult) {
	c.mutex.Lock()
	c.results[id] = result
	c.mutex.Unlock()
	// A result holds every family of its target. It is dropped as soon as it is no longer
	// fresh, and not when a later scrape completes: there may be none for a long time.
	time.AfterFunc(c.freshness-time.Since(result.timestamp), func() {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		if c.results[id] == result {
			delete(c.results, id)
		}
	})
}

func scrapeOutcome(err error) string {
	if err == nil {
		return "success"
	}
	return ErrorCode(err)
}

// ErrorCode returns the code of a scrape failure, for a client.
func ErrorCode(err error) string {
	if scrapeErr, ok := errors.AsType[*Error](err); ok {
		return scrapeErr.Code
	}
	return ErrorCodeFailed
}

// ErrorMessage returns the message of a scrape failure which is safe to send to a client.
func ErrorMessage(err error) string {
	if scrapeErr, ok := errors.AsType[*Error](err); ok {
		return scrapeErr.Message
	}
	return "the scrape failed"
}
