package readwriter

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go

type Interface interface {
	Read(ctx context.Context) (bool, []byte, []byte, error)
	Write(ctx context.Context, hash []byte, salt []byte) error
}
