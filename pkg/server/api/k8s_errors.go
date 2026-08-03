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

package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/dynamic"

	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

// dynamicClientFor builds a dynamic client that acts as the user behind this request.
func (s *Server) dynamicClientFor(c *gin.Context) (dynamic.Interface, *errors.ServerError) {
	client, err := s.clientFactory.DynamicClientForRequest(c.Request.Context())
	if err != nil {
		return nil, &errors.ServerError{
			Code: http.StatusInternalServerError,
			Err:  fmt.Errorf("failed to build K8s client for request: %w", err),
		}
	}
	return client, nil
}

// k8sError maps an error from a Kubernetes API call into a response for the UI.
//
// The 401/403 distinction is the important part. The API server answers 401 when the credential
// itself is rejected, which means the session is finished, and 403 when the credential is fine but
// the user is not allowed to do this, which is an ordinary and recoverable outcome now that
// authorization is per-user. Conflating them would log a user out every time they hit a
// permissions error.
func (s *Server) k8sError(c *gin.Context, err error, message string) *errors.ServerError {
	switch {
	case apierrors.IsUnauthorized(err):
		if ra, ok := authn.RequestAuthFromGin(c); ok {
			ra.Invalidate()
			s.authenticator.ClearSessionCookie(c)
		}
		return &errors.ServerError{
			Code:    http.StatusUnauthorized,
			Message: "Session is no longer valid, please log in again",
		}
	case apierrors.IsForbidden(err):
		return &errors.ServerError{
			Code:    http.StatusForbidden,
			Message: err.Error(),
		}
	case apierrors.IsNotFound(err):
		return &errors.ServerError{
			Code:    http.StatusNotFound,
			Message: err.Error(),
		}
	default:
		return &errors.ServerError{
			Code: http.StatusInternalServerError,
			Err:  fmt.Errorf("%s: %w", message, err),
		}
	}
}

// upstreamStatusError maps a status code returned by the Antrea Service (which delegates
// authn/authz to Kubernetes) the same way k8sError maps a client-go error.
func (s *Server) upstreamStatusError(c *gin.Context, statusCode int, body []byte) *errors.ServerError {
	switch statusCode {
	case http.StatusUnauthorized:
		// The handler has already invalidated the session; clear the cookie too.
		s.authenticator.ClearSessionCookie(c)
		return &errors.ServerError{
			Code:    http.StatusUnauthorized,
			Message: "Session is no longer valid, please log in again",
		}
	case http.StatusForbidden:
		return &errors.ServerError{
			Code:    http.StatusForbidden,
			Message: string(body),
		}
	default:
		return &errors.ServerError{
			Code: http.StatusInternalServerError,
			Err:  fmt.Errorf("unexpected status %d from Antrea Service", statusCode),
		}
	}
}
