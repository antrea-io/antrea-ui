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
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/madflojo/testcerts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// fakeFlowStreamServer lets each test script the GetFlows behavior it needs, and records the
// bearer token (if any) each call was made with.
type fakeFlowStreamServer struct {
	flowpb.UnimplementedFlowStreamServiceServer
	// handle decides what a GetFlows call returns. A non-nil error is returned before anything
	// is sent, the way FA rejects a call that fails authentication or authorization.
	handle func(callNum int, bearer string) error
	// send, when set, is called once handle returns nil, in place of sending
	// firstResponse, and can put any messages (or none) on the stream.
	send  func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error
	calls atomic.Int32
	// peerDNSNames captures the DNS SAN names of the TLS client certificate presented on the
	// connection, if any. Only the KindCert test reads it.
	peerDNSNames atomic.Value
}

func (f *fakeFlowStreamServer) GetFlows(req *flowpb.GetFlowsRequest, stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
	bearer := ""
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			bearer = vals[0]
		}
	}
	callNum := int(f.calls.Add(1))
	if p, ok := peer.FromContext(stream.Context()); ok {
		if tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(tlsInfo.State.PeerCertificates) > 0 {
			f.peerDNSNames.Store(tlsInfo.State.PeerCertificates[0].DNSNames)
		}
	}
	if err := f.handle(callNum, bearer); err != nil {
		return err
	}
	if f.send != nil {
		return f.send(stream)
	}
	return stream.Send(firstResponse)
}

// firstResponse is what a Flow Aggregator with per-user flow authorization sends first on every
// stream: no flows, and a resume_token carrying its stream epoch.
var firstResponse = &flowpb.GetFlowsResponse{
	ResumeToken: &flowpb.ResumeToken{StreamEpoch: "test-epoch", SequenceNumber: -1},
}

// newTestSubscriber starts fake on an in-memory bufconn listener and returns a
// GRPCFlowStreamSubscriber dialed against it with no TLS, bypassing the real constructor (which
// requires real TLS credentials).
//
// extraDialOpts are appended to the client's dial options, for a test that needs to change how the
// client itself behaves (see TestSubscribeReportsOversizedMessageAsNotRetryable).
func newTestSubscriber(t *testing.T, fake *fakeFlowStreamServer, extraDialOpts ...grpc.DialOption) *GRPCFlowStreamSubscriber {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	flowpb.RegisterFlowStreamServiceServer(srv, fake)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	dialOpts := append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, extraDialOpts...)
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpts...)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return &GRPCFlowStreamSubscriber{
		logger: testr.New(t),
		client: flowpb.NewFlowStreamServiceClient(conn),
		conn:   conn,
	}
}

// newCertTestSubscriber starts fake on a real TCP listener secured with serverCert and requiring
// a client certificate signed by ca, and returns a GRPCFlowStreamSubscriber whose tlsConfig
// trusts ca. Unlike newTestSubscriber, this cannot use bufconn: buildCertConn dials h.address
// directly, with no injectable dialer, so the KindCert path needs a real address to connect to.
func newCertTestSubscriber(t *testing.T, fake *fakeFlowStreamServer, ca *testcerts.CertificateAuthority, serverCert tls.Certificate, serverName string) *GRPCFlowStreamSubscriber {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.CertPool(),
	})))
	flowpb.RegisterFlowStreamServiceServer(srv, fake)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)
	t.Cleanup(func() { lis.Close() })

	return &GRPCFlowStreamSubscriber{
		logger:    testr.New(t),
		address:   lis.Addr().String(),
		tlsConfig: &tls.Config{RootCAs: ca.CertPool(), ServerName: serverName},
	}
}

func ctxWithSessionAuth(t *testing.T, store session.Store, spec *session.Spec) (context.Context, *session.RequestAuth) {
	t.Helper()
	sess, err := store.Create(spec)
	require.NoError(t, err)
	ra := session.NewSessionAuth(store, sess)
	return session.WithRequestAuth(t.Context(), ra), ra
}

