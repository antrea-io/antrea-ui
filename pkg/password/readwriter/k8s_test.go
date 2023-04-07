// Copyright 2023 Antrea Authors.
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

package readwriter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestReadWrite(t *testing.T) {
	const (
		namespace = "kube-system"
		name      = "my-secret"
	)
	var (
		testSalt  = []byte("salt")
		testHash1 = []byte("pswd1")
		testHash2 = []byte("pswd2")
	)
	ctx := context.Background()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	rw := NewK8sSecret(namespace, name, client)
	ok, _, _, err := rw.Read(ctx)
	require.NoError(t, err)
	assert.False(t, ok)
	// create
	require.NoError(t, rw.Write(ctx, testHash1, testSalt))
	ok, hash, salt, err := rw.Read(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, testHash1, hash)
	assert.Equal(t, testSalt, salt)
	// update
	require.NoError(t, rw.Write(ctx, testHash2, testSalt))
	ok, hash, salt, err = rw.Read(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, testHash2, hash)
	assert.Equal(t, testSalt, salt)
}
