package bridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type statTimeoutTestServer struct {
	pb.UnimplementedContainerServiceServer
}

func (*statTimeoutTestServer) Stat(context.Context, *pb.StatRequest) (*pb.StatResponse, error) {
	return nil, status.Error(codes.DeadlineExceeded, "sandbox provider timed out")
}

// A timeout the provider reports arrives as a DeadlineExceeded status, which
// is neither ErrUnavailable nor context.DeadlineExceeded on the error chain;
// IsProviderTimeout recognizes it. A deadline that expired in the caller's
// own code is not the workspace's answer.
func TestProviderTimeoutIsUnreachable(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &statTimeoutTestServer{})
	_, err := client.Stat(t.Context(), "/")
	if err == nil || !IsProviderTimeout(err) || !IsUnreachable(err) {
		t.Fatalf("Stat() error = %v, want a provider timeout", err)
	}
	if !IsUnreachable(fmt.Errorf("workspace client: %w", status.Error(codes.Unavailable, "runtime unreachable"))) {
		t.Fatal("an undecoded Unavailable status from a provider is not unreachable")
	}
	if IsUnreachable(fmt.Errorf("load bot container: %w", context.DeadlineExceeded)) {
		t.Fatal("a caller-side deadline reported as unreachable")
	}
	if IsUnreachable(mapError(context.Background(), status.Error(codes.NotFound, "missing"))) {
		t.Fatal("not found reported as unreachable")
	}
}

func TestWorkspaceAvailabilityIsTurnScoped(t *testing.T) {
	t.Parallel()

	unscoped := t.Context()
	if MarkWorkspaceUnavailableOn(unscoped, ErrUnavailable); WorkspaceUnavailableFromContext(unscoped) {
		t.Fatal("a context outside a turn recorded availability")
	}

	turn := WithWorkspaceAvailability(t.Context())
	if WithWorkspaceAvailability(turn) != turn {
		t.Fatal("WithWorkspaceAvailability replaced the turn's state")
	}
	if MarkWorkspaceUnavailableOn(turn, fmt.Errorf("read: %w", ErrNotFound)) || WorkspaceUnavailableFromContext(turn) {
		t.Fatal("a failed request marked the workspace unavailable")
	}
	if MarkWorkspaceUnavailableOn(turn, errors.New("boom")) {
		t.Fatal("an unclassified failure marked the workspace unavailable")
	}
	child, cancel := context.WithCancel(turn)
	defer cancel()
	if !MarkWorkspaceUnavailableOn(child, fmt.Errorf("read: %w", status.Error(codes.DeadlineExceeded, "sandbox provider timed out"))) {
		t.Fatal("a provider timeout did not mark the workspace unavailable")
	}
	if !WorkspaceUnavailableFromContext(turn) {
		t.Fatal("a later stage of the turn does not see the verdict")
	}

	canceled, cancelTurn := context.WithCancel(WithWorkspaceAvailability(t.Context()))
	cancelTurn()
	if MarkWorkspaceUnavailableOn(canceled, ErrUnavailable) {
		t.Fatal("a canceled turn marked the workspace unavailable")
	}
}

// Work that outlives a turn, such as a background task, opens availability
// state of its own: a verdict reached on either side stays there.
func TestWithNewWorkspaceAvailabilityIsolatesScopes(t *testing.T) {
	t.Parallel()

	turn := WithWorkspaceAvailability(t.Context())
	task := WithNewWorkspaceAvailability(context.WithoutCancel(turn))
	if task == turn || WithWorkspaceAvailability(task) != task {
		t.Fatal("WithNewWorkspaceAvailability did not open a scope of its own")
	}
	MarkWorkspaceUnavailable(task)
	if WorkspaceUnavailableFromContext(turn) {
		t.Fatal("a background task's verdict reached the parent turn")
	}
	MarkWorkspaceUnavailable(turn)
	child := WithNewWorkspaceAvailability(turn)
	if WorkspaceUnavailableFromContext(child) {
		t.Fatal("a new scope inherited the parent turn's verdict")
	}
}
