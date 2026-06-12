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
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// FlowFilterDirection controls which endpoint of a flow the directional filters are matched against.
type FlowFilterDirection int

const (
	FlowFilterDirectionBoth FlowFilterDirection = 0
	FlowFilterDirectionFrom FlowFilterDirection = 1
	FlowFilterDirectionTo   FlowFilterDirection = 2
)

// FlowStreamFilter represents the parsed query parameters for the flow stream endpoint.
// All specified filters are AND-ed. Within each filter, values are OR-ed.
type FlowStreamFilter struct {
	Namespaces       []string
	PodNames         []string
	PodLabelSelector string
	ServiceNames     []string
	FlowTypes        []apisv1.FlowType
	IPs              []string
	Direction        FlowFilterDirection
}

// SSEHandler handles the SSE endpoint for flow streaming.
type SSEHandler struct {
	logger  logr.Logger
	handler FlowStreamSubscriber
}

func NewSSEHandler(logger logr.Logger, handler FlowStreamSubscriber) *SSEHandler {
	return &SSEHandler{
		logger:  logger,
		handler: handler,
	}
}

// splitTrimmed splits s by comma and trims whitespace from each element,
// omitting any elements that are empty after trimming.
func splitTrimmed(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

func parseFlowStreamFilter(c *gin.Context) (*FlowStreamFilter, error) {
	filter := &FlowStreamFilter{}

	if ns := c.Query("namespaces"); ns != "" {
		filter.Namespaces = splitTrimmed(ns)
	}
	if pods := c.Query("pods"); pods != "" {
		filter.PodNames = splitTrimmed(pods)
	}
	if svcs := c.Query("services"); svcs != "" {
		filter.ServiceNames = splitTrimmed(svcs)
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
		filter.IPs = splitTrimmed(ips)
	}
	if dir := c.Query("direction"); dir != "" {
		switch strings.ToLower(dir) {
		case "from":
			filter.Direction = FlowFilterDirectionFrom
		case "to":
			filter.Direction = FlowFilterDirectionTo
		default:
			filter.Direction = FlowFilterDirectionBoth
		}
	}
	return filter, nil
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

	// Set headers required for Server-Sent Events (SSE).
	// Content-Type must be text/event-stream for browsers to process the stream.
	// Cache-Control: no-cache prevents intermediary proxies from caching the stream data.
	// Connection: keep-alive keeps the connection open for continuous data flow.
	// X-Accel-Buffering: no instructs Nginx and other proxies to disable response buffering,
	// ensuring events are sent to the client immediately instead of waiting for a buffer to fill.
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Emit one SSE comment and flush before blocking on the first gRPC read. Otherwise, when the
	// Flow Aggregator ring buffer is empty, the select below blocks indefinitely with no bytes
	// written, so HTTP response headers are never flushed and clients (fetch, curl) see a hang
	// or "Disconnected" even though mTLS and auth succeeded.
	preambleWritten := false
	writePreamble := func(w io.Writer) {
		if preambleWritten {
			return
		}
		preambleWritten = true
		if _, err := w.Write([]byte(": stream-open\n\n")); err != nil {
			h.logger.Error(err, "Failed to write SSE preamble")
			return
		}
		if fl, ok := c.Writer.(http.Flusher); ok {
			fl.Flush()
		}
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
				data, err := json.Marshal(droppedEvt)
				if err != nil {
					h.logger.Error(err, "Failed to marshal dropped event")
					return true
				}
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
				// No more errors will be sent; keep streaming until flowsCh is
				// closed or ctx is done. Setting errCh to nil disables this case
				// in future select iterations so we don't spin on a closed channel.
				errCh = nil
				return true
			}
			errEvent := apisv1.FlowStreamErrorEvent{Message: streamErr.Error()}
			data, err := json.Marshal(errEvent)
			if err != nil {
				h.logger.Error(err, "Failed to marshal error event")
				return false
			}
			c.SSEvent("error", string(data))
			h.logger.Error(streamErr, "Flow stream error")
			return false
		}
	})
}
