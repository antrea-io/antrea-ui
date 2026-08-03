// Copyright 2024 Antrea Authors.
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

package antreasvc

import (
	"context"
	"io"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type RequestsHandler interface {
	// Request forwards a request to the Antrea Service as the end user behind ctx, and returns
	// the response body along with the upstream status code. Callers must keep an upstream 401
	// (rejected credential, session is dead) distinct from a 403 (authorization failure, which
	// must not end the session).
	Request(ctx context.Context, method string, path string, body io.Reader) ([]byte, int, error)
}