func newTestStore(t *testing.T) session.Store {
	t.Helper()
	return session.NewStore(testr.New(t), session.Options{
		IdleTimeout: time.Hour,
		MaxLifetime: time.Hour,
		MaxSessions: 10,
	})
}

// A bearer credential must be attached as "authorization: Bearer <token>" call metadata: that is
// the only shape FA accepts a token in.
func TestSubscribeAttachesBearerToken(t *testing.T) {
	var gotBearer atomic.Value
	fake := &fakeFlowStreamServer{handle: func(_ int, bearer string) error {
		gotBearer.Store(bearer)
		return nil
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("s3cr3t")},
	})

	_, errCh, readyCh := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		if ok {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream to finish")
	}
	// The fake server closes the stream immediately (handle returns nil), so calls should be 1.
	assert.Equal(t, int32(1), fake.calls.Load())
	assert.Equal(t, "Bearer s3cr3t", gotBearer.Load())
	// readyCh must already be closed by the time errCh reports the stream ended - the caller
	// deciding whether to commit to a 200 (see StreamFlows) reads it as "the call cleared auth".
	select {
	case _, ok := <-readyCh:
		assert.False(t, ok, "readyCh must be closed, not merely have a value")
	default:
		t.Fatal("readyCh was not closed for a stream that cleared authentication")
	}
}

// A failure resolved before any call reaches FA (see resolveCall) must never signal readiness:
// StreamFlows relies on readyCh only ever closing for a stream that is actually live, so a
// caller racing errCh against it never mistakes this kind of failure for success.
func TestSubscribeReadyNeverClosesWithoutResolvedIdentity(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)

	_, errCh, readyCh := h.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	select {
	case <-readyCh:
		t.Fatal("readyCh must not close on a failure that never reached FA")
	case <-time.After(50 * time.Millisecond):
	}
}

// Subscribe must fail closed, without dialing anything, when the context carries no resolved
// identity: this is a wiring bug, not something a client can trigger, but the flow stream
// endpoint runs for hours and must not guess a credential.
func TestSubscribeFailsWithoutResolvedIdentity(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)

	_, errCh, _ := h.Subscribe(t.Context(), &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load(), "must not have dialed GetFlows with no resolved identity")
}

// codes.Unauthenticated means FA rejected the credential itself, but that must not invalidate the
// backing session the way an upstream 401 from the kube-apiserver would: FA is a different
// server, trusting a different CA and potentially a different token audience, so a credential FA
// rejects may still be perfectly valid for every other antrea-ui call. Ending the whole UI session
// over it would log the user out of unrelated pages, on every re-login, if that mismatch is just
// how the deployment is configured.
func TestSubscribeDoesNotInvalidateSessionOnUnauthenticated(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return status.Error(codes.Unauthenticated, "no credential")
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, ra := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("bad")},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err := <-errCh:
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}

	_, err := store.Get(t.Context(), ra.SessionID())
	assert.NoError(t, err, "an FA-rejected credential must not end the antrea-ui session")
}

// codes.ResourceExhausted (the stream limiter, or FA's token-auth semaphore) is reported to the
// caller as a retryable StreamError rather than retried here: retrying is the frontend's job,
// driven by its own reconnect backoff (see startStream's doc comment).
func TestSubscribeReportsResourceExhaustedAsRetryable(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return status.Error(codes.ResourceExhausted, "at capacity")
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeResourceExhausted, streamErr.Code)
		assert.True(t, streamErr.Retryable)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Equal(t, int32(1), fake.calls.Load(), "must not retry on its own")
}

// grpc-go raises codes.ResourceExhausted in the *client* when a response exceeds the configured
// receive limit (4 MiB by default), which is a fixed mismatch between FA's batch size and this
// client's limit - nothing like FA being at capacity. Reporting it as capacity would both retry a
// batch that cannot ever fit and point whoever debugs it at FA's load instead of the size limit.
func TestSubscribeReportsOversizedMessageAsNotRetryable(t *testing.T) {
	// Comfortably past the 1 KiB receive limit set below, so the client rejects the response
	// before it is ever handed to forwardResp.
	oversized := &flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{{Id: strings.Repeat("x", 4096)}}}
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return nil
	}}
	fake.send = func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
		return stream.Send(oversized)
	}
	h := newTestSubscriber(t, fake, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1024)))

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeInternal, streamErr.Code)
		assert.False(t, streamErr.Retryable)
		assert.NotContains(t, streamErr.Error(), "at capacity")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
}

