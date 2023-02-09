package password

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go

type Store interface {
	Update(ctx context.Context, password []byte) error
	Compare(ctx context.Context, password []byte) error
}
