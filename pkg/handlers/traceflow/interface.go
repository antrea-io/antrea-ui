package traceflow

import (
	"context"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go

type RequestsHandler interface {
	CreateRequest(ctx context.Context, request *Request) (string, error)
	GetRequestStatus(ctx context.Context, requestID string) (*RequestStatus, error)
	GetRequestResult(ctx context.Context, requestID string) (map[string]interface{}, error)
	DeleteRequest(ctx context.Context, requestID string) (bool, error)
}
