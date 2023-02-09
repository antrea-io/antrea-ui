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
