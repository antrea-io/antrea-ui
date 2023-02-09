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
