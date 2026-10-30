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

package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// ConnFor has no ephemeral (session-less) path, unlike TransportFor: a gRPC connection owns real
// resources that something has to close, and there is nowhere to cache a cleanup callback for a
// request with no session. It must fail closed rather than build (and leak) a connection per call.
func TestConnForFailsClosedForEphemeralAuth(t *testing.T) {
	ra := NewEphemeralAuth(Credential{Kind: KindBearer, Token: []byte("tok")}, "alice")
	built := false
	_, err := ra.ConnFor("some-key", func(cred *Credential) (*grpc.ClientConn, func(), error) {
		built = true
		return nil, nil, nil
	})
	assert.Error(t, err)
	assert.False(t, built, "must not even attempt to build a connection for an ephemeral request")
}

func TestConnForRequiresBuilder(t *testing.T) {
	ra := NewEphemeralAuth(Credential{Kind: KindBearer, Token: []byte("tok")}, "alice")
	_, err := ra.ConnFor("some-key", nil)
	require.Error(t, err)
}

// A session-backed request caches the connection under key, building it only once even if
// ConnFor is called again with the same key.
func TestConnForCachesPerSession(t *testing.T) {
	store := NewStore(testr.New(t), Options{IdleTimeout: time.Hour, MaxLifetime: time.Hour, MaxSessions: 10})
	sess, err := store.Create(&Spec{
		Mode:       ModeToken,
		Credential: Credential{Kind: KindCert, CertPEM: []byte("cert"), KeyPEM: []byte("key")},
	})
	require.NoError(t, err)
	ra := NewSessionAuth(store, sess)

	calls := 0
	build := func(cred *Credential) (*grpc.ClientConn, func(), error) {
		calls++
		return &grpc.ClientConn{}, nil, nil
	}

	conn1, err := ra.ConnFor("flow-aggregator-grpc", build)
	require.NoError(t, err)
	conn2, err := ra.ConnFor("flow-aggregator-grpc", build)
	require.NoError(t, err)

	assert.Same(t, conn1, conn2)
	assert.Equal(t, 1, calls)
}

func TestConnForRetiresSupersededVersions(t *testing.T) {
	store := NewStore(testr.New(t), Options{IdleTimeout: time.Hour, MaxLifetime: time.Hour, MaxSessions: 10})
	sess, err := store.Create(&Spec{Mode: ModeKubeconfig, Credential: Credential{Kind: KindCert}})
	require.NoError(t, err)
	t.Cleanup(func() { store.Delete(sess.ID()) })
	ra := NewSessionAuth(store, sess)
	closed := map[*grpc.ClientConn]int{}
	build := func(*Credential) (*grpc.ClientConn, func(), error) {
		// NewClient is lazy: these tests use real connection lifecycles without network I/O.
		conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, nil, err
		}
		closed[conn] = 0
		return conn, func() { closed[conn]++; require.NoError(t, conn.Close()) }, nil
	}
	get := func(key string) *grpc.ClientConn {
		conn, err := ra.ConnFor(key, build)
		require.NoError(t, err)
		return conn
	}
	legacy := get("unversioned")
	reader := get("flow-reader-grpc/0")
	live := get("flow-aggregator-grpc/0")
	for i := 1; i <= 10; i++ {
		previous := live
		key := fmt.Sprintf("flow-aggregator-grpc/%d", i)
		live = get(key)
		require.Equal(t, connectivity.Shutdown, previous.GetState())
		require.Equal(t, 1, closed[previous])
		require.Same(t, live, get(key))
		require.Len(t, sess.conns, 3, "rotations must not accumulate connections")
		require.Equal(t, connectivity.Idle, reader.GetState(), "live rotation must not close Reader")
		require.Equal(t, connectivity.Idle, legacy.GetState())
	}
	_, err = ra.ConnFor("flow-aggregator-grpc/failed", func(*Credential) (*grpc.ClientConn, func(), error) {
		return nil, nil, errors.New("invalid replacement credential")
	})
	require.Error(t, err)
	require.Equal(t, connectivity.Idle, live.GetState(), "a failed replacement must not close the working connection")
	require.Len(t, sess.conns, 3)
	get("flow-reader-grpc/1")
	require.Equal(t, connectivity.Shutdown, reader.GetState())
	require.Equal(t, connectivity.Idle, live.GetState())
	store.Delete(sess.ID())
	for _, n := range closed {
		require.Equal(t, 1, n, "every connection must be cleaned up exactly once")
	}
}
