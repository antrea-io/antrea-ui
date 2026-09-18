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
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
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
