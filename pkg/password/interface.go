package password

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type Store interface {
	Update(ctx context.Context, password []byte) error
	Compare(ctx context.Context, password []byte) error
}
