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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// bearerSessionCtx builds a context carrying a bearer-credential session, which is the cheapest
// credential shape for these tests: the probe needs a resolvable identity, and which one it is
// does not matter to it.
func bearerSessionCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, _ := ctxWithSessionAuth(t, newTestStore(t), &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	return ctx
}

// subscribeAndWait opens a stream and returns the first error it reports, or nil if the stream
// ended without one.
func subscribeAndWait(t *testing.T, h *GRPCFlowStreamSubscriber, ctx context.Context) error {
	t.Helper()
	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		if !ok {
			return nil
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the stream to report")
		return nil
	}
}

// A Flow Aggregator that enforces the mutual exclusion of cluster_wide and namespaces is one that
// knows both fields, so it authorizes per user and redacts per endpoint. The real request goes out
// and the stream runs normally.
func TestVersionProbeAcceptsModernFlowAggregator(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	require.NoError(t, subscribeAndWait(t, h, ctx))
	assert.Equal(t, int32(1), fake.probeCalls.Load())
	assert.Equal(t, int32(1), fake.calls.Load(), "the real request must have been sent")
}

// A Flow Aggregator that predates the fields parses both into unknown fields, ignores them, and
// (with Follow false and an empty ring buffer) closes the stream immediately. It would answer the
// real request with every flow it has, unredacted, and nothing in the response would say so - so
// the only safe thing is to refuse before the real request is ever sent.
func TestVersionProbeRefusesOldFlowAggregator(t *testing.T) {
	fake := &fakeFlowStreamServer{
		// Returning nil is exactly the pre-authorization behaviour: no error, no records,
		// stream closed.
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error { return nil },
		handle:      func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	h.address = "flow-aggregator.flow-aggregator.svc:14680"
	ctx := bearerSessionCtx(t)

	err := subscribeAndWait(t, h, ctx)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	assert.Equal(t, StreamErrorCodeFlowAggregatorTooOld, streamErr.Code)
	assert.False(t, streamErr.Retryable, "an old Flow Aggregator is terminal until it is upgraded")
	// An operator's next question is which component to upgrade, so the address has to be in
	// the message.
	assert.Contains(t, streamErr.Error(), "flow-aggregator.flow-aggregator.svc:14680")
	assert.Zero(t, fake.calls.Load(), "the real request must never have been sent")
}

// A record followed by EOF is the same verdict as an immediate close: both mean the scope fields
// were ignored. The probe sets MaxCount 1, so one record is the most an old server can send.
func TestVersionProbeRefusesOldFlowAggregatorThatReturnsARecord(t *testing.T) {
	fake := &fakeFlowStreamServer{
		handle: func(_ int, _ string) error { return nil },
	}
	// An old Flow Aggregator answers the probe with up to one historical record (MaxCount 1)
	// and then closes the stream, having ignored the scope fields entirely.
	fake.probeHandle = func(_ int, stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
		return stream.Send(&flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{{Id: "historical"}}})
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	err := subscribeAndWait(t, h, ctx)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	assert.Equal(t, StreamErrorCodeFlowAggregatorTooOld, streamErr.Code)
}

// Unauthenticated from the probe says nothing about the server's version: it is about the
// credential. It must surface as such, and must not be cached as a verdict - otherwise one user
// with a bad credential would take flow visibility down for everyone else for the whole TTL.
func TestVersionProbeDoesNotTreatUnauthenticatedAsAVersionVerdict(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(probeNum int, _ grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			if probeNum == 1 {
				return status.Error(codes.Unauthenticated, "no credential")
			}
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	err := subscribeAndWait(t, h, ctx)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	assert.Equal(t, StreamErrorCodeUnauthenticated, streamErr.Code)
	assert.NotEqual(t, StreamErrorCodeFlowAggregatorTooOld, streamErr.Code)

	// Nothing was cached, so the next caller probes again - and a caller whose credential the
	// Flow Aggregator does accept gets a stream.
	require.NoError(t, subscribeAndWait(t, h, ctx))
	assert.Equal(t, int32(2), fake.probeCalls.Load())
}

// The verdict is cached, so the probe is not a per-stream-open cost. Opens are frequent: the page
// reopens the stream on every filter change and unpause, and every reconnect attempt is another
// open.
func TestVersionProbeVerdictIsCached(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	for range 3 {
		require.NoError(t, subscribeAndWait(t, h, ctx))
	}
	assert.Equal(t, int32(1), fake.probeCalls.Load(), "the verdict must be cached across opens")
	assert.Equal(t, int32(3), fake.calls.Load())
}

// A cached "too old" verdict is answered without a probe too, so a deployment that cannot support
// flow visibility is not billed one probe per reconnect attempt.
func TestVersionProbeCachesTheTooOldVerdict(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error { return nil },
		handle:      func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	for range 3 {
		err := subscribeAndWait(t, h, ctx)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeFlowAggregatorTooOld, streamErr.Code)
	}
	assert.Equal(t, int32(1), fake.probeCalls.Load())
	assert.Zero(t, fake.calls.Load())
}

// An expired verdict is re-probed rather than trusted forever, which is what bounds how long a
// Flow Aggregator upgraded under a running antrea-ui stays refused.
func TestVersionProbeReprobesAfterTTL(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	require.NoError(t, subscribeAndWait(t, h, ctx))
	require.Equal(t, int32(1), fake.probeCalls.Load())

	// Age the cached verdict past its TTL rather than waiting ten minutes for it.
	h.versionProbe.mutex.Lock()
	h.versionProbe.expiresAt = time.Now().Add(-time.Second)
	h.versionProbe.mutex.Unlock()

	require.NoError(t, subscribeAndWait(t, h, ctx))
	assert.Equal(t, int32(2), fake.probeCalls.Load())
}

// Concurrent first opens - a page load that starts several streams, or several users arriving at
// once - must cost one probe between them, not one each.
func TestVersionProbeIsSingleFlighted(t *testing.T) {
	const openers = 8
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			// Hold the leader inside the probe until every caller has had a chance to
			// reach checkFlowAggregatorVersion, so a per-caller probe would be observable
			// as a second probeCalls increment.
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-release
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(_ int, _ string) error { return nil },
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	var wg sync.WaitGroup
	errs := make([]error, openers)
	for i := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = subscribeAndWait(t, h, ctx)
		}()
	}
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the probe to be reached")
	}
	// Give the followers time to pile up behind the leader; without this the test could pass
	// simply because they had not started yet.
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.Equal(t, int32(1), fake.probeCalls.Load(), "concurrent opens must share one probe")
	assert.Equal(t, int32(openers), fake.calls.Load(), "every caller must still get its own stream")
}

