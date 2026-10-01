package errs

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

func remoteStatus(t *testing.T, code codes.Code, msg, reason, fault string) error {
	t.Helper()
	info := &errdetails.ErrorInfo{Reason: reason, Metadata: map[string]string{}}
	if fault != "" {
		info.Metadata["fault"] = fault
	}
	st, err := status.New(code, msg).WithDetails(info)
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestRemoteFaultRules(t *testing.T) {
	for _, tc := range []struct {
		name, fault string
		forwarded   bool
		want        Fault
		// public reports whether the remote public error is used for the
		// response. It is not when the remote refused this process's request.
		public bool
	}{
		{"client", "client", false, FaultServer, false},
		{"forwarded client", "client", true, FaultClient, true},
		{"server", "server", false, FaultDependency, true},
		{"dependency", "dependency", false, FaultDependency, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Remote(remoteStatus(t, codes.Internal, "remote", "remote.failed", tc.fault))
			if tc.forwarded {
				err = Forwarded(err)
			}
			got := Analyze(context.Background(), err)
			if got.Fault != tc.want || (got.Public != nil) != tc.public || got.Reason != "remote.failed" {
				t.Fatalf("fault=%s public=%v reason=%q", got.Fault, got.Public, got.Reason)
			}
		})
	}
	if got := Analyze(context.Background(), apperror.New(codeClientBare, nil)).Fault; got != FaultClient {
		t.Fatal(got)
	}
}

func TestInvalidRemoteFault(t *testing.T) {
	err := Forwarded(Remote(remoteStatus(t, codes.Internal, "x", "x", "invalid")))
	if got := Analyze(context.Background(), err); got.Fault != FaultDependency || got.RemoteFault != "" {
		t.Fatalf("report=%+v", got)
	}
}

// An RPC client restores the reason into an apperror and keeps the received
// status on the chain. The apperror cannot carry fault; it is read from the
// status under the same Remote marker.
func TestRemoteFaultFromRestoredAppError(t *testing.T) {
	received := remoteStatus(t, codes.FailedPrecondition, "workspace down", string(codeServer), "dependency")
	restored := Remote(stderrors.Join(apperror.New(codeServer, nil), received))
	r := Analyze(context.Background(), Wrap(restored, "call runtime"))
	if !r.Remote || r.RemoteFault != "dependency" || r.Fault != FaultDependency || r.Reason != string(codeServer) {
		t.Fatalf("report=%+v", r)
	}
	if apperror.CodeOf(r.Public.Err) != codeServer {
		t.Fatalf("public=%+v, want restored apperror", r.Public)
	}

	// A status from another call, outside the marker, is not this remote's fault.
	other := remoteStatus(t, codes.Internal, "x", "x", "client")
	r = Analyze(context.Background(), stderrors.Join(Remote(apperror.New(codeServer, nil)), other))
	if r.RemoteFault != "" {
		t.Fatalf("remote_fault=%q read outside the marker", r.RemoteFault)
	}
}

func TestNativeStatusIsPublic(t *testing.T) {
	st, err := status.New(codes.NotFound, "runtime not found").WithDetails(
		&errdetails.ErrorInfo{Reason: "runtime.not_found", Metadata: map[string]string{"runtime_id": "r1"}},
		&errdetails.RetryInfo{},
	)
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(context.Background(), Wrap(fmt.Errorf("lookup: %w", st.Err()), "resolve runtime"))
	if r.Fault != FaultClient || r.Reason != "runtime.not_found" || r.Public.Code != http.StatusNotFound || r.Public.Message != "runtime not found" || r.Public.Metadata["runtime_id"] != "r1" {
		t.Fatalf("report=%+v public=%+v", r, r.Public)
	}
	if got, ok := status.FromError(r.Public.Err); !ok || len(got.Details()) != 2 || got.Message() != "runtime not found" {
		t.Fatalf("status not preserved: %v", r.Public.Err)
	}
	if got := Analyze(context.Background(), status.Error(codes.FailedPrecondition, "team is not active")); got.Reason != "failed_precondition" || got.Fault != FaultClient {
		t.Fatalf("fallback reason: %+v", got)
	}
	if got := Analyze(context.Background(), status.Error(codes.Unavailable, "down")); got.Fault != FaultServer || !got.Unlocated {
		t.Fatalf("5xx status: %+v", got)
	}
	remote := remoteStatus(t, codes.Internal, "internal error", "internal", "server")
	if got := Analyze(context.Background(), Wrap(Remote(remote), "call runtime")); got.Fault != FaultDependency || got.RemoteFault != "server" || !got.Remote {
		t.Fatalf("remote status: %+v", got)
	}
	if got := Analyze(context.Background(), Wrap(Remote(status.Error(codes.Unavailable, "dial tcp 10.0.0.1:9000: connection refused")), "call runtime")); got.Public != nil || got.Reason != "internal" || got.Fault != FaultDependency {
		t.Fatalf("transport status must not be public: %+v", got)
	}
}

func TestRemoteRejectionOfOurRequestKeepsStatusOffWire(t *testing.T) {
	r := Analyze(context.Background(), Wrap(Remote(remoteStatus(t, codes.PermissionDenied, "workload not allowed", "forbidden", "client")), "call runtime"))
	if r.Fault != FaultServer || r.Public != nil || r.Reason != "forbidden" || !r.Remote || r.RemoteFault != "client" {
		t.Fatalf("report = %+v", r)
	}
}

func TestRemoteMarkerKeepsStatus(t *testing.T) {
	st, _ := status.New(codes.FailedPrecondition, "team is not active").WithDetails(&errdetails.RetryInfo{})
	got, ok := status.FromError(Remote(st.Err()))
	if !ok || got.Code() != codes.FailedPrecondition || got.Message() != "team is not active" || len(got.Details()) != 1 {
		t.Fatalf("status through marker: %v %v", got, ok)
	}
	if code := status.Code(Wrap(Remote(st.Err()), "call runtime")); code != codes.FailedPrecondition {
		t.Fatal(code)
	}
	if _, ok := status.FromError(Remote(stderrors.New("plain"))); ok {
		t.Fatal("plain error reported as status")
	}
}
