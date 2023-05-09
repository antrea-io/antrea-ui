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
		req := httptest.NewRequest("PUT", "/api/v1/account/password", bytes.NewReader(b))
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
