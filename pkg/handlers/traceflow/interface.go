// Copyright 2023 Antrea Authors.
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

package traceflow

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type RequestsHandler interface {
	CreateRequest(ctx context.Context, request *Request) (string, error)
	// GetRequestResult returns the Traceflow object, and a boolean to indicate whether the
	// Traceflow request is completed. Completed means that the Traceflow Status has either been
	// updated to "Succeeded" or "Failed".
	GetRequestResult(ctx context.Context, requestID string) (map[string]interface{}, bool, error)
	DeleteRequest(ctx context.Context, requestID string) (bool, error)
}
