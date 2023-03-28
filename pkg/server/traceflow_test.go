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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	traceflowhandler "antrea.io/antrea-ui/pkg/handlers/traceflow"
)

func TestTraceflowRequest(t *testing.T) {
	tf := map[string]interface{}{
		"spec": map[string]interface{}{
			"source": map[string]interface{}{
				"namespace": "default",
				"pod":       "podX",
			},
			"destination": map[string]interface{}{
				"namespace": "default",
				"pod":       "podY",
			},
		},
	}
	tfJSON, err := json.Marshal(tf)
	require.NoError(t, err)

	ts := newTestServer(t)

	// create traceflow request
	req, err := http.NewRequest("POST", "/api/v1/traceflow", bytes.NewBuffer(tfJSON))
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	requestID := uuid.NewString()
	ts.traceflowRequestsHandler.EXPECT().CreateRequest(gomock.Any(), &traceflowhandler.Request{
		Object: tf,
	}).Return(requestID, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)
	resp := rr.Result()
	url, err := resp.Location()
	require.NoError(t, err)
	reqURI := url.RequestURI()

	// get request: should give a 303 redirect to /status endpoint
	req, err = http.NewRequest("GET", reqURI, nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusSeeOther, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)
	statusURI := url.RequestURI()
	assert.Equal(t, reqURI+"/status", statusURI)

	// get status: not ready yet
	req, err = http.NewRequest("GET", statusURI, nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), requestID).Return(tf, false, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)
	assert.Equal(t, statusURI, url.RequestURI())

	tfResult := map[string]interface{}{
		"spec": tf["spec"],
		"status": map[string]interface{}{
			"phase": "Succeeded",
		},
	}

	// get status: ready
	req, err = http.NewRequest("GET", statusURI, nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), requestID).Return(tfResult, true, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)
	resultURI := url.RequestURI()
	assert.Equal(t, reqURI+"/result", resultURI)

	// get result
	req, err = http.NewRequest("GET", resultURI, nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), requestID).Return(tfResult, true, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
	assert.Equal(t, tfResult, result)

	// delete request
	req, err = http.NewRequest("DELETE", reqURI, nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().DeleteRequest(gomock.Any(), requestID).Return(true, nil)
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
