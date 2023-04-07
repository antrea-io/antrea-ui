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

package server

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
)

func (s *server) UpdatePassword(c *gin.Context) {
	if sError := func() *serverError {
		var updatePassword apisv1alpha1.UpdatePassword
		if err := c.BindJSON(&updatePassword); err != nil {
			return &serverError{
				code:    http.StatusBadRequest,
				message: "invalid body",
			}
		}
		if err := s.passwordStore.Compare(c, updatePassword.CurrentPassword); err != nil {
			return &serverError{
				code:    http.StatusBadRequest,
				message: "Invalid current admin password",
			}
		}
		if err := s.passwordStore.Update(c, updatePassword.NewPassword); err != nil {
			return &serverError{
				code: http.StatusInternalServerError,
				err:  fmt.Errorf("error when updating password: %w", err),
			}
		}
		return nil
	}(); sError != nil {
		s.HandleError(c, sError)
		s.LogError(sError, "Failed to update password")
		return
	}
	// 200 and not 202 because all processing is synchronous (this could change later)
	c.Status(http.StatusOK)
}

func (s *server) AddAccountRoutes(r *gin.RouterGroup) {
	r = r.Group("/account")
	r.Use(s.checkBearerToken)
	r.PUT("/password", s.UpdatePassword)
}
