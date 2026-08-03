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

package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"antrea.io/antrea-ui/pkg/server/errors"
)

// copied from https://github.com/antrea-io/antrea/blob/6507578dbce95808629d1dd62ece34fb0a38c86d/pkg/apiserver/handlers/featuregates/handler.go#L40
type featureGate struct {
	Component string `json:"component,omitempty"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status,omitempty"`
	Version   string `json:"version,omitempty"`
}

func (s *Server) GetFeatureGates(c *gin.Context) {
	if sError := func() *errors.ServerError {
		// c.Request.Context(), not c: the handler reads the resolved identity back out with
		// session.RequestAuthFrom, which keys off an unexported type. A *gin.Context only
		// forwards Value() lookups to the request context when Engine.ContextWithFallback is
		// set, which it is not, so passing c here would lose the identity entirely.
		b, statusCode, err := s.antreaSvcRequestsHandler.Request(c.Request.Context(), "GET", "/featuregates", nil)
		if err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  err,
			}
		}
		if statusCode != http.StatusOK {
			return s.upstreamStatusError(c, statusCode, b)
		}
		// We return the API response as it to the client, with no transformation.
		// But first, we validate that it is what we expect.
		var resp []featureGate
		if err := json.Unmarshal(b, &resp); err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("unexpected response from Antrea service: %w", err),
			}
		}
		c.Data(http.StatusOK, "application/json", b)
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to get feature gates")
	}
}
