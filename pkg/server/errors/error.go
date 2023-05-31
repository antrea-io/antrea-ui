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

package errors

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"
)

type ServerError struct {
	Code    int
	Err     error
	Message string
}

func HandleError(c *gin.Context, sError *ServerError) {
	if sError == nil {
		panic("ServerError is nil")
	}
	if sError.Err != nil {
		c.Error(sError.Err)
	}
	if sError.Code == http.StatusInternalServerError {
		c.JSON(sError.Code, "Internal Server Error")
	} else {
		c.JSON(sError.Code, sError.Message)
	}
}

func LogError(logger logr.Logger, sError *ServerError, msg string, keysAndValues ...interface{}) {
	if sError == nil || sError.Err == nil {
		return
	}
	logger.Error(sError.Err, msg, keysAndValues...)
}
