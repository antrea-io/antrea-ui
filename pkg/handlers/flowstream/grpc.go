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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// flowStreamConnKey is the session transport/connection cache key for a per-credential
// FlowAggregator gRPC connection (a KindCert credential; see connFor). It has no "/" so it is
// never treated as versioned - unlike the Antrea Service's TLS settings, the Flow Aggregator's
// do not change under a live session.
const flowStreamConnKey = "flow-aggregator-grpc"

// StreamError classifies a flow-stream failure for callers that need to react to it (the frontend,
// via the "code"/"retryable" fields on apisv1.FlowStreamErrorEvent) without parsing Error() text.
type StreamError struct {
	msg string
	// Code is a stable, machine-readable identifier for the failure kind.
	Code string
	// Retryable reports whether the same request is expected to succeed if retried, as opposed
	// to a failure that will recur until something about the request or the deployment changes.
	Retryable bool
}

func (e *StreamError) Error() string { return e.msg }

const (
	// StreamErrorCodeUnauthenticated means the Flow Aggregator rejected the credential it was
	// presented. Not retryable: the same credential will be rejected again.
	StreamErrorCodeUnauthenticated = "unauthenticated"
	// StreamErrorCodeResourceExhausted means the Flow Aggregator is at capacity (its stream
	// limiter or its token-auth semaphore). Retryable: capacity is expected to free up.
	StreamErrorCodeResourceExhausted = "resource_exhausted"
	// StreamErrorCodeInternal covers everything else: a dial failure, a credential this backend
	// could not resolve or mint, or any other error. Not retryable by default, since most of
	// these reflect a persistent local or configuration problem rather than a transient one.
	StreamErrorCodeInternal = "internal"
)

// internalStreamError wraps err as a non-retryable StreamError with StreamErrorCodeInternal, for
// failures resolved locally rather than reported by the Flow Aggregator.
func internalStreamError(err error) *StreamError {
	return &StreamError{msg: err.Error(), Code: StreamErrorCodeInternal, Retryable: false}
}

// GRPCFlowStreamSubscriber connects to the FlowAggregator's FlowStreamService
// over gRPC and implements the FlowStreamSubscriber interface.
type GRPCFlowStreamSubscriber struct {
	logger logr.Logger
	// address and tlsConfig are kept so that a per-credential connection (KindCert) can be
	// dialed with the same server-verification settings as the shared one.
	address   string
	tlsConfig *tls.Config
	// client and conn are the connection shared by every bearer-authenticated (and
	// admin-token) call - the common case, and cheap since it carries no per-user state.
	client flowpb.FlowStreamServiceClient
	conn   *grpc.ClientConn
	// adminTokenSource mints the bearer token used for admin-password (KindImpersonate)
	// sessions. Nil disables flow streaming for that login mode.
	adminTokenSource *AdminTokenSource
}

// GRPCConfig holds the connection parameters for the FlowAggregator gRPC server.
type GRPCConfig struct {
	Address string
	// TLSConfig is the TLS configuration used for the gRPC connection. Its Certificates field
	// is left unset here: a KindCert session credential adds its own, on a separate
	// per-session connection (see connFor).
	TLSConfig *tls.Config
	// AdminTokenSource mints the bearer token used for admin-password sessions. May be nil, in
	// which case that login mode cannot use flow streaming.
	AdminTokenSource *AdminTokenSource
}

func NewGRPCFlowStreamSubscriber(logger logr.Logger, cfg GRPCConfig) (*GRPCFlowStreamSubscriber, error) {
	if cfg.TLSConfig == nil {
		return nil, fmt.Errorf("TLSConfig must not be nil")
	}
	conn, err := grpc.NewClient(
		cfg.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg.TLSConfig)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to %s: %w", cfg.Address, err)
	}

	client := flowpb.NewFlowStreamServiceClient(conn)
	// Note: grpc.NewClient is lazy — the connection is not established until the first
	// RPC call. This log confirms the client was created successfully, not that the
	// server is reachable.
	logger.Info("FlowAggregator gRPC client created", "address", cfg.Address)

	return &GRPCFlowStreamSubscriber{
		logger:           logger,
		address:          cfg.Address,
		tlsConfig:        cfg.TLSConfig,
		client:           client,
		conn:             conn,
		adminTokenSource: cfg.AdminTokenSource,
	}, nil
}