// The counterpart of TestSubscribeReportsMissingStreamEpochAsFlowAggregatorTooOld: once the first
// response carries a stream epoch, Subscribe must signal ready and forward the flows that follow.
func TestSubscribeForwardsFlowsAfterStreamEpoch(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return nil
	}}
	fake.send = func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
		if err := stream.Send(firstResponse); err != nil {
			return err
		}
		return stream.Send(&flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{{Id: "flow-1"}}})
	}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})

	flowsCh, errCh, readyCh := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case <-readyCh:
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ready")
	}
	select {
	case evt, ok := <-flowsCh:
		require.True(t, ok, "the flow after the first response must be forwarded")
		require.Len(t, evt.Flows, 1)
		assert.Equal(t, "flow-1", evt.Flows[0].ID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the flow")
	}
	_, ok := <-flowsCh
	assert.False(t, ok, "only the flow record must be forwarded, not the first response")
	err, ok := <-errCh
	assert.False(t, ok, "unexpected error: %v", err)
}

// A Flow Aggregator that predates per-user flow authorization ignores the stream's scope and
// cannot produce a stream epoch, so a first response without one must be read as "too old" and
// the stream refused rather than passed on: forwarding it would show the caller cluster-wide,
// full-identity data regardless of the scope it asked for. That holds whatever else the first
// response carries: an old Flow Aggregator's first response is not always a flow record.
func TestSubscribeReportsMissingStreamEpochAsFlowAggregatorTooOld(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first *flowpb.GetFlowsResponse
	}{
		{
			name:  "flow record",
			first: &flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{{Id: "flow-1"}}},
		},
		{
			// The ring buffer wrapped before the first read, and every record read was
			// filtered out.
			name:  "dropped count only",
			first: &flowpb.GetFlowsResponse{DroppedCount: 5},
		},
		{
			// A Flow Aggregator built between per-user authorization and stream epochs,
			// which never shipped in a release.
			name:  "empty",
			first: &flowpb.GetFlowsResponse{},
		},
		{
			name:  "resume token with no epoch",
			first: &flowpb.GetFlowsResponse{ResumeToken: &flowpb.ResumeToken{SequenceNumber: -1}},
		},
		{
			// The stream closed with no response: a non-follow request to an old Flow
			// Aggregator with nothing buffered.
			name:  "no response",
			first: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
				return nil
			}}
			fake.send = func(stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
				if tc.first == nil {
					return nil
				}
				if err := stream.Send(tc.first); err != nil {
					return err
				}
				return stream.Send(&flowpb.GetFlowsResponse{Flows: []*flowpb.Flow{{Id: "flow-2"}}})
			}
			h := newTestSubscriber(t, fake)

			store := newTestStore(t)
			ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
				Mode:       session.ModeToken,
				Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
			})

			flowsCh, errCh, readyCh := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
			select {
			case err, ok := <-errCh:
				require.True(t, ok)
				var streamErr *StreamError
				require.ErrorAs(t, err, &streamErr)
				assert.Equal(t, StreamErrorCodeFlowAggregatorTooOld, streamErr.Code)
				assert.False(t, streamErr.Retryable)
			case <-readyCh:
				t.Fatal("must not signal ready for a Flow Aggregator with no stream epoch")
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for error")
			}
			_, ok := <-flowsCh
			assert.False(t, ok, "no flow record must be forwarded to the caller")
		})
	}
}

