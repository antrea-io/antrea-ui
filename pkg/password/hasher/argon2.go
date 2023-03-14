package hasher

import (
	"golang.org/x/crypto/argon2"
)

type Argon2id struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
}

func NewArgon2id() *Argon2id {
	return &Argon2id{
		time:    2,
		memory:  19 * 1024,
		threads: 1,
		keyLen:  32,
	}
}

func (h *Argon2id) Hash(password []byte, salt []byte) ([]byte, error) {
	hash := argon2.IDKey(password, salt, h.time, h.memory, h.threads, h.keyLen)
	return hash, nil
}