func (h *GRPCFlowStreamSubscriber) Close() error {
	if h.conn != nil {
		return h.conn.Close()
	}
	return nil
}

// buildCertConn dials a connection to the Flow Aggregator presenting cred's client certificate as
// the TLS handshake credential. It is a session.ConnBuilder, cached per session by connFor: FA
// authenticates a KindCert credential from the connection, not from per-call metadata, so it
// needs its own connection (and its own pool) rather than reusing the shared one.
func (h *GRPCFlowStreamSubscriber) buildCertConn(cred *session.Credential) (*grpc.ClientConn, func(), error) {
	if h.tlsConfig == nil {
		// (*tls.Config).Clone() returns nil for a nil receiver, which would nil-panic below.
		// NewGRPCFlowStreamSubscriber rejects a nil TLSConfig, so this only guards against a
		// struct built directly (e.g. by a test) with tlsConfig left unset.
		return nil, nil, fmt.Errorf("flow aggregator client TLS config is not configured")
	}
	cert, err := tls.X509KeyPair(cred.CertPEM, cred.KeyPEM)
	if err != nil {
		// Deliberately not wrapping err with any credential detail.
		return nil, nil, fmt.Errorf("failed to build FlowAggregator client certificate")
	}
	tlsCfg := h.tlsConfig.Clone()
	tlsCfg.Certificates = []tls.Certificate{cert}
	conn, err := grpc.NewClient(h.address, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gRPC connection to %s: %w", h.address, err)
	}
	return conn, func() { conn.Close() }, nil
}

// resolveCall returns the client to call GetFlows on and the context to call it with (carrying
// bearer credential metadata, where the credential kind uses one), for the identity resolved for
// ctx's request. See docs/authentication.md, "Flow data is not yet per-user": FA accepts exactly
// two credential shapes, a bearer token in call metadata or a client cert on the connection, and
// nothing else.
func (h *GRPCFlowStreamSubscriber) resolveCall(ctx context.Context) (flowpb.FlowStreamServiceClient, context.Context, error) {
	ra, ok := session.RequestAuthFrom(ctx)
	if !ok {
		return nil, nil, internalStreamError(fmt.Errorf("flow stream request carries no resolved identity"))
	}
	cred := ra.Credential()
	switch cred.Kind {
	case session.KindBearer:
		return h.client, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+string(cred.Token)), nil
	case session.KindCert:
		conn, err := ra.ConnFor(flowStreamConnKey, h.buildCertConn)
		if err != nil {
			return nil, nil, internalStreamError(fmt.Errorf("failed to build FlowAggregator client certificate connection: %w", err))
		}
		return flowpb.NewFlowStreamServiceClient(conn), ctx, nil
	case session.KindImpersonate:
		if h.adminTokenSource == nil {
			return nil, nil, internalStreamError(fmt.Errorf("flow visibility is unavailable in admin-password mode: no admin token source is configured"))
		}
		token, err := h.adminTokenSource.Token(ctx)
		if err != nil {
			return nil, nil, internalStreamError(fmt.Errorf("failed to mint admin token for flow stream: %w", err))
		}
		// FA charges a token-auth slot whenever authorization metadata is present, so this
		// path (like KindBearer) must never also carry a client cert on the connection -
		// h.client is the shared, cert-free connection, so that is guaranteed here.
		return h.client, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), nil
	default:
		return nil, nil, internalStreamError(fmt.Errorf("unsupported credential kind %q for flow stream", cred.Kind))
	}
}

