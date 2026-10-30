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

package flowstream

import (
	"context"
	"fmt"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const dependencyUnavailableCode = "flow_source_unavailable"

// historyDisabledCode identifies a request for a capability disabled by configuration.
const historyDisabledCode = "flow_history_disabled"

func dependencyUnavailable(message string) *StreamError {
	return &StreamError{msg: message, Code: dependencyUnavailableCode, Retryable: true}
}

// RoutedSubscriber selects the configured source without substituting live data for history.
type RoutedSubscriber struct {
	Live   FlowStreamSubscriber
	Reader FlowStreamSubscriber
}

func (s *RoutedSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flows := make(chan apisv1.FlowStreamEvent)
	errs := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		defer close(errs)
		defer close(flows)
		announced := false
		markReady := func() {
			if !announced {
				close(ready)
				announced = true
			}
		}
		send := func(event apisv1.FlowStreamEvent) error {
			select {
			case flows <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		run := func(upstream FlowStreamSubscriber, source string) error {
			if upstream == nil {
				return dependencyUnavailable("flow source is not configured")
			}
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var count uint64
			err := relay(requestCtx, upstream, scope, filter, nil, func() error {
				markReady()
				return send(apisv1.FlowStreamEvent{Source: source})
			}, func(event apisv1.FlowStreamEvent) error {
				if !filter.following() {
					batchCount := uint64(len(event.Flows))
					if batchCount > uint64(filter.resultLimit())-count {
						return internalStreamError(fmt.Errorf("flow source exceeded the requested result limit"))
					}
					count += batchCount
				}
				return send(event)
			})
			return err
		}
		var err error
		if filter.following() {
			err = run(s.Live, "live")
		} else if s.Reader == nil {
			err = &StreamError{msg: "Historical flow queries are not enabled for this Antrea UI instance.", Code: historyDisabledCode}
		} else {
			err = run(s.Reader, "historical")
		}
		if err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()
	return flows, errs, ready
}

// relay drains both channels before declaring success. A final buffered error
// must never be lost to a simultaneous close of the flow channel.
func relay(ctx context.Context, upstream FlowStreamSubscriber, scope *FlowStreamScope, filter *FlowStreamFilter, changed <-chan struct{}, ready func() error, send func(apisv1.FlowStreamEvent) error) error {
	flows, errs, started := upstream.Subscribe(ctx, scope, filter)
	announced := false
	announce := func() error {
		if announced {
			return nil
		}
		announced = true
		started = nil
		return ready()
	}
	for flows != nil || errs != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
			return dependencyUnavailable("Flow Aggregator Service changed; reconnecting")
		case <-started:
			if err := announce(); err != nil {
				return err
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
			} else if err != nil {
				return err
			}
		case event, ok := <-flows:
			if !ok {
				flows = nil
				continue
			}
			if err := announce(); err != nil {
				return err
			}
			if err := send(event); err != nil {
				return err
			}
		}
	}
	if !announced {
		select {
		case <-started:
			return announce()
		default:
			return dependencyUnavailable("flow source closed before acknowledging the request")
		}
	}
	return ctx.Err()
}
