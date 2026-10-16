package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/rpc"
)

func TestMapErrorRestoresSentinelsByCode(t *testing.T) {
	t.Parallel()

	for code, want := range map[codes.Code]error{
		codes.NotFound:         ErrNotFound,
		codes.InvalidArgument:  ErrBadRequest,
		codes.PermissionDenied: ErrForbidden,
		codes.Unavailable:      ErrUnavailable,
		codes.Aborted:          ErrUnavailable,
	} {
		received := status.Error(code, "open /data/a.txt: no such file")
		err := mapError(context.Background(), received)
		if !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", code, err, want)
		}
		if status.Code(rpc.Received(err)) != code {
			t.Fatalf("%s: received status is not on the chain: %v", code, rpc.Received(err))
		}
		if !errs.Analyze(context.Background(), err).Remote {
			t.Fatalf("%s: not marked remote", code)
		}
		// The bridge's message is the only description such a status has; an
		// agent tool reports it to the model.
		if !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("%s: text %q lost the bridge message", code, err.Error())
		}
	}
}

// A call that ends with Canceled while its context is live was closed by the
// connection or the bridge, not by the caller.
func TestMapErrorReadsCanceledAgainstTheCallContext(t *testing.T) {
	t.Parallel()

	received := status.Error(codes.Canceled, "grpc: the client connection is closing")
	if err := mapError(context.Background(), received); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("live context: got %v, want ErrUnavailable", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mapError(ctx, received)
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("ended context: got %v, want the canceled status", err)
	}
	if status.Code(err) != codes.Canceled {
		t.Fatalf("ended context: status = %v", status.Code(err))
	}
	if got := errs.Analyze(ctx, err).Fault; got != apperror.FaultCanceled {
		t.Fatalf("ended context: fault = %s, want canceled", got)
	}
}

// An unreachable workspace is the workspace runtime's failure. The public
// error a handler translates it to is attributed to the dependency.
func TestUnreachableWorkspaceIsADependencyFailure(t *testing.T) {
	t.Parallel()

	for name, received := range map[string]error{
		"unavailable":        status.Error(codes.Unavailable, "connection refused"),
		"aborted":            status.Error(codes.Aborted, "write aborted by sender"),
		"connection closing": status.Error(codes.Canceled, "grpc: the client connection is closing"),
	} {
		err := mapError(context.Background(), received)
		if got := errs.FaultOf(err); got != apperror.FaultDependency {
			t.Fatalf("%s: fault = %s, want dependency", name, got)
		}
		public := apperror.Wrap(apperror.CodeWorkspaceUnreachable, err, nil)
		if got := errs.FaultOf(public); got != apperror.FaultDependency {
			t.Fatalf("%s: workspace.unreachable fault = %s, want dependency", name, got)
		}
	}
}

func TestMapErrorKeepsOtherStatuses(t *testing.T) {
	t.Parallel()

	received := status.Error(codes.Internal, "write: no space left on device")
	err := mapError(context.Background(), received)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v, want Internal", status.Code(err))
	}
	if got := errs.Analyze(context.Background(), err); !got.Remote || got.Fault != apperror.FaultDependency {
		t.Fatalf("report = %+v, want a remote dependency failure", got)
	}
	if err := mapError(context.Background(), nil); err != nil {
		t.Fatalf("nil: %v", err)
	}
}