func (h *GRPCFlowStreamSubscriber) Subscribe(ctx context.Context, filter *FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent, 16)
	errCh := make(chan error, 1)

	go func() {
		// Defers run LIFO: flowsCh is closed first so the SSE handler can drain
		// all buffered flow events before errCh closes and terminates the stream.
		defer close(errCh)
		defer close(flowsCh)

		client, callCtx, err := h.resolveCall(ctx)
		if err != nil {
			h.logger.Error(err, "Failed to resolve credential for flow stream")
			errCh <- err
			return
		}

		req := filterToGetFlowsRequest(filter)
		// startStream has to read the first response too, not just call GetFlows: for a
		// server-streaming RPC, an error the server returns before sending anything (FA's
		// Unauthenticated/ResourceExhausted included) surfaces on the first Recv, not on the
		// call that opens the stream.
		stream, firstResp, err := h.startStream(ctx, client, callCtx, req)
		if err != nil {
			errCh <- err
			return
		}
		if stream == nil {
			// ctx ended, or the connection was closed from under the call (e.g. a KindCert
			// session logging out): an ordinary client disconnect, not a stream failure.
			// Nothing to report.
			return
		}

		// lastDroppedCount tracks the cumulative absolute dropped-flow count from the server.
		// We forward a new event only when the count increases; the forwarded value is the
		// absolute cumulative total, not a per-event delta.
		var lastDroppedCount uint64
		if firstResp != nil {
			if !h.forwardResp(ctx, firstResp, &lastDroppedCount, flowsCh) {
				return
			}
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil || status.Code(err) == codes.Canceled {
					// A Canceled Recv with ctx still alive means the connection itself was
					// closed out from under this call - e.g. a KindCert session logging out
					// drops its per-session ClientConn (see Session.zero). That is an
					// ordinary disconnect, not a stream failure worth reporting.
					return
				}
				h.logger.Error(err, "Error receiving from flow stream")
				errCh <- h.classifyStreamErr(err)
				return
			}
			if !h.forwardResp(ctx, resp, &lastDroppedCount, flowsCh) {
				return
			}
		}
	}()

	return flowsCh, errCh
}

// startStream calls GetFlows and reads the first response. It does not retry: classifyStreamErr
// marks a retryable failure (e.g. ResourceExhausted) as such, and retrying is the frontend's job,
// driven by its own reconnect backoff. Retrying here too would double up that backoff behind a
// single opaque error, hiding the failure kind (and whether it is worth retrying at all) from the
// client for as long as this loop kept trying.
//
// The first response has to be read here, not left for the caller's receive loop: for a
// server-streaming RPC, FA reports both Unauthenticated and ResourceExhausted before sending any
// message, and that kind of error does not surface on the call that opens the stream - only on
// the first Recv. A nil firstResp with a nil error means the server closed the stream immediately
// with no error and no message; the caller's receive loop handles that the same way it always
// has. A nil stream with a nil error means ctx ended, or the underlying connection was closed
// (codes.Canceled) while this call or the wait for a first matching flow was still in flight
// (which, with a narrow filter, can take a while); the caller treats that as an ordinary
// disconnect, not a failure.
func (h *GRPCFlowStreamSubscriber) startStream(ctx context.Context, client flowpb.FlowStreamServiceClient, callCtx context.Context, req *flowpb.GetFlowsRequest) (flowpb.FlowStreamService_GetFlowsClient, *flowpb.GetFlowsResponse, error) {
	stream, err := client.GetFlows(callCtx, req)
	if err == nil {
		resp, recvErr := stream.Recv()
		switch {
		case recvErr == nil:
			return stream, resp, nil
		case errors.Is(recvErr, io.EOF):
			return stream, nil, nil
		default:
			err = recvErr
		}
	}
	if ctx.Err() != nil || status.Code(err) == codes.Canceled {
		return nil, nil, nil
	}
	h.logger.Error(err, "Failed to start GetFlows stream")
	return nil, nil, h.classifyStreamErr(err)
}

// forwardResp converts resp to a FlowStreamEvent and sends it on flowsCh if it carries anything
// new, tracking the cumulative dropped-flow count in *lastDroppedCount across calls (the server
// reports an absolute cumulative total, not a per-event delta). It reports false if ctx ended
// while waiting to send, which tells the caller to stop the stream.
func (h *GRPCFlowStreamSubscriber) forwardResp(ctx context.Context, resp *flowpb.GetFlowsResponse, lastDroppedCount *uint64, flowsCh chan<- apisv1.FlowStreamEvent) bool {
	evt := apisv1.FlowStreamEvent{}
	if resp.DroppedCount > *lastDroppedCount {
		*lastDroppedCount = resp.DroppedCount
		evt.DroppedCount = *lastDroppedCount
	}
	if len(resp.Flows) > 0 {
		converted := make([]apisv1.Flow, 0, len(resp.Flows))
		for _, pbFlow := range resp.Flows {
			converted = append(converted, protoFlowToAPI(pbFlow))
		}
		evt.Flows = converted
	}
	if evt.DroppedCount == 0 && len(evt.Flows) == 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case flowsCh <- evt:
		return true
	}
}

