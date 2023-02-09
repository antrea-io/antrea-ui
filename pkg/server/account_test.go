package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
)

func TestUpdatePassword(t *testing.T) {
	sendAuthorizedRequest := func(ts *testServer, body any) *httptest.ResponseRecorder {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest("PUT", "/api/v1/account/password", bytes.NewBuffer(b))
		require.NoError(t, err)
		ts.authorizeRequest(req)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	currentPassword := []byte("foo")
	newPassword := []byte("bar")
	wrongPassword := []byte("abc")

	t.Run("valid update", func(t *testing.T) {
		ts := newTestServer(t)
		gomock.InOrder(
			ts.passwordStore.EXPECT().Compare(gomock.Any(), currentPassword),
			ts.passwordStore.EXPECT().Update(gomock.Any(), newPassword),
		)
		rr := sendAuthorizedRequest(ts, &apisv1alpha1.UpdatePassword{
			CurrentPassword: currentPassword,
			NewPassword:     newPassword,
		})
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("wrong password", func(t *testing.T) {
		ts := newTestServer(t)
		ts.passwordStore.EXPECT().Compare(gomock.Any(), wrongPassword).Return(fmt.Errorf("bad password"))
		rr := sendAuthorizedRequest(ts, &apisv1alpha1.UpdatePassword{
			CurrentPassword: wrongPassword,
			NewPassword:     newPassword,
		})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("invalid request format", func(t *testing.T) {
		ts := newTestServer(t)
		rr := sendAuthorizedRequest(ts, "hello")
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}
