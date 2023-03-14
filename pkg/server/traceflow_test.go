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

	// get status: not ready yet
	req, err = http.NewRequest("GET", url.RequestURI(), nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestStatus(gomock.Any(), requestID).Return(&traceflowhandler.RequestStatus{
		Done: false,
		Err:  nil,
	}, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)

	// get status: ready
	req, err = http.NewRequest("GET", url.RequestURI(), nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().GetRequestStatus(gomock.Any(), requestID).Return(&traceflowhandler.RequestStatus{
		Done: true,
		Err:  nil,
	}, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	resp = rr.Result()
	url, err = resp.Location()
	require.NoError(t, err)

	// get result
	req, err = http.NewRequest("GET", url.RequestURI(), nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	tfResult := map[string]interface{}{
		"spec": tf["spec"],
		"status": map[string]interface{}{
			"phase": "Succeeded",
		},
	}
	ts.traceflowRequestsHandler.EXPECT().GetRequestResult(gomock.Any(), requestID).Return(tfResult, nil)
	ts.router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
	assert.Equal(t, tfResult, result)

	// delete request
	req, err = http.NewRequest("DELETE", url.RequestURI(), nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr = httptest.NewRecorder()
	ts.traceflowRequestsHandler.EXPECT().DeleteRequest(gomock.Any(), requestID).Return(true, nil)
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
