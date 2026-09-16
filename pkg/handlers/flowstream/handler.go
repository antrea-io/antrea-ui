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
	"strconv"
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
//
// These narrow a stream; they do not authorize it. See FlowStreamScope, which does, and which is
// deliberately a separate type: Namespaces here selects flows by either end, and upstream
// explicitly allows naming a Namespace that is outside the stream's scope, which is how a flow is
// selected by its peer rather than by its own end.
type FlowStreamFilter struct {
	Namespaces       []string
	PodNames         []string
	PodLabelSelector string
	ServiceNames     []string
	FlowTypes        []apisv1.FlowType
	IPs              []string
	Direction        FlowFilterDirection
}

// FlowStreamScope is the authorization scope of a stream, distinct from the filters that narrow
// it: the Flow Aggregator checks RBAC against this and resolves each endpoint's disclosure tier
// relative to it. Exactly one of ObservedNamespace and ClusterWide must be set.
//
// Scalar, though the protobuf field is repeated: the Flow Aggregator names one Namespace per
// stream at most (maxRequestedNamespaces), and a cap it set to match this UI's single-select is
// not worth carrying a slice through every layer for. filterToGetFlowsRequest builds the
// one-element slice.
type FlowStreamScope struct {
	ObservedNamespace string
	ClusterWide       bool
}

// defaultKeepAliveInterval is how often the stream emits an SSE comment and re-checks its session.
const defaultKeepAliveInterval = 5 * time.Second

// defaultInitialResponseTimeout bounds how long StreamFlows waits for Subscribe to confirm the
// stream is live (or fail) before committing to a 200 response anyway. See the comment where it
// is used: against a Flow Aggregator that sends its post-authz ack (antrea-io/antrea#8420), this
// almost never fires - it exists for the case FA accepts the call and then never responds at all.
const defaultInitialResponseTimeout = 15 * time.Second

// streamErrorEvent describes streamErr for a client, carrying classifyStreamErr's code and
// retryable flag when it has them so the client does not have to parse Message. It is the body of
// both the SSE "error" event and the pre-200 HTTP error response, so the frontend parses one
// shape either way.
func streamErrorEvent(streamErr error) apisv1.FlowStreamErrorEvent {
	evt := apisv1.FlowStreamErrorEvent{Message: streamErr.Error()}
	var se *StreamError
	if errors.As(streamErr, &se) {
		evt.Code = se.Code
		evt.Retryable = se.Retryable
	}
	return evt
}

// statusForStreamErr maps a flow-stream failure to the HTTP status StreamFlows returns for it when
// caught before the response is committed to a 200.
//
// Deliberately never 401, even for StreamErrorCodeUnauthenticated: a 401 from any antrea-ui
// endpoint means "your antrea-ui session is over, log in again", and the frontend acts on it by
// doing exactly that. A credential the Flow Aggregator rejects says nothing about the antrea-ui
// session - FA is a different server, trusting a different CA and potentially a different audience
// than the kube-apiserver (see classifyStreamErr for the full reasoning, and
// TestSubscribeDoesNotInvalidateSessionOnUnauthenticated for the backend half of it). Returning a
// 401 here would log the user out of every page in the UI because flow visibility alone could not
// authenticate, on every single re-login, whenever that mismatch is simply how the deployment is
// configured. The failure is an upstream one, so it gets an upstream status; the client tells the
// kinds apart from the response body's code/retryable fields, not from the status.
func statusForStreamErr(err error) int {
	var streamErr *StreamError
	if errors.As(err, &streamErr) {
		switch streamErr.Code {
		case StreamErrorCodeResourceExhausted:
			// Capacity, not a broken upstream: the one pre-200 failure worth retrying, and
			// 503 is the status that says so.
			return http.StatusServiceUnavailable
		case StreamErrorCodeForbidden:
			// The Flow Aggregator authenticated the credential but refused the stream: a
			// real 403, the same as any other antrea-ui endpoint's RBAC denial, and the one
			// status FlowStreamClient checks for to render the "missing flows grant" panel
			// instead of a generic error.
			return http.StatusForbidden
		}
	}
	return http.StatusBadGateway
}

// errUnauthenticatedStream means the handler was reached without the authentication middleware
// having resolved an identity, which is a wiring bug rather than anything a client can cause.
var errUnauthenticatedStream = errors.New("flow stream request carries no resolved identity")

// SSEHandler handles the SSE endpoint for flow streaming.
//
// This endpoint is authenticated here and authorized entirely by the Flow Aggregator. The
// subscriber presents the caller's own credential (a bearer token or client cert; see grpc.go's
// resolveCall), and FlowStreamService checks Kubernetes RBAC against a virtual "flows" resource
// in the scope the request names, then resolves each endpoint of each record to the disclosure
// tier that caller is entitled to. Records therefore arrive here already redacted.
//
// antrea-ui performs no redaction of its own and must not: doing so would need antrea-ui's own
// ServiceAccount to hold unredacted cluster-wide flow access, making it a privileged component
// and duplicating the Flow Aggregator's tier logic. All this handler does with the scope is parse
// it and validate it locally - see parseFlowStreamScope - so a request that cannot be authorized
// is refused without a connection.
type SSEHandler struct {
	logger  logr.Logger
	handler FlowStreamSubscriber
	// keepAliveInterval is a field so tests do not have to wait seconds for a tick.
	keepAliveInterval time.Duration
	// initialResponseTimeout is a field so tests do not have to wait for the production timeout.
	initialResponseTimeout time.Duration
}

