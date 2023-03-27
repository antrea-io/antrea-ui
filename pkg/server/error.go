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
	"net/http"

	"github.com/gin-gonic/gin"
)

type serverError struct {
	code    int
	err     error
	message string
}

func (s *server) HandleError(c *gin.Context, sError *serverError) {
	if sError == nil {
		panic("serverError is nil")
	}
	if sError.err != nil {
		c.Error(sError.err)
	}
	if sError.code == http.StatusInternalServerError {
		c.JSON(sError.code, "Internal Server Error")
	} else {
		c.JSON(sError.code, sError.message)
	}
}

func (s *server) LogError(sError *serverError, msg string, keysAndValues ...interface{}) {
	if sError == nil || sError.err == nil {
		return
	}
	s.logger.Error(sError.err, msg, keysAndValues...)
}