// codes.Unavailable, and any other gRPC code with no case of its own in classifyStreamErr, is what
// grpc-go returns for an ordinary transient failure - FA restarting, rolling, or a network blip -
// and before this package existed, the frontend's own reconnect loop recovered from exactly this.
// It must still be reported as retryable now that classifyStreamErr owns the decision.
func TestSubscribeReportsUnknownCodeAsRetryable(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return status.Error(codes.Unavailable, "connection reset")
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeInternal, streamErr.Code)
		assert.True(t, streamErr.Retryable)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
}

// codes.Unauthenticated is reported as a non-retryable StreamError: the same credential will be
// rejected again, so the frontend must stop rather than keep reconnecting into it.
func TestSubscribeReportsUnauthenticatedAsNotRetryable(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		return status.Error(codes.Unauthenticated, "no credential")
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("bad")},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.Equal(t, StreamErrorCodeUnauthenticated, streamErr.Code)
		assert.False(t, streamErr.Retryable)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
}

// An ordinary client disconnect (tab closed, filter changed) while startStream is still waiting
// for the first response - which authentication and authorization can delay - must not surface
// as a stream error: it is indistinguishable from every other client-initiated teardown.
func TestSubscribeStopsSilentlyOnDisconnectDuringFirstRecv(t *testing.T) {
	reached := make(chan struct{})
	never := make(chan struct{})
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		close(reached)
		<-never // block until the test cancels ctx; GetFlows call succeeds but no response ever arrives.
		return nil
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	baseCtx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})
	ctx, cancel := context.WithCancel(baseCtx)

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fake server to be called")
	}
	cancel()

	select {
	case _, ok := <-errCh:
		assert.False(t, ok, "a client disconnect while waiting for the first response must not be reported as an error")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream to stop")
	}
}

// A credential kind FA does not support (session.KindImpersonate with no admin token source
// configured) must fail closed rather than silently calling with no credential at all.
func TestSubscribeFailsForImpersonateWithoutAdminTokenSource(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeAdmin,
		Credential: session.Credential{Kind: session.KindImpersonate, UserName: "antrea-ui-admin"},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load())
}

// Most ways CreateToken can fail are transient (the mint timeout firing, an apiserver 5xx,
// client-side throttling), so a session that opens the flow page during a brief apiserver hiccup
// must be told to retry rather than stopped for good.
func TestSubscribeReportsAdminTokenMintFailureAsRetryable(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)
	h.adminTokenSource = NewAdminTokenSource(newContextAwareClientset(func(context.Context, string, *authenticationv1.TokenRequest, metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
		return nil, fmt.Errorf("apiserver unavailable")
	}), "ns", "antrea-ui-admin")

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeAdmin,
		Credential: session.Credential{Kind: session.KindImpersonate, UserName: "antrea-ui-admin"},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.True(t, streamErr.Retryable)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load())
}

// A missing serviceaccounts/token grant or a deleted antrea-ui-admin ServiceAccount needs an
// operator to fix, so retrying the same request will not help - unlike the generic mint failure
// in TestSubscribeReportsAdminTokenMintFailureAsRetryable.
func TestSubscribeReportsForbiddenAdminTokenMintAsNotRetryable(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)
	h.adminTokenSource = NewAdminTokenSource(newContextAwareClientset(func(context.Context, string, *authenticationv1.TokenRequest, metav1.CreateOptions) (*authenticationv1.TokenRequest, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts/token"}, "antrea-ui-admin", fmt.Errorf("no create verb"))
	}), "ns", "antrea-ui-admin")

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeAdmin,
		Credential: session.Credential{Kind: session.KindImpersonate, UserName: "antrea-ui-admin"},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		var streamErr *StreamError
		require.ErrorAs(t, err, &streamErr)
		assert.False(t, streamErr.Retryable)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load())
}

