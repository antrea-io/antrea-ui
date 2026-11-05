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
	"errors"
	"fmt"
	"io"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// StreamErrorCodeSourceUnavailable identifies a flow source that cannot be reached or is not
// ready to serve. Retryable.
const StreamErrorCodeSourceUnavailable = "flow_source_unavailable"

// StreamErrorCodeSourceNotServed identifies a request that names no source when none of the
// configured sources, or no source at all, serves the requested mode. How the deployment is
// configured, not something a retry or another request of the same kind can change.
const StreamErrorCodeSourceNotServed = "flow_source_not_served"

// StreamErrorCodeSourceUnsupported identifies a request naming a flow source that is not
// configured or does not have the capability the request needs. It is raised before dialing and
// never falls back to another source: live data must not be substituted for a finite query.
const StreamErrorCodeSourceUnsupported = "flow_source_unsupported"

func dependencyUnavailable(message string) *StreamError {
	return &StreamError{msg: message, Code: StreamErrorCodeSourceUnavailable, Retryable: true}
}

// FlowSource is a named flow source: an endpoint implementing FlowStreamService.
type FlowSource struct {
	// Name identifies the source in requests (source=<name>).
	Name string
	// Watch and List are the capabilities the source serves: live streams (follow=true) and
	// finite queries (follow=false).
	Watch, List bool
	Subscriber  FlowStreamSubscriber
}

func (s *FlowSource) supports(follow bool) bool {
	if follow {
		return s.Watch
	}
	return s.List
}

// RoutedSubscriber routes each request to one configured flow source, never substituting another
// one.
type RoutedSubscriber struct {
	Sources []FlowSource
}

// resolve selects the source for a request. A named source is matched against the configured
// names only. Without a name, the first source with the needed capability is the default.
func (s *RoutedSubscriber) resolve(filter *FlowStreamFilter) (*FlowSource, error) {
	follow := filter.following()
	needed := "finite queries (follow=false)"
	if follow {
		needed = "live streams (follow=true)"
	}
	unsupported := func(format string, args ...any) error {
		return &StreamError{msg: fmt.Sprintf(format, args...), Code: StreamErrorCodeSourceUnsupported}
	}
	if filter.Source != "" {
		for i := range s.Sources {
			if s.Sources[i].Name != filter.Source {
				continue
			}
			if !s.Sources[i].supports(follow) {
				return nil, unsupported("flow source %q does not support %s", filter.Source, needed)
			}
			return &s.Sources[i], nil
		}
		return nil, unsupported("unknown flow source %q", filter.Source)
	}
	for i := range s.Sources {
		if s.Sources[i].supports(follow) {
			return &s.Sources[i], nil
		}
	}
	return nil, &StreamError{
		msg:  fmt.Sprintf("this deployment does not serve %s: no configured flow source supports it", needed),
		Code: StreamErrorCodeSourceNotServed,
	}
}

func (s *RoutedSubscriber) Subscribe(ctx context.Context, scope *FlowStreamScope, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{}) {
	flows := make(chan apisv1.FlowStreamEvent)
	errs := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		defer close(errs)
		defer close(flows)
		source, err := s.resolve(filter)
		if err != nil {
			errs <- err
			return
		}
		announced := false
		var count uint64
		err = relay(ctx, source.Subscriber, scope, filter, func() error {
			if !announced {
				close(ready)
				announced = true
			}
			return nil
		}, func(event apisv1.FlowStreamEvent) error {
			if !filter.following() {
				batchCount := uint64(len(event.Flows))
				if batchCount > uint64(filter.resultLimit())-count {
					return internalStreamError(fmt.Errorf("flow source %q exceeded the requested result limit", source.Name))
				}
				count += batchCount
			}
			select {
			case flows <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil && ctx.Err() == nil {
			errs <- err
		}
	}()
	return flows, errs, ready
}

// Close closes the sources which hold connections.
func (s *RoutedSubscriber) Close() error {
	var errs []error
	for _, source := range s.Sources {
		if closer, ok := source.Subscriber.(io.Closer); ok {
			errs = append(errs, closer.Close())
		}
	}
	return errors.Join(errs...)
}

// relay drains both channels before declaring success. A final buffered error
// must never be lost to a simultaneous close of the flow channel.
func relay(ctx context.Context, upstream FlowStreamSubscriber, scope *FlowStreamScope, filter *FlowStreamFilter, ready func() error, send func(apisv1.FlowStreamEvent) error) error {
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