func NewSSEHandler(logger logr.Logger, handler FlowStreamSubscriber) *SSEHandler {
	return &SSEHandler{
		logger:                 logger,
		handler:                handler,
		keepAliveInterval:      defaultKeepAliveInterval,
		initialResponseTimeout: defaultInitialResponseTimeout,
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

// parseFlowStreamScope reads the stream's authorization scope from the query string.
//
// Every rejection here is one the Flow Aggregator would also make, with INVALID_ARGUMENT, but
// answering locally gives a message that names the query parameter at fault and costs no
// connection to a server that was only going to refuse. A comma-separated list (or a repeated
// parameter) is rejected rather than truncated to its first element, so a client can never come
// away believing it asked for more than it got.
func parseFlowStreamScope(c *gin.Context) (*FlowStreamScope, error) {
	scope := &FlowStreamScope{}

	if values := c.QueryArray("observedNamespace"); len(values) > 0 {
		if len(values) > 1 {
			return nil, fmt.Errorf("invalid observedNamespace: a stream observes a single namespace, but %d were given", len(values))
		}
		v := values[0]
		if strings.Contains(v, ",") {
			return nil, fmt.Errorf("invalid observedNamespace %q: a stream observes a single namespace, not a list", v)
		}
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("invalid observedNamespace: must not be empty; request cluster scope with clusterWide=true")
		}
		scope.ObservedNamespace = strings.TrimSpace(v)
	}
	if values := c.QueryArray("clusterWide"); len(values) > 0 {
		if len(values) > 1 {
			return nil, fmt.Errorf("invalid clusterWide: expected a single value, but %d were given", len(values))
		}
		v, err := strconv.ParseBool(values[0])
		if err != nil {
			return nil, fmt.Errorf("invalid clusterWide value %q: expected true or false", values[0])
		}
		scope.ClusterWide = v
	}

	switch {
	case scope.ClusterWide && scope.ObservedNamespace != "":
		return nil, fmt.Errorf("observedNamespace and clusterWide are mutually exclusive")
	case !scope.ClusterWide && scope.ObservedNamespace == "":
		return nil, fmt.Errorf("a flow stream must name its scope: set observedNamespace to a single namespace, or clusterWide=true")
	}
	return scope, nil
}

// StreamFlows handles GET /api/v1/flows/stream as an SSE endpoint.
func (h *SSEHandler) StreamFlows(c *gin.Context) {
	filter, err := parseFlowStreamFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	scope, err := parseFlowStreamScope(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	flowsCh, errCh, readyCh := h.handler.Subscribe(ctx, scope, filter)

	// Every failure this endpoint can hit today - a rejected credential, FA at capacity, a dial
	// or credential-resolution failure - surfaces on Subscribe's first GetFlows call or its first
	// Recv (see startStream), not partway through an established stream. Waiting for either errCh
	// or readyCh (see Subscribe) lets a real failure reach the client as an HTTP status instead of
	// prose inside a 200 body, and lets a real success commit to the 200 as soon as it is known
	// rather than guessed - the timeout below is a fallback for a Flow Aggregator that accepts the
	// call and then never responds at all, not the common path.
	select {
	case streamErr, ok := <-errCh:
		if ok {
			h.logger.Error(streamErr, "Flow stream failed before the response was committed")
			c.JSON(statusForStreamErr(streamErr), streamErrorEvent(streamErr))
			return
		}
		// errCh closed with nothing buffered: Subscribe ended (e.g. ctx already canceled)
		// without an error. Disable this case for the rest of the request, same as the
		// c.Stream loop below does for the same situation.
		errCh = nil
	case <-readyCh:
		// The stream is confirmed live: proceed as an ordinary 200 SSE stream.
	case <-time.After(h.initialResponseTimeout):
		// No answer either way: proceed as an ordinary 200 SSE stream.
	}

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
	// Commit and flush the status and headers now, rather than waiting for the first byte of
	// body: readyCh above already confirmed the stream is live, but flowsCh can still go quiet
	// for a while on a narrow filter, and c.Stream's loop only writes (and only then flushes) on
	// a flow, a dropped-count change, or the next keepAlive tick (up to keepAliveInterval away).
	// WriteHeaderNow alone only records the status on gin's writer - net/http still buffers the
	// header bytes until a body write or an explicit Flush - so without the Flush here, clients
	// (fetch, curl) would see a hang or "Disconnected" for that long even though TLS and auth
	// succeeded, and a proxy honoring X-Accel-Buffering would hold the headers just as long.
	c.Writer.Flush()

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

	// emitStreamError writes streamErr as an SSE "error" event and reports whether the caller
	// should keep streaming (it never does: every caller treats an error as terminal).
	emitStreamError := func(streamErr error) bool {
		data, err := json.Marshal(streamErrorEvent(streamErr))
		if err != nil {
			h.logger.Error(err, "Failed to marshal error event")
			return false
		}
		c.SSEvent("error", string(data))
		h.logger.Error(streamErr, "Flow stream error")
		return false
	}

	c.Stream(func(w io.Writer) bool {
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
				// Subscribe closes flowsCh before errCh (see its own comment on why), but a
				// final error it buffered into errCh just before closing both is a second,
				// independently-ready case by the time this select runs - select picks
				// uniformly among ready cases, so without this drain, roughly half the time
				// this branch would be chosen over the errCh one and the error would never
				// reach the client. A non-blocking receive here catches it either way: errCh
				// already holds the value (ok), is already closed with nothing buffered
				// (!ok, the common case), or isn't closed yet, in which case Subscribe is
				// still running and errCh could not have been written to end this stream.
				select {
				case streamErr, ok := <-errCh:
					if ok {
						return emitStreamError(streamErr)
					}
				default:
				}
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
			return emitStreamError(streamErr)
		}
	})
}
