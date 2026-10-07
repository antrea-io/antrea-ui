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
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	traceflowhandler "antrea.io/antrea-ui/pkg/handlers/traceflow"
)

func mustMarshal(obj any) []byte {
	b, err := json.Marshal(obj)
	if err != nil {
		panic("Failed to marshal object to JSON")
	}
	return b
}

var (
	tf = map[string]any{
		"spec": map[string]any{
			"source": map[string]any{
				"namespace": "default",
				"pod":       "podX",
			},
			"destination": map[string]any{
				"namespace": "default",
				"pod":       "podY",
			},
		},
	}

	tfJSON = mustMarshal(tf)
)

func TestTraceflowRequest(t *testing.T) {
	ts := newTestServer(t)

	// create traceflow request
	req := httptest.NewRequest("POST", "/api/v1/traceflow", bytes.NewReader(tfJSON))
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	requestID := uuid.New().String()
	ts.traceflowRequestsHandler.EXPECT().CreateRequest(gomock.Any(), gomock.Any(), &traceflowhandler.Request{
		Object: tf,
	}).Return(requestID, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)
	resp := rr.Result()
	url, err := resp.Location()
	require.NoError(t, err)
	reqURI := url.RequestURI()

	// get request: should give a 303 redirect to /status endpoint
	req = httptest.NewRequest("GET", reqURI, nil)
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
	req = httptest.NewRequest("GET", statusURI, nil)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), gomock.Any(), requestID).Return(tf, false, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)
	assert.Equal(t, statusURI, url.RequestURI())

	tfResult := map[string]any{
		"spec": tf["spec"],
		"status": map[string]any{
			"phase": "Succeeded",
		},
	}

	// get status: ready
	req = httptest.NewRequest("GET", statusURI, nil)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), gomock.Any(), requestID).Return(tfResult, true, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)
	resultURI := url.RequestURI()
	assert.Equal(t, reqURI+"/result", resultURI)

	// get result
	req = httptest.NewRequest("GET", resultURI, nil)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), gomock.Any(), requestID).Return(tfResult, true, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
	assert.Equal(t, tfResult, result)

	// delete request
	req = httptest.NewRequest("DELETE", reqURI, nil)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().DeleteRequest(gomock.Any(), gomock.Any(), requestID).Return(true, nil)
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestTraceflowRequestRateLimiting(t *testing.T) {
	sendRequest := func(ts *testServer) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/traceflow", bytes.NewReader(tfJSON))
		rr := httptest.NewRecorder()
		ts.authorizeRequest(req)
		ts.router.ServeHTTP(rr, req)
		return rr
	}

	t.Run("0/s", func(t *testing.T) {
		ts := newTestServer(t, setMaxTraceflowsPerHour(0))
		rr := sendRequest(ts)
		assert.Equal(t, http.StatusTooManyRequests, rr.Code)
	})

	t.Run("5/s", func(t *testing.T) {
		// The rate limiter reads the current time, and nothing here touches the network: in
		// a bubble, the fake clock makes the burst and the refill exact.
		synctest.Test(t, func(t *testing.T) {
			const (
				// Must match the burst size in AddTraceflowRoutes.
				burstSize      = 10
				refillInterval = time.Second / 5
				// The limiter computes tokens with floating point numbers, so the
				// assertions stay clear of the exact instant a token is added.
				margin = time.Millisecond
			)
			ts := newTestServer(t, setMaxTraceflowsPerHour(5*3600))
			ts.traceflowRequestsHandler.EXPECT().CreateRequest(gomock.Any(), gomock.Any(), &traceflowhandler.Request{
				Object: tf,
			}).Return(uuid.New().String(), nil).AnyTimes()
			for i := range burstSize {
				require.Equalf(t, http.StatusAccepted, sendRequest(ts).Code, "request %d is within the burst", i+1)
			}
			assert.Equal(t, http.StatusTooManyRequests, sendRequest(ts).Code, "the burst is used up")

			time.Sleep(refillInterval - margin)
			assert.Equal(t, http.StatusTooManyRequests, sendRequest(ts).Code, "no token is available yet")
			time.Sleep(2 * margin)
			assert.Equal(t, http.StatusAccepted, sendRequest(ts).Code, "a token is available again")
			assert.Equal(t, http.StatusTooManyRequests, sendRequest(ts).Code, "only one token was added")
		})
	})
}
