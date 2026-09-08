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
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"antrea.io/antrea-ui/pkg/auth/session"
	flowpb "antrea.io/antrea-ui/pkg/flowpb"
)

// fakeFlowStreamServer lets each test script the GetFlows behavior it needs, and records the
// bearer token (if any) each call was made with.
type fakeFlowStreamServer struct {
	flowpb.UnimplementedFlowStreamServiceServer
	// handle is called for every GetFlows call; it decides what the call returns.
	handle func(callNum int, bearer string) error
	calls  atomic.Int32
}

func (f *fakeFlowStreamServer) GetFlows(_ *flowpb.GetFlowsRequest, stream grpc.ServerStreamingServer[flowpb.GetFlowsResponse]) error {
	callNum := int(f.calls.Add(1))
	bearer := ""
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			bearer = vals[0]
		}
	}
	return f.handle(callNum, bearer)
}

// newTestSubscriber starts fake on an in-memory bufconn listener and returns a
// GRPCFlowStreamSubscriber dialed against it with no TLS, bypassing the real constructor (which
// requires real TLS credentials).
func newTestSubscriber(t *testing.T, fake *fakeFlowStreamServer) *GRPCFlowStreamSubscriber {
	t.Helper()
	return newTestSubscriberWithConcurrency(t, fake, 4)
}

func newTestSubscriberWithConcurrency(t *testing.T, fake *fakeFlowStreamServer, maxConcurrent int) *GRPCFlowStreamSubscriber {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	flowpb.RegisterFlowStreamServiceServer(srv, fake)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return &GRPCFlowStreamSubscriber{
		logger:                   testr.New(t),
		client:                   flowpb.NewFlowStreamServiceClient(conn),
		conn:                     conn,
		sem:                      make(chan struct{}, maxConcurrent),
		resourceExhaustedBackoff: 20 * time.Millisecond,
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

	_, errCh := h.Subscribe(ctx, &FlowStreamFilter{})
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
}

// Subscribe must fail closed, without dialing anything, when the context carries no resolved
// identity: this is a wiring bug, not something a client can trigger, but the flow stream
// endpoint runs for hours and must not guess a credential.
func TestSubscribeFailsWithoutResolvedIdentity(t *testing.T) {
	fake := &fakeFlowStreamServer{handle: func(_ int, _ string) error { return nil }}
	h := newTestSubscriber(t, fake)

	_, errCh := h.Subscribe(t.Context(), &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load(), "must not have dialed GetFlows with no resolved identity")
}

// codes.Unauthenticated means FA rejected the credential itself - a credential problem, not an FA
// outage - so it must invalidate the backing session the same way an upstream 401 from the
// kube-apiserver would: FA is a different server, trusting a different CA and potentially a
// different token audience, so a credential FA rejects may still be perfectly valid for every
// other antrea-ui call. Ending the whole UI session over it would log the user out of unrelated
// pages, on every re-login, if that mismatch is just how the deployment is configured.
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

	_, errCh := h.Subscribe(ctx, &FlowStreamFilter{})
	select {
	case err := <-errCh:
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}

	_, err := store.Get(t.Context(), ra.SessionID())
	assert.NoError(t, err, "an FA-rejected credential must not end the antrea-ui session")
}

// codes.ResourceExhausted (the stream limiter, or FA's token-auth semaphore) is retryable: the
// call must succeed once FA has capacity again rather than failing the stream outright.
func TestSubscribeRetriesOnResourceExhausted(t *testing.T) {
	const failuresBeforeSuccess = 2
	fake := &fakeFlowStreamServer{handle: func(callNum int, _ string) error {
		if callNum <= failuresBeforeSuccess {
			return status.Error(codes.ResourceExhausted, "at capacity")
		}
		return nil
	}}
	h := newTestSubscriber(t, fake)

	store := newTestStore(t)
	ctx, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok")},
	})

	_, errCh := h.Subscribe(ctx, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		if ok {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retry to succeed")
	}
	assert.Equal(t, int32(failuresBeforeSuccess+1), fake.calls.Load())
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

	_, errCh := h.Subscribe(ctx, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh:
		require.True(t, ok)
		assert.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for error")
	}
	assert.Zero(t, fake.calls.Load())
}

// A caller that arrives once antrea-ui is already at its own concurrency cap must get an
// immediate, clear error - not silently hang waiting for a slot, which would look like a stuck
// connection (keepalive comments, no data, no error) instead of the capacity problem it is.
func TestSubscribeFailsImmediatelyWhenAtCapacity(t *testing.T) {
	holdFirstCall := make(chan struct{})
	fake := &fakeFlowStreamServer{handle: func(callNum int, _ string) error {
		if callNum == 1 {
			<-holdFirstCall
		}
		return nil
	}}
	h := newTestSubscriberWithConcurrency(t, fake, 1)
	store := newTestStore(t)

	ctx1, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok1")},
	})
	_, errCh1 := h.Subscribe(ctx1, &FlowStreamFilter{})

	// Wait for the first call to actually reach the fake server (and so hold the semaphore)
	// before starting the second one.
	require.Eventually(t, func() bool { return fake.calls.Load() >= 1 }, time.Second, time.Millisecond)

	ctx2, _ := ctxWithSessionAuth(t, store, &session.Spec{
		Mode:       session.ModeToken,
		Credential: session.Credential{Kind: session.KindBearer, Token: []byte("tok2")},
	})
	start := time.Now()
	_, errCh2 := h.Subscribe(ctx2, &FlowStreamFilter{})
	select {
	case err, ok := <-errCh2:
		require.True(t, ok)
		assert.Error(t, err)
		assert.Less(t, time.Since(start), 500*time.Millisecond, "an at-capacity caller must fail immediately, not block waiting for a slot")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the at-capacity error")
	}
	assert.Equal(t, int32(1), fake.calls.Load(), "the second call must never reach the server")

	close(holdFirstCall)
	select {
	case _, ok := <-errCh1:
		assert.False(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first stream to finish")
	}
}