// classifyStreamErr turns a gRPC error from GetFlows/Recv into a StreamError callers of Subscribe
// (ultimately the frontend, via FlowStreamErrorEvent's code/retryable fields) can act on without
// parsing the message:
//   - Unauthenticated: the credential FA saw was rejected. Not retryable. This is deliberately
//     *not* treated as a reason to invalidate the antrea-ui session the way an upstream 401 from
//     the kube-apiserver is (see RequestAuth.Invalidate): FA is a different server, trusting a
//     different CA and (for a bearer token) potentially a different audience than the
//     kube-apiserver, so a credential FA rejects may still be perfectly valid for every other
//     antrea-ui call. Ending the whole UI session over it would log the user out of pages that
//     have nothing to do with flow visibility, on every single re-login, if that mismatch is
//     simply how the deployment is configured.
//   - ResourceExhausted: FA is at capacity (its stream limiter or its token-auth semaphore).
//     Retryable.
//   - anything else: a generic, non-retryable stream failure.
func (h *GRPCFlowStreamSubscriber) classifyStreamErr(err error) *StreamError {
	switch status.Code(err) {
	case codes.Unauthenticated:
		return &StreamError{
			msg:       fmt.Errorf("FlowAggregator rejected the credential: %w", err).Error(),
			Code:      StreamErrorCodeUnauthenticated,
			Retryable: false,
		}
	case codes.ResourceExhausted:
		return &StreamError{
			msg:       fmt.Errorf("FlowAggregator is at capacity, please retry: %w", err).Error(),
			Code:      StreamErrorCodeResourceExhausted,
			Retryable: true,
		}
	default:
		return &StreamError{
			msg:       fmt.Errorf("flow stream error: %w", err).Error(),
			Code:      StreamErrorCodeInternal,
			Retryable: false,
		}
	}
}

var filterDirectionToProto = map[FlowFilterDirection]flowpb.FlowFilterDirection{
	FlowFilterDirectionBoth: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_BOTH,
	FlowFilterDirectionFrom: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
	FlowFilterDirectionTo:   flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO,
}

// filterToGetFlowsRequest translates our internal filter type to the protobuf request.
func filterToGetFlowsRequest(filter *FlowStreamFilter) *flowpb.GetFlowsRequest {
	pbFilter := &flowpb.FlowFilter{
		Namespaces:       filter.Namespaces,
		PodNames:         filter.PodNames,
		PodLabelSelector: filter.PodLabelSelector,
		ServiceNames:     filter.ServiceNames,
		Ips:              filter.IPs,
		Direction:        filterDirectionToProto[filter.Direction],
	}
	for _, ft := range filter.FlowTypes {
		pbFilter.FlowTypes = append(pbFilter.FlowTypes, flowpb.FlowType(ft))
	}
	return &flowpb.GetFlowsRequest{
		Filters: []*flowpb.FlowFilter{pbFilter},
		// The SSE flow stream always requires follow mode so the Flow Aggregator does not
		// close the gRPC stream on the first empty ring-buffer read (!follow && n==0).
		Follow: true,
	}
}

// ipBytesToString converts a protobuf bytes IP address to its string representation.
// Returns an empty string if the slice is nil/empty or not a valid IP.
func ipBytesToString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	addr, ok := netip.AddrFromSlice(b)
	if !ok {
		return fmt.Sprintf("<invalid-ip:%x>", b)
	}
	return addr.String()
}

