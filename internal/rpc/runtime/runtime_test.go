package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/rpc/runtimepb"
)

func callWith(t *testing.T, handlerErr error) error {
	t.Helper()
	srv := NewServer(nil, map[string]Handler{
		"m": func(context.Context, json.RawMessage) (any, error) { return nil, handlerErr },
	})
	_, err := srv.Call(context.Background(), &runtimepb.CallRequest{Method: "m"})
	return err
}

// TestCallSanitizesNestedStatusErrors: a gRPC status buried in a wrap chain
// (e.g. a workspace-bridge Unavailable from a stopped bot container) is a
// downstream detail, not this layer's wire vocabulary — passing it through
// would make the peer misdiagnose a per-bot failure as a server↔channel
// link outage.
func TestCallSanitizesNestedStatusErrors(t *testing.T) {
	nested := fmt.Errorf("list skills from container: %w", status.Error(codes.Unavailable, "bridge is down"))
	err := callWith(t, nested)
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want Internal", got)
	}
	if msg := status.Convert(err).Message(); strings.Contains(msg, "bridge is down") {
		t.Fatalf("nested detail leaked: %q", msg)
	}
}

// TestCallPassesThroughDirectStatus: statuses constructed by the handler
// layer itself (reason tokens) are intentional and cross unchanged.
func TestCallPassesThroughDirectStatus(t *testing.T) {
	err := callWith(t, status.Error(codes.NotFound, "channel.config_not_found"))
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", got)
	}
	if got := status.Convert(err).Message(); got != "channel.config_not_found" {
		t.Fatalf("message = %q", got)
	}
}

// TestCallTransportsPublicErrors: Public marks operator-facing adapter
// errors for verbatim transport.
func TestCallTransportsPublicErrors(t *testing.T) {
	err := callWith(t, Public(errors.New("telegram: chat not found")))
	if got := status.Code(err); got != codes.Unknown {
		t.Fatalf("code = %v, want Unknown", got)
	}
	if got := status.Convert(err).Message(); got != "telegram: chat not found" {
		t.Fatalf("message = %q", got)
	}
}

func TestPublicNilIsNil(t *testing.T) {
	if Public(nil) != nil {
		t.Fatal("Public(nil) must be nil")
	}
}

// TestCallSanitizesPlainErrors keeps the default: unmarked errors stay
// opaque.
func TestCallSanitizesPlainErrors(t *testing.T) {
	err := callWith(t, errors.New("private database detail"))
	if got := status.Code(err); got != codes.Internal {
		t.Fatalf("code = %v, want Internal", got)
	}
	if msg := status.Convert(err).Message(); strings.Contains(msg, "private") {
		t.Fatalf("detail leaked: %q", msg)
	}
}
