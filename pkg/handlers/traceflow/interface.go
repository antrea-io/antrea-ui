package traceflow

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

type RequestsHandler interface {
	CreateRequest(ctx context.Context, request *Request) (string, error)
	// GetRequestResult returns the Traceflow object, and a boolean to indicate whether the
	// Traceflow request is completed. Completed means that the Traceflow Status has either been
	// updated to "Succeeded" or "Failed".
	GetRequestResult(ctx context.Context, requestID string) (map[string]interface{}, bool, error)
	DeleteRequest(ctx context.Context, requestID string) (bool, error)
}
