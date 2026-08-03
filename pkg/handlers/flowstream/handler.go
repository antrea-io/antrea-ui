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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-logr/logr"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
)

// FlowFilterDirection controls which endpoint of a flow the directional filters are matched against.
type FlowFilterDirection int32

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

// defaultKeepAliveInterval is how often the stream emits an SSE comment and re-checks its session.
const defaultKeepAliveInterval = 5 * time.Second

// errUnauthenticatedStream means the handler was reached without the authentication middleware
// having resolved an identity, which is a wiring bug rather than anything a client can cause.
var errUnauthenticatedStream = errors.New("flow stream request carries no resolved identity")

// SSEHandler handles the SSE endpoint for flow streaming.
//
// Known gap, deliberate for now: this endpoint is authenticated but not authorized per user. The
// subscriber reaches the Flow Aggregator over antrea-ui's own mTLS gRPC connection, so unlike
// every other API route, the caller's Kubernetes RBAC has no say in what they see — any user who
// can log in sees every exported flow. Authorization is being implemented upstream in
// antrea-io/antrea#8221; see the "Flow data is not yet per-user" section of docs/authentication.md.
type SSEHandler struct {
	logger  logr.Logger
	handler FlowStreamSubscriber
	// keepAliveInterval is a field so tests do not have to wait seconds for a tick.
	keepAliveInterval time.Duration
}

func NewSSEHandler(logger logr.Logger, handler FlowStreamSubscriber) *SSEHandler {
	return &SSEHandler{
		logger:            logger,
		handler:           handler,
		keepAliveInterval: defaultKeepAliveInterval,
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

var flowTypeByName = map[string]apisv1.FlowType{
	"intra-node":    apisv1.FlowTypeIntraNode,
	"inter-node":    apisv1.FlowTypeInterNode,
	"to-external":   apisv1.FlowTypeToExternal,
	"from-external": apisv1.FlowTypeFromExternal,
}

func parseFlowType(s string) (apisv1.FlowType, error) {
	if v, ok := flowTypeByName[strings.ToLower(s)]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("invalid flowType value %q: expected one of intra-node, inter-node, to-external, from-external", s)
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
		for _, p := range splitTrimmed(ft) {
			v, err := parseFlowType(p)
			if err != nil {
				return nil, err
			}
			filter.FlowTypes = append(filter.FlowTypes, v)
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
	keepAlive := time.NewTicker(h.keepAliveInterval)
	defer keepAlive.Stop()

	// This is a single request that can run for hours (nginx allows up to 24h for it), so the
	// session's last-seen time has to be bumped for as long as the stream is attached -
	// otherwise an actively-streaming session would idle out from under itself. This holds even
	// while the tab is in the background, which is the one place antrea-ui departs from "idle
	// means no visible tab": a flow-visibility tab is something people background on purpose.
	// See RequestAuth.KeepAlive for why that exception is only safe because the same call also
	// renews the credential. It reports when the session has ended (logged out in another tab,
	// past the absolute lifetime cap, a credential that can no longer be renewed), which must
	// close the stream: a logged-out user must stop receiving flows.
	// Fails closed: every route reaching this handler goes through the authentication
	// middleware, so a missing identity means the handler was wired up without it. A stream
	// that cannot tell whether its session is still alive must not keep running for hours.
	sessionAlive := func() bool {
		ra, ok := session.RequestAuthFrom(ctx)
		if !ok {
			h.logger.Error(errUnauthenticatedStream, "Closing flow stream")
			return false
		}
		return ra.KeepAlive(ctx)
	}

	c.Stream(func(w io.Writer) bool {
		writePreamble(w)
		select {
		case <-ctx.Done():
			return false
		case <-keepAlive.C:
			if !sessionAlive() {
				h.logger.V(2).Info("Closing flow stream: session is no longer valid")
				return false
			}
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