// A KindCert credential is presented as the TLS client certificate on its own connection, dialed
// by buildCertConn via resolveCall's KindCert branch and connFor's per-session cache - nothing
// else in this file, or in context_test.go's generic connFor coverage with a stub builder,
// exercises that path end to end. It does not cover a nil h.tlsConfig: see
// TestBuildCertConnRejectsNilTLSConfig for that.
func TestSubscribeUsesClientCertForKindCert(t *testing.T) {
	ca := testcerts.NewCA()

	const serverName = "flow-aggregator-test"
	serverKP, err := ca.NewKeyPair(serverName)
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(serverKP.PublicKey(), serverKP.PrivateKey())
	require.NoError(t, err)

	const clientName = "flow-viewer-client"
	clientKP, err := ca.NewKeyPair(clientName)
	require.NoError(t, err)

	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newCertTestSubscriber(t, fake, ca, serverCert, serverName)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode: session.ModeKubeconfig,
		Credential: session.Credential{
			Kind:    session.KindCert,
			CertPEM: clientKP.PublicKey(),
			KeyPEM:  clientKP.PrivateKey(),
		},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		if ok {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream to finish")
	}
	assert.Equal(t, int32(1), fake.calls.Load())
	names, _ := fake.peerDNSNames.Load().([]string)
	assert.Contains(t, names, clientName, "FA must see the credential's own client certificate on the connection")
}

// When a KindCert session ends (logout, eviction, GC), Session.zero closes the per-session
// ClientConn the stream is running on. The resulting Recv error (codes.Canceled) must be treated
// as a quiet disconnect, the same as an ordinary client-initiated teardown, not surfaced as a
// stream error - a normal logout should not show the user a flow-stream error.
func TestSubscribeStopsSilentlyWhenSessionEndsDuringKindCertStream(t *testing.T) {
	ca := testcerts.NewCA()

	const serverName = "flow-aggregator-test"
	serverKP, err := ca.NewKeyPair(serverName)
	require.NoError(t, err)
	serverCert, err := tls.X509KeyPair(serverKP.PublicKey(), serverKP.PrivateKey())
	require.NoError(t, err)

	clientKP, err := ca.NewKeyPair("flow-viewer-client")
	require.NoError(t, err)

	reached := make(chan struct{})
	never := make(chan struct{})
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error {
		close(reached)
		<-never // block until the test ends the session; no response ever arrives.
		return nil
	}}
	h := newCertTestSubscriber(t, fake, ca, serverCert, serverName)

	store := newTestStore(t)
	ctx, ra := ctxWithSessionAuth(t, store, &session.Spec{
		Mode: session.ModeKubeconfig,
		Credential: session.Credential{
			Kind:    session.KindCert,
			CertPEM: clientKP.PublicKey(),
			KeyPEM:  clientKP.PrivateKey(),
		},
	})

	_, errCh, _ := h.Subscribe(ctx, &FlowStreamScope{ClusterWide: true}, &FlowStreamFilter{})
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fake server to be called")
	}

	// Simulate a normal logout: ctx stays alive, but this closes the per-session ClientConn the
	// stream is running on (Session.zero -> dropTransportsLocked).
	store.Delete(ra.SessionID())

	select {
	case _, ok := <-errCh:
		assert.False(t, ok, "a session ending must not surface as a stream error, like an ordinary disconnect")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream to stop")
	}
}

// NewGRPCFlowStreamSubscriber must reject a nil TLSConfig rather than construct a subscriber that
// nil-panics later: (*tls.Config).Clone returns nil for a nil receiver, so buildCertConn's use of
// h.tlsConfig.Clone() would nil-panic inside the Subscribe goroutine, which has no recover().
func TestNewGRPCFlowStreamSubscriberRejectsNilTLSConfig(t *testing.T) {
	_, err := NewGRPCFlowStreamSubscriber(testr.New(t), GRPCConfig{Address: "127.0.0.1:0"})
	assert.Error(t, err)
}

// Defense in depth for the same nil-panic, for a GRPCFlowStreamSubscriber built directly (as the
// other tests in this file do) with tlsConfig left unset.
func TestBuildCertConnRejectsNilTLSConfig(t *testing.T) {
	h := &GRPCFlowStreamSubscriber{logger: testr.New(t), address: "127.0.0.1:0"}
	_, _, err := h.buildCertConn(&session.Credential{})
	assert.Error(t, err)
}
