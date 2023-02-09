package password

import (
	"context"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hasher "antrea.io/antrea-ui/pkg/password/hasher/testing"
	readwriter "antrea.io/antrea-ui/pkg/password/readwriter/testing"
)

var (
	testHash = []byte("foo")
	testSalt = []byte("bar")
)

func setup(t *testing.T) (*hasher.MockInterface, *readwriter.MockInterface, *store) {
	ctrl := gomock.NewController(t)
	h := hasher.NewMockInterface(ctrl)
	rw := readwriter.NewMockInterface(ctrl)
	s := NewStore(rw, h)
	return h, rw, s
}

func TestInit(t *testing.T) {
	ctx := context.Background()

	t.Run("existing password", func(t *testing.T) {
		_, rw, s := setup(t)
		rw.EXPECT().Read(ctx).Return(true, testHash, testSalt, nil)
		require.NoError(t, s.Init(ctx))
	})

	t.Run("default password", func(t *testing.T) {
		h, rw, s := setup(t)
		rw.EXPECT().Read(ctx).Return(false, nil, nil, nil)
		h.EXPECT().Hash([]byte(defaultPassword), gomock.Any()).Return(testHash, nil)
		rw.EXPECT().Write(ctx, testHash, gomock.Any()).Return(nil)
		require.NoError(t, s.Init(ctx))
		assert.Len(t, s.cachedSalt, saltLength)
		assert.Equal(t, testHash, s.cachedHash)
	})
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	h, rw, s := setup(t)
	s.cachedSalt = testSalt
	newPassword := []byte("pswd")
	h.EXPECT().Hash(newPassword, testSalt).Return(testHash, nil)
	rw.EXPECT().Write(ctx, testHash, s.cachedSalt).Return(nil)
	require.NoError(t, s.Update(ctx, newPassword))
	assert.Equal(t, testHash, s.cachedHash)
}

func TestCompare(t *testing.T) {
	ctx := context.Background()

	t.Run("password match", func(t *testing.T) {
		h, _, s := setup(t)
		s.cachedSalt = testSalt
		s.cachedHash = testHash
		h.EXPECT().Hash([]byte("password1"), testSalt).Return(testHash, nil)
		require.NoError(t, s.Compare(ctx, []byte("password1")))
	})

	t.Run("password mismatch", func(t *testing.T) {
		h, _, s := setup(t)
		s.cachedSalt = testSalt
		s.cachedHash = testHash
		otherHash := make([]byte, len(testHash))
		copy(otherHash, testHash)
		otherHash[len(testHash)-1] ^= 0xff
		h.EXPECT().Hash([]byte("password2"), testSalt).Return(otherHash, nil)
		require.ErrorIs(t, s.Compare(ctx, []byte("password2")), InvalidPasswordErr)
	})

	t.Run("hash error", func(t *testing.T) {
		h, _, s := setup(t)
		s.cachedSalt = testSalt
		s.cachedHash = testHash
		h.EXPECT().Hash([]byte("password1"), testSalt).Return(testHash, fmt.Errorf("some error"))
		require.Error(t, s.Compare(ctx, []byte("password1")))
	})

	t.Run("uninitialized", func(t *testing.T) {
		_, _, s := setup(t)
		require.ErrorIs(t, s.Compare(ctx, []byte("password1")), NotInitializedErr)
	})
}
