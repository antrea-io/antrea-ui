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
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// versionProbeTTL is how long a verdict about the Flow Aggregator's FlowStreamService is trusted
// before it is probed again. Ten minutes matches the lifetime upstream gives its own
// authorization caches, and bounds how long a Flow Aggregator upgraded under a running antrea-ui
// stays refused.
const versionProbeTTL = 10 * time.Minute

// probeNamespace is the Namespace the probe names alongside cluster_wide. Its value is irrelevant
// - the request is rejected before the Namespace is looked at or authorized - but naming one that
// exists in every cluster keeps the request from looking like a typo in a server-side log.
const probeNamespace = "kube-system"

// flowAggregatorTooOldErr builds the error a caller gets when the Flow Aggregator predates
// per-user flow authorization. It names the address, because the operator's next question is
// which component to upgrade.
func flowAggregatorTooOldErr(address string) *StreamError {
	return &StreamError{
		msg: fmt.Sprintf("the Flow Aggregator at %s does not support per-user flow authorization; "+
			"flow visibility requires a Flow Aggregator built with that support "+
			"(see antrea-io/antrea#8221)", address),
		Code:      StreamErrorCodeFlowAggregatorTooOld,
		Retryable: false,
	}
}

// versionProbe caches whether the Flow Aggregator supports per-user flow authorization, and
// collapses concurrent first opens onto a single probe.
//
// Kept on the subscriber rather than in a package-level variable: there is one subscriber per
// process, so the effect is the same, and a verdict about one Flow Aggregator address does not
// leak into a test (or a future deployment) talking to another.
type versionProbe struct {
	mutex sync.Mutex
	// valid is false when there is no verdict to trust, either because none has been reached
	// or because one was invalidated. supported and expiresAt are only meaningful when it is
	// true.
	valid     bool
	supported bool
	expiresAt time.Time
	// inflight is non-nil while a probe is running, and is closed when it finishes. Followers
	// wait on it instead of issuing a probe of their own, and then re-read the cache. This is
	// not singleflight.Group because a follower must not inherit the leader's error: the
	// leader's Unauthenticated is about the leader's own credential, and every caller here
	// presents a different one.
	inflight chan struct{}
}

// invalidate drops any cached verdict, so the next caller probes again.
func (p *versionProbe) invalidate() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.valid = false
}

// cached returns the cached verdict, if there is an unexpired one.
func (p *versionProbe) cached() (supported bool, ok bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if !p.valid || !time.Now().Before(p.expiresAt) {
		return false, false
	}
	return p.supported, true
}

// store records a verdict and starts its TTL.
func (p *versionProbe) store(supported bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.valid = true
	p.supported = supported
	p.expiresAt = time.Now().Add(versionProbeTTL)
}

// checkFlowAggregatorVersion reports nil when the Flow Aggregator behind client supports per-user
// flow authorization, and a StreamError otherwise.
//
// Subscribe must call this before sending the real request: a Flow Aggregator that predates
// per-user authorization parses cluster_wide and namespaces into unknown fields and ignores them,
// then streams every flow it has with no disclosure markers on any record. A client reads a
// missing marker as Full - correctly, since that is the enum's zero value - so the result is a UI
// that says it is observing one Namespace while showing the whole cluster at full identity.
// Nothing in the response distinguishes that from a legitimate cluster-wide stream, so the only
// place it can be caught is before the request goes out.
//
// The probe is lazy rather than done at startup: at startup there may be no credential to present,
// and an unreachable Flow Aggregator should not delay boot.
func (h *GRPCFlowStreamSubscriber) checkFlowAggregatorVersion(ctx context.Context, client flowpb.FlowStreamServiceClient, callCtx context.Context) error {
	for {
		if supported, ok := h.versionProbe.cached(); ok {
			if supported {
				return nil
			}
			return flowAggregatorTooOldErr(h.address)
		}

		h.versionProbe.mutex.Lock()
		if wait := h.versionProbe.inflight; wait != nil {
			h.versionProbe.mutex.Unlock()
			// Wait for the leader and then re-read the cache. If the leader failed
			// without reaching a verdict (its credential was rejected), the cache is
			// still empty and this caller becomes the next leader with its own
			// credential, which may well be accepted.
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return errProbeCanceled
			}
		}
		done := make(chan struct{})
		h.versionProbe.inflight = done
		h.versionProbe.mutex.Unlock()

		supported, err := h.probeFlowAggregatorVersion(ctx, client, callCtx)
		if err == nil {
			// Only a real verdict is cached. An error - notably the probe's own
			// Unauthenticated, which says nothing about the server's version - is not.
			h.versionProbe.store(supported)
		}
		h.versionProbe.mutex.Lock()
		h.versionProbe.inflight = nil
		h.versionProbe.mutex.Unlock()
		close(done)

		if err != nil {
			return err
		}
		if !supported {
			return flowAggregatorTooOldErr(h.address)
		}
		return nil
	}
}

