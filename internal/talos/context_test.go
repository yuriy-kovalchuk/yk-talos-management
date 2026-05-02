package talos

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsContextCancelled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("wrapped: %w", context.Canceled),
			want: true,
		},
		{
			name: "wrapped context.DeadlineExceeded",
			err:  fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			want: true,
		},
		{
			name: "gRPC Canceled status",
			err:  status.Error(codes.Canceled, "operation canceled"),
			want: true,
		},
		{
			name: "gRPC DeadlineExceeded status",
			err:  status.Error(codes.DeadlineExceeded, "deadline exceeded"),
			want: true,
		},
		{
			name: "gRPC other code",
			err:  status.Error(codes.Internal, "internal error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "random error",
			err:  errors.New("something went wrong"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsContextCancelled(tt.err)
			if got != tt.want {
				t.Errorf("IsContextCancelled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsCancelled(t *testing.T) {
	t.Run("canceled context returns true", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if !IsCancelled(ctx) {
			t.Error("expected IsCancelled() = true for canceled context")
		}
	})

	t.Run("deadline exceeded context returns true", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()

		if !IsCancelled(ctx) {
			t.Error("expected IsCancelled() = true for deadline exceeded")
		}
	})

	t.Run("active context returns false", func(t *testing.T) {
		ctx := context.Background()

		if IsCancelled(ctx) {
			t.Error("expected IsCancelled() = false for active context")
		}
	})
}