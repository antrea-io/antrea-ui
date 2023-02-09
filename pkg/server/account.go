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
		if err := s.passwordStore.Compare(c, []byte(updatePassword.CurrentPassword)); err != nil {
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
