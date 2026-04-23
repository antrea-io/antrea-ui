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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// SSEHandler handles the SSE endpoint for flow streaming.
type SSEHandler struct {
	logger  logr.Logger
	handler FlowStreamHandler
}

func NewSSEHandler(logger logr.Logger, handler FlowStreamHandler) *SSEHandler {
	return &SSEHandler{
		logger:  logger,
		handler: handler,
	}
}

func parseFlowStreamFilter(c *gin.Context) (*apisv1.FlowStreamFilter, error) {
	filter := &apisv1.FlowStreamFilter{}

	if ns := c.Query("namespaces"); ns != "" {
		filter.Namespaces = strings.Split(ns, ",")
	}
	if pods := c.Query("pods"); pods != "" {
		filter.PodNames = strings.Split(pods, ",")
	}
	if svcs := c.Query("services"); svcs != "" {
		filter.ServiceNames = strings.Split(svcs, ",")
	}
	if selector := c.Query("podLabelSelector"); selector != "" {
		filter.PodLabelSelector = selector
	}
	if ft := c.Query("flowTypes"); ft != "" {
		parts := strings.Split(ft, ",")
		for _, p := range parts {
			v, err := strconv.Atoi(strings.TrimSpace(p))
			if err != nil {
				return nil, fmt.Errorf("invalid flowType value %q: %w", p, err)
			}
			filter.FlowTypes = append(filter.FlowTypes, apisv1.FlowType(v))
		}
	}
	if ips := c.Query("ips"); ips != "" {
		filter.IPs = strings.Split(ips, ",")
	}
	if dir := c.Query("direction"); dir != "" {
		switch strings.ToLower(dir) {
		case "from":
			filter.Direction = apisv1.FlowFilterDirectionFrom
		case "to":
			filter.Direction = apisv1.FlowFilterDirectionTo
		default:
			filter.Direction = apisv1.FlowFilterDirectionBoth
		}
	}
	filter.Follow = parseFollowQuery(c)

	return filter, nil
}

// parseFollowQuery returns whether the client wants follow mode (live stream).
//
// Gin's DefaultQuery("follow", "true") returns "" when the key is present but
// empty (?follow=), and "" == "true" is false — that incorrectly disabled follow
// and caused Flow Aggregator to close the gRPC stream immediately (!follow && n==0),
// which showed up as SSE disconnect when applying an "empty" filter (minimal URL).
func parseFollowQuery(c *gin.Context) bool {
	v := strings.TrimSpace(c.DefaultQuery("follow", "true"))
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		// Be permissive: unknown values keep the stream open rather than snapping to one-shot mode.
		return true
	}
}

// StreamFlows handles GET /api/v1/flows/stream as an SSE endpoint.
func (h *SSEHandler) StreamFlows(c *gin.Context) {
	filter, err := parseFlowStreamFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	flowsCh, errCh := h.handler.Subscribe(ctx, filter)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Emit one SSE comment and flush before blocking on the first gRPC read. Otherwise, when the
	// Flow Aggregator ring buffer is empty, the select below blocks indefinitely with no bytes
	// written, so HTTP response headers are never flushed and clients (fetch, curl) see a hang
	// or "Disconnected" even though mTLS and auth succeeded.
	var preamble sync.Once
	writePreamble := func(w io.Writer) {
		preamble.Do(func() {
			if _, err := w.Write([]byte(": stream-open\n\n")); err != nil {
				h.logger.Error(err, "Failed to write SSE preamble")
				return
			}
			if fl, ok := c.Writer.(http.Flusher); ok {
				fl.Flush()
			}
		})
	}

	// The gRPC client only forwards non-empty flow batches (and dropped-count changes). When
	// filtered streams match nothing for a long time, nothing is sent on flowsCh and this
	// handler would block forever on the next select, stalling fetch() and freezing the UI.
	// Periodic SSE comments keep the connection and ReadableStream alive.
	keepAlive := time.NewTicker(5 * time.Second)
	defer keepAlive.Stop()

	c.Stream(func(w io.Writer) bool {
		writePreamble(w)
		select {
		case <-ctx.Done():
			return false
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return false
			}
			if fl, ok := c.Writer.(http.Flusher); ok {
				fl.Flush()
			}
			return true
		case event, ok := <-flowsCh:
			if !ok {
				return false
			}
			if event.DroppedCount > 0 {
				droppedEvt := apisv1.FlowStreamDroppedEvent{DroppedCount: event.DroppedCount}
				data, _ := json.Marshal(droppedEvt)
				c.SSEvent("dropped", string(data))
			}
			if len(event.Flows) > 0 {
				flowEvt := apisv1.FlowStreamEvent{Flows: event.Flows}
				data, err := json.Marshal(flowEvt)
				if err != nil {
					h.logger.Error(err, "Failed to marshal flow event")
					return true
				}
				c.SSEvent("flow", string(data))
			}
			return true
		case streamErr, ok := <-errCh:
			if !ok {
				return false
			}
			errEvent := apisv1.FlowStreamErrorEvent{Message: streamErr.Error()}
			data, _ := json.Marshal(errEvent)
			c.SSEvent("error", string(data))
			h.logger.Error(streamErr, "Flow stream error")
			return false
		}
	})
}
