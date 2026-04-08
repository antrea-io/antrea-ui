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

// FlowStreamHandler provides a channel-based interface for streaming flow data.
// Implementations connect to the FlowAggregator's gRPC FlowStreamService (see grpc.go)
// and relay flow events to the caller.
type FlowStreamHandler interface {
	// Subscribe starts streaming flows matching the given filter.
	// It returns a channel of FlowStreamEvent and a channel of errors.
	// The caller should read from both channels until they are closed.
	// Cancel the context to stop the stream.
	Subscribe(ctx context.Context, filter *apisv1.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error)
}