// protoFlowToAPI converts a protobuf Flow message to our JSON-serializable API type.
func protoFlowToAPI(pb *flowpb.Flow) apisv1.Flow {
	f := apisv1.Flow{
		ID:        pb.GetId(),
		EndReason: apisv1.FlowEndReason(pb.GetEndReason()),
	}
	if ts := pb.GetStartTs(); ts != nil {
		f.StartTs = ts.AsTime().Format(time.RFC3339Nano)
	}
	if ts := pb.GetEndTs(); ts != nil {
		f.EndTs = ts.AsTime().Format(time.RFC3339Nano)
	}

	if ip := pb.GetIp(); ip != nil {
		f.IP = apisv1.FlowIP{
			Version:     apisv1.IPVersion(ip.GetVersion()),
			Source:      ipBytesToString(ip.GetSource()),
			Destination: ipBytesToString(ip.GetDestination()),
		}
	}

	if t := pb.GetTransport(); t != nil {
		f.Transport = apisv1.FlowTransport{
			ProtocolNumber:  t.GetProtocolNumber(),
			SourcePort:      t.GetSourcePort(),
			DestinationPort: t.GetDestinationPort(),
		}
		if tcp := t.GetTCP(); tcp != nil {
			f.Transport.TCP = &apisv1.FlowTCP{
				StateName: tcp.GetStateName(),
			}
		}
	}

	if k := pb.GetK8S(); k != nil {
		f.K8s = apisv1.FlowKubernetes{
			FlowType:                       apisv1.FlowType(k.GetFlowType()),
			SourcePodNamespace:             k.GetSourcePodNamespace(),
			SourcePodName:                  k.GetSourcePodName(),
			SourcePodUid:                   k.GetSourcePodUid(),
			SourcePodLabels:                k.GetSourcePodLabels().GetLabels(),
			SourceNodeName:                 k.GetSourceNodeName(),
			SourceNodeUid:                  k.GetSourceNodeUid(),
			DestinationPodNamespace:        k.GetDestinationPodNamespace(),
			DestinationPodName:             k.GetDestinationPodName(),
			DestinationPodUid:              k.GetDestinationPodUid(),
			DestinationPodLabels:           k.GetDestinationPodLabels().GetLabels(),
			DestinationNodeName:            k.GetDestinationNodeName(),
			DestinationNodeUid:             k.GetDestinationNodeUid(),
			DestinationClusterIp:           ipBytesToString(k.GetDestinationClusterIp()),
			DestinationServicePort:         k.GetDestinationServicePort(),
			DestinationServicePortName:     k.GetDestinationServicePortName(),
			DestinationServiceUid:          k.GetDestinationServiceUid(),
			IngressNetworkPolicyType:       apisv1.NetworkPolicyType(k.GetIngressNetworkPolicyType()),
			IngressNetworkPolicyNamespace:  k.GetIngressNetworkPolicyNamespace(),
			IngressNetworkPolicyName:       k.GetIngressNetworkPolicyName(),
			IngressNetworkPolicyUid:        k.GetIngressNetworkPolicyUid(),
			IngressNetworkPolicyRuleName:   k.GetIngressNetworkPolicyRuleName(),
			IngressNetworkPolicyRuleAction: apisv1.NetworkPolicyRuleAction(k.GetIngressNetworkPolicyRuleAction()),
			EgressNetworkPolicyType:        apisv1.NetworkPolicyType(k.GetEgressNetworkPolicyType()),
			EgressNetworkPolicyNamespace:   k.GetEgressNetworkPolicyNamespace(),
			EgressNetworkPolicyName:        k.GetEgressNetworkPolicyName(),
			EgressNetworkPolicyUid:         k.GetEgressNetworkPolicyUid(),
			EgressNetworkPolicyRuleName:    k.GetEgressNetworkPolicyRuleName(),
			EgressNetworkPolicyRuleAction:  apisv1.NetworkPolicyRuleAction(k.GetEgressNetworkPolicyRuleAction()),
			EgressName:                     k.GetEgressName(),
			EgressIp:                       ipBytesToString(k.GetEgressIp()),
			EgressNodeName:                 k.GetEgressNodeName(),
			EgressNodeUid:                  k.GetEgressNodeUid(),
			EgressUid:                      k.GetEgressUid(),
		}
	}

	if s := pb.GetStats(); s != nil {
		f.Stats = apisv1.FlowStats{
			PacketTotalCount: s.GetPacketTotalCount(),
			PacketDeltaCount: s.GetPacketDeltaCount(),
			OctetTotalCount:  s.GetOctetTotalCount(),
			OctetDeltaCount:  s.GetOctetDeltaCount(),
		}
		if agg := pb.GetAggregation(); agg != nil {
			f.Stats.Throughput = agg.GetThroughput()
		}
	}

	if rs := pb.GetReverseStats(); rs != nil {
		f.ReverseStats = apisv1.FlowStats{
			PacketTotalCount: rs.GetPacketTotalCount(),
			PacketDeltaCount: rs.GetPacketDeltaCount(),
			OctetTotalCount:  rs.GetOctetTotalCount(),
			OctetDeltaCount:  rs.GetOctetDeltaCount(),
		}
		if agg := pb.GetAggregation(); agg != nil {
			f.ReverseStats.Throughput = agg.GetReverseThroughput()
		}
	}

	return f
}
