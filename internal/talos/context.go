package talos

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// IsContextCancelled returns true if err represents a context cancellation or deadline,
// including gRPC status errors that wrap context cancellation from the server side.
func IsContextCancelled(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if s, ok := status.FromError(err); ok {
		return s.Code() == codes.Canceled || s.Code() == codes.DeadlineExceeded
	}
	return false
}

func IsCancelled(ctx context.Context) bool {
	return ctx.Err() != nil
}