// probeFlowAggregatorVersion sends one request that a Flow Aggregator with per-user flow
// authorization must reject and an older one cannot even parse, and reports whether the server
// supports it.
//
// FlowStreamService has no version or capability RPC, but it does not need one: setting both
// cluster_wide and namespaces is a combination upstream rejects with INVALID_ARGUMENT
// ("namespaces and cluster_wide are mutually exclusive"), while a server that predates the fields
// parses both into unknown fields, ignores them, and answers with up to one historical record
// followed by EOF. Three properties make that reliable, and each is load-bearing:
//
//   - Permission-independent. Upstream validates the requested Namespaces inside its stream
//     authorization, before any SubjectAccessReview, so the INVALID_ARGUMENT comes back for any
//     authenticated caller whatever RBAC they hold. The probe needs no privilege.
//   - Self-terminating. Follow is false, so an older Flow Aggregator closes the stream on its own
//     (it closes immediately on !follow && n == 0). Nothing to cancel, no dangling stream.
//   - Unambiguous. With no filters, INVALID_ARGUMENT cannot have come from filter parsing, which
//     is the only other source of that code on this path.
//
// A returned error is neither verdict: Unauthenticated in particular is about the credential, not
// the server's version, and is passed straight through so it reaches the caller as
// StreamErrorCodeUnauthenticated.
func (h *GRPCFlowStreamSubscriber) probeFlowAggregatorVersion(ctx context.Context, client flowpb.FlowStreamServiceClient, callCtx context.Context) (bool, error) {
	req := &flowpb.GetFlowsRequest{
		ClusterWide: true,
		Namespaces:  []string{probeNamespace},
		Follow:      false,
		MaxCount:    1,
		Filters:     nil,
	}
	stream, err := client.GetFlows(callCtx, req)
	if err == nil {
		// As with startStream, an error the server returns before sending anything surfaces
		// on the first Recv rather than on the call that opens the stream.
		_, err = stream.Recv()
	}
	switch {
	case err == nil, errors.Is(err, io.EOF):
		// A record, or an immediately-closed stream: both fields were ignored, so this
		// server predates them.
		return false, nil
	case status.Code(err) == codes.InvalidArgument:
		// The mutual exclusion was enforced, which only a server that knows the fields can
		// do.
		return true, nil
	case ctx.Err() != nil || status.Code(err) == codes.Canceled:
		// An ordinary client disconnect during the probe. Report it as unsupported-unknown
		// with no error; Subscribe's caller is going away anyway.
		return false, errProbeCanceled
	default:
		h.logger.Error(err, "Failed to probe the Flow Aggregator for per-user flow authorization support")
		return false, h.classifyStreamErr(err)
	}
}

// errProbeCanceled means the probe ended because the request it was made for did, which is not a
// stream failure. Subscribe drops it rather than reporting it, the same way startStream drops a
// Canceled first Recv.
var errProbeCanceled = errors.New("flow aggregator version probe canceled")