// A real stream failing with InvalidArgument drops the cached verdict, so the next open re-probes
// instead of waiting out the TTL. parseFlowStreamScope's local 400s should make this unreachable,
// which is exactly why it is worth pinning: the invalidation is the recovery path for a
// client-visible disagreement about which fields the Flow Aggregator enforces.
func TestRealStreamInvalidArgumentInvalidatesTheVersionCache(t *testing.T) {
	fake := &fakeFlowStreamServer{
		probeHandle: func(int, grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
			return status.Error(codes.InvalidArgument, "namespaces and cluster_wide are mutually exclusive")
		},
		handle: func(callNum int, _ string) error {
			if callNum == 1 {
				return status.Error(codes.InvalidArgument, "name the namespaces to observe flows in")
			}
			return nil
		},
	}
	h := newTestSubscriber(t, fake)
	ctx := bearerSessionCtx(t)

	err := subscribeAndWait(t, h, ctx)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	require.Equal(t, StreamErrorCodeInvalidRequest, streamErr.Code)
	require.Equal(t, int32(1), fake.probeCalls.Load())

	supported, ok := h.versionProbe.cached()
	assert.False(t, ok, "the cached verdict must have been dropped")
	assert.False(t, supported)

	// And the next open re-probes rather than trusting a verdict that has just been
	// contradicted.
	require.NoError(t, subscribeAndWait(t, h, ctx))
	assert.Equal(t, int32(2), fake.probeCalls.Load())
}

// The probe must not be reached at all when no credential can be resolved: that is a wiring bug,
// and the probe has nothing to present.
func TestVersionProbeIsNotReachedWithoutAnIdentity(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)

	_, errCh, _ := h.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.probeCalls.Load())
	assert.Zero(t, fake.calls.Load())
}
