package client

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMapErrorTreatsClosingConnectionAsUnavailable(t *testing.T) {
	t.Parallel()

	err := mapError(status.Error(codes.Canceled, "grpc: the client connection is closing"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestMapErrorKeepsNonTransportCanceledAsCanceled(t *testing.T) {
	t.Parallel()

	err := mapError(status.Error(codes.Canceled, "operation canceled"))
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("did not expect ErrUnavailable, got %v", err)
	}
	if got := err.Error(); got != "grpc Canceled: operation canceled" {
		t.Fatalf("unexpected mapped error: %q", got)
	}
}
