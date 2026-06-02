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
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	flowpb "antrea.io/antrea/v2/pkg/apis/flow/v1alpha1"
	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// GRPCFlowStreamSubscriber connects to the FlowAggregator's FlowStreamService
// over gRPC and implements the FlowStreamSubscriber interface.
type GRPCFlowStreamSubscriber struct {
	logger logr.Logger
	client flowpb.FlowStreamServiceClient
	conn   *grpc.ClientConn
}

// GRPCConfig holds the connection parameters for the FlowAggregator gRPC server.
// The FlowStreamService uses server-side TLS only (no client authentication).
type GRPCConfig struct {
	Address string
	// CACert is the PEM-encoded CA certificate used to verify the FlowStreamService
	// server certificate. If empty, server verification is skipped (dev only).
	CACert []byte
}

func NewGRPCFlowStreamSubscriber(logger logr.Logger, cfg GRPCConfig) (*GRPCFlowStreamSubscriber, error) {
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config: %w", err)
	}
	conn, err := grpc.NewClient(
		cfg.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
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
		logger: logger,
		client: client,
		conn:   conn,
	}, nil
}

func (h *GRPCFlowStreamSubscriber) Close() error {
	if h.conn != nil {
		return h.conn.Close()
	}
	return nil
}

func (h *GRPCFlowStreamSubscriber) Subscribe(ctx context.Context, filter *apisv1.FlowStreamFilter) (<-chan apisv1.FlowStreamEvent, <-chan error) {
	flowsCh := make(chan apisv1.FlowStreamEvent, 16)
	errCh := make(chan error, 1)

	go func() {
		defer close(flowsCh)
		defer close(errCh)

		req := filterToGetFlowsRequest(filter)
		stream, err := h.client.GetFlows(ctx, req)
		if err != nil {
			h.logger.Error(err, "Failed to start GetFlows stream")
			errCh <- fmt.Errorf("failed to start flow stream: %w", err)
			return
		}

		// lastDroppedCount tracks the cumulative absolute dropped-flow count from the server.
		// We forward a new event only when the count increases; the forwarded value is the
		// absolute cumulative total, not a per-event delta.
		var lastDroppedCount uint64
		for {
			resp, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil {
					return
				}
				h.logger.Error(err, "Error receiving from flow stream")
				errCh <- fmt.Errorf("flow stream error: %w", err)
				return
			}

			if resp.DroppedCount > lastDroppedCount {
				lastDroppedCount = resp.DroppedCount
				select {
				case <-ctx.Done():
					return
				case flowsCh <- apisv1.FlowStreamEvent{DroppedCount: lastDroppedCount}:
				}
			}

			if len(resp.Flows) > 0 {
				converted := make([]apisv1.Flow, 0, len(resp.Flows))
				for _, pbFlow := range resp.Flows {
					converted = append(converted, protoFlowToAPI(pbFlow))
				}
				select {
				case <-ctx.Done():
					return
				case flowsCh <- apisv1.FlowStreamEvent{Flows: converted}:
				}
			}
		}
	}()

	return flowsCh, errCh
}

var filterDirectionToProto = map[apisv1.FlowFilterDirection]flowpb.FlowFilterDirection{
	apisv1.FlowFilterDirectionBoth: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_BOTH,
	apisv1.FlowFilterDirectionFrom: flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_FROM,
	apisv1.FlowFilterDirectionTo:   flowpb.FlowFilterDirection_FLOW_FILTER_DIRECTION_TO,
}

// filterToGetFlowsRequest translates our internal filter type to the protobuf request.
func filterToGetFlowsRequest(filter *apisv1.FlowStreamFilter) *flowpb.GetFlowsRequest {
	pbFilter := &flowpb.FlowFilter{
		Namespaces:       filter.Namespaces,
		PodNames:         filter.PodNames,
		PodLabelSelector: filter.PodLabelSelector,
		ServiceNames:     filter.ServiceNames,
		Ips:              filter.IPs,
		Direction:        filterDirectionToProto[filter.Direction],
	}
	for _, ft := range filter.FlowTypes {
		pbFilter.FlowTypes = append(pbFilter.FlowTypes, flowpb.FlowType(ft)) //nolint:gosec // FlowType values are small constants, no overflow risk
	}
	return &flowpb.GetFlowsRequest{
		Filters: []*flowpb.FlowFilter{pbFilter},
		// This handler only backs the SSE flow stream, which must stay in follow mode so Flow
		// Aggregator does not close the gRPC stream on the first empty ring-buffer read
		// (!follow && n==0). Always set true regardless of filter.Follow so a zero Go value or
		// query parsing edge case cannot disable follow.
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
		StartTs:   pb.GetStartTs().AsTime().Format(time.RFC3339Nano),
		EndTs:     pb.GetEndTs().AsTime().Format(time.RFC3339Nano),
		EndReason: apisv1.FlowEndReason(pb.GetEndReason()),
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
	}

	if rs := pb.GetReverseStats(); rs != nil {
		f.ReverseStats = apisv1.FlowStats{
			PacketTotalCount: rs.GetPacketTotalCount(),
			PacketDeltaCount: rs.GetPacketDeltaCount(),
			OctetTotalCount:  rs.GetOctetTotalCount(),
			OctetDeltaCount:  rs.GetOctetDeltaCount(),
		}
	}

	return f
}

func buildTLSConfig(cfg GRPCConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if len(cfg.CACert) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CACert) {
			return nil, fmt.Errorf("failed to parse FlowAggregator CA cert")
		}
		tlsCfg.RootCAs = pool
	} else {
		// No CA provided: skip verification (dev/test only).
		tlsCfg.InsecureSkipVerify = true //nolint:gosec
	}

	// When dialing via kubectl port-forward the address resolves to loopback or "localhost",
	// but the server cert is issued for the in-cluster Service DNS name.
	// Set ServerName so TLS verification succeeds in that case.
	// Note: this override assumes the default Flow Aggregator service name and namespace
	// (flow-aggregator.flow-aggregator.svc). If a non-default name/namespace is used,
	// port-forward verification will fail; users should configure a non-loopback address.
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		host = cfg.Address
	}
	isLocalhost := host == "localhost" || host == "localhost."
	if ip := net.ParseIP(host); (ip != nil && ip.IsLoopback()) || isLocalhost {
		if tlsCfg.RootCAs != nil {
			tlsCfg.ServerName = "flow-aggregator.flow-aggregator.svc"
		}
	}

	return tlsCfg, nil
}
