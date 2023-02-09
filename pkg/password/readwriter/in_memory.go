package readwriter

import (
	"context"
	"sync"
)

type InMemory struct {
	sync.Mutex
	set  bool
	hash []byte
	salt []byte
}

func (rw *InMemory) Read(ctx context.Context) (bool, []byte, []byte, error) {
	rw.Lock()
	defer rw.Unlock()
	if !rw.set {
		return false, nil, nil, nil
	}
	return true, rw.hash, rw.salt, nil
}

func (rw *InMemory) Write(ctx context.Context, hash []byte, salt []byte) error {
	rw.Lock()
	defer rw.Unlock()
	rw.hash = hash
	rw.salt = salt
	rw.set = true
	return nil
}

func NewInMemory() *InMemory {
	return &InMemory{}
}
