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

package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	"antrea.io/antrea-ui/pkg/server/authn"
	"antrea.io/antrea-ui/pkg/server/errors"
)

func (s *Server) UpdatePassword(c *gin.Context) {
	if sError := func() *errors.ServerError {
		// The admin password is antrea-ui's own credential, not a Kubernetes one, so no RBAC
		// stands behind this endpoint. Restrict it to sessions that authenticated with that
		// password in the first place: a user who logged in with their own Kubernetes
		// identity should not be able to change how everyone else logs in.
		if ra, ok := authn.RequestAuthFromGin(c); !ok || ra.Mode != session.ModeAdmin {
			return &errors.ServerError{
				Code:    http.StatusForbidden,
				Message: "Only a session authenticated with the admin password can change it",
			}
		}
		var updatePassword apisv1.UpdatePassword
		if err := c.BindJSON(&updatePassword); err != nil {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "invalid body",
			}
		}
		if err := s.passwordStore.Compare(c, updatePassword.CurrentPassword); err != nil {
			return &errors.ServerError{
				Code:    http.StatusBadRequest,
				Message: "Invalid current admin password",
			}
		}
		if err := s.passwordStore.Update(c, updatePassword.NewPassword); err != nil {
			return &errors.ServerError{
				Code: http.StatusInternalServerError,
				Err:  fmt.Errorf("error when updating password: %w", err),
			}
		}
		return nil
	}(); sError != nil {
		errors.HandleError(c, sError)
		s.LogError(sError, "Failed to update password")
		return
	}
	// 200 and not 202 because all processing is synchronous (this could change later)
	c.Status(http.StatusOK)
}

func (s *Server) AddAccountRoutes(r *gin.RouterGroup) {
	// Without basic auth there is no admin password and no password store to talk to.
	if s.passwordStore == nil {
		return
	}
	r = r.Group("/account")
	r.Use(s.authenticate())
	r.PUT("/password", s.UpdatePassword)
}
