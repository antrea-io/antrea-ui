// Copyright 2026 Antrea Authors.
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

package flowstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func TestParseFlowStreamFilter(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		expected    *apisv1.FlowStreamFilter
		expectError bool
	}{
		{
			name:  "empty query defaults",
			query: "",
			expected: &apisv1.FlowStreamFilter{
				Follow: true,
			},
		},
		{
			name:  "namespaces comma-separated",
			query: "namespaces=default,kube-system",
			expected: &apisv1.FlowStreamFilter{
				Namespaces: []string{"default", "kube-system"},
				Follow:     true,
			},
		},
		{
			name:  "pods and services",
			query: "pods=pod-a,pod-b&services=svc-a",
			expected: &apisv1.FlowStreamFilter{
				PodNames:     []string{"pod-a", "pod-b"},
				ServiceNames: []string{"svc-a"},
				Follow:       true,
			},
		},
		{
			name:  "flowTypes parsed as ints",
			query: "flowTypes=1,2",
			expected: &apisv1.FlowStreamFilter{
				FlowTypes: []apisv1.FlowType{apisv1.FlowTypeIntraNode, apisv1.FlowTypeInterNode},
				Follow:    true,
			},
		},
		{
			name:        "invalid flowType returns error",
			query:       "flowTypes=abc",
			expectError: true,
		},
		{
			name:  "follow=false",
			query: "follow=false",
			expected: &apisv1.FlowStreamFilter{
				Follow: false,
			},
		},
		{
			name:  "follow unknown value defaults to true",
			query: "follow=anything",
			expected: &apisv1.FlowStreamFilter{
				Follow: true,
			},
		},
		{
			name:  "follow empty value means true",
			query: "follow=",
			expected: &apisv1.FlowStreamFilter{
				Follow: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/flows/stream?"+tt.query, nil)

			filter, err := parseFlowStreamFilter(c)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, filter)
		})
	}
}

// stubFlowStreamHandler is a minimal FlowStreamHandler for testing the SSE handler.
type stubFlowStreamHandler struct {
	events []apisv1.FlowStreamEvent
	err    error
}

func (s *stubFlowStreamHandler) Subscribe(_ context.Context, _ *apisv1.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent, len(s.events)+1)
	errCh := make(chan error, 1)

	if s.err != nil {
		errCh <- s.err
	} else {
		for _, e := range s.events {
			flowsCh <- e
		}
		close(flowsCh)
	}
	// When err is set, leave flowsCh open so the select picks up errCh first.

	return flowsCh, errCh
}

func newTestRouter(handler *SSEHandler) *gin.Engine {
	router := gin.New()
	router.GET("/api/v1/flows/stream", handler.StreamFlows)
	return router
}

func TestStreamFlowsHappyPath(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamHandler{
		events: []apisv1.FlowStreamEvent{
			{
				Flows: []apisv1.Flow{
					{
						ID:      "flow-1",
						StartTs: "2026-03-25T00:00:00Z",
						EndTs:   "2026-03-25T00:01:00Z",
						IP: apisv1.FlowIP{
							Version:     apisv1.IPVersionIPv4,
							Source:      "10.0.0.1",
							Destination: "10.0.0.2",
						},
						Transport: apisv1.FlowTransport{
							ProtocolNumber:  6,
							SourcePort:      12345,
							DestinationPort: 80,
						},
					},
				},
			},
		},
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?follow=false")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	scanner := bufio.NewScanner(resp.Body)
	var foundFlowEvent bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			var event apisv1.FlowStreamEvent
			err := json.Unmarshal([]byte(data), &event)
			require.NoError(t, err)
			assert.Len(t, event.Flows, 1)
			assert.Equal(t, "flow-1", event.Flows[0].ID)
			foundFlowEvent = true
		}
	}
	require.NoError(t, scanner.Err())
	assert.True(t, foundFlowEvent, "expected at least one flow event in SSE stream")
}

func TestStreamFlowsErrorPath(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamHandler{
		err: fmt.Errorf("upstream connection lost"),
	}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?follow=false")
	require.NoError(t, err)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var body strings.Builder
	for scanner.Scan() {
		body.WriteString(scanner.Text())
		body.WriteString("\n")
	}
	require.NoError(t, scanner.Err())

	assert.Contains(t, body.String(), "event:error")
	assert.Contains(t, body.String(), "upstream connection lost")
}

func TestStreamFlowsBadFilter(t *testing.T) {
	logger := testr.New(t)
	stub := &stubFlowStreamHandler{}

	sseHandler := NewSSEHandler(logger, stub)
	ts := httptest.NewServer(newTestRouter(sseHandler))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/flows/stream?flowTypes=abc")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
