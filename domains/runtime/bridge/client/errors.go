package client

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("unavailable")
	ErrBadRequest  = errors.New("invalid argument")
	ErrForbidden   = errors.New("permission denied")
)

// mapError converts a gRPC status error into a domain error.
// Non-gRPC errors pass through unchanged.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	s, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := s.Message()
	switch s.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrBadRequest, msg)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: %s", ErrForbidden, msg)
	case codes.Unavailable, codes.Aborted:
		return fmt.Errorf("%w: %s", ErrUnavailable, msg)
	case codes.Canceled:
		if isConnectionClosingMessage(msg) {
			return fmt.Errorf("%w: %s", ErrUnavailable, msg)
		}
		return fmt.Errorf("grpc %s: %s", s.Code(), msg)
	default:
		return fmt.Errorf("grpc %s: %s", s.Code(), msg)
	}
}

func isConnectionClosingMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "client connection is closing") ||
		strings.Contains(lower, "transport is closing") ||
		strings.Contains(lower, "use of closed network connection")
}
