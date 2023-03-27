package readwriter

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type Interface interface {
	Read(ctx context.Context) (bool, []byte, []byte, error)
	Write(ctx context.Context, hash []byte, salt []byte) error
}
