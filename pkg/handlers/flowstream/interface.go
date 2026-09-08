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

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

// FlowStreamSubscriber provides a channel-based interface for streaming flow data.
// Implementations connect to the FlowAggregator's gRPC FlowStreamService (see grpc.go)
// and relay flow events to the caller.
type FlowStreamSubscriber interface {
	// Subscribe starts streaming flows matching the given filter.
	// It returns a channel of FlowStreamEvent, a channel of errors, and a channel that closes
	// once the stream is confirmed live - i.e. the upstream call cleared authentication and
	// authorization and is ready to serve flows, even if none have arrived yet. A caller
	// deciding when it is safe to commit to a response (see StreamFlows) can wait on the ready
	// channel instead of guessing how long that takes; it is never closed on a failure path,
	// since errCh already reports those.
	// The caller should read from all three until flowsCh and errCh are closed.
	// Cancel the context to stop the stream.
	//
	// ctx must carry the request's *session.RequestAuth (see session.WithRequestAuth): the
	// GRPCFlowStreamSubscriber implementation reads it to decide which credential to present
	// to the Flow Aggregator.
	Subscribe(ctx context.Context, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error, <-chan struct{})
}
