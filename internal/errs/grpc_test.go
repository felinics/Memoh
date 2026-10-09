package errs

import (
	"context"
	stderrors "errors"
	"fmt"
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
		want        apperror.Fault
		// public reports whether the remote public error is used for the
		// response. It is not when the remote refused this process's request.
		public bool
	}{
		{"client", "client", false, apperror.FaultServer, false},
		{"forwarded client", "client", true, apperror.FaultClient, true},
		{"server", "server", false, apperror.FaultDependency, true},
		{"dependency", "dependency", false, apperror.FaultDependency, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			received := remoteStatus(t, codes.Internal, "remote", string(codeServer), tc.fault)
			err := Remote(stderrors.Join(apperror.New(codeServer, nil), received))
			if tc.forwarded {
				err = Forwarded(err)
			}
			got := Analyze(context.Background(), err)
			if got.Fault != tc.want || (got.answer != nil) != tc.public || got.Reason != string(codeServer) {
				t.Fatalf("fault=%s answer=%v reason=%q", got.Fault, got.answer, got.Reason)
			}
		})
	}
	if got := Analyze(context.Background(), apperror.New(codeClientBare, nil)).Fault; got != apperror.FaultClient {
		t.Fatal(got)
	}
}

func TestInvalidRemoteFault(t *testing.T) {
	err := Forwarded(Remote(remoteStatus(t, codes.Internal, "x", "x", "invalid")))
	if got := Analyze(context.Background(), err); got.Fault != apperror.FaultDependency || got.RemoteFault != "" {
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
	if !r.Remote || r.RemoteFault != "dependency" || r.Fault != apperror.FaultDependency || r.Reason != string(codeServer) {
		t.Fatalf("report=%+v", r)
	}
	if apperror.CodeOf(r.answer) != codeServer {
		t.Fatalf("answer=%v, want restored apperror", r.answer)
	}

	// A status from another call, outside the marker, is not this remote's fault.
	other := remoteStatus(t, codes.Internal, "x", "x", "client")
	r = Analyze(context.Background(), stderrors.Join(Remote(apperror.New(codeServer, nil)), other))
	if r.RemoteFault != "" {
		t.Fatalf("remote_fault=%q read outside the marker", r.RemoteFault)
	}
}

func TestNativeStatusIsAttributedNotAnswered(t *testing.T) {
	st, err := status.New(codes.NotFound, "runtime not found").WithDetails(
		&errdetails.ErrorInfo{Reason: "runtime.not_found", Metadata: map[string]string{"runtime_id": "r1"}},
		&errdetails.RetryInfo{},
	)
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(context.Background(), Wrap(fmt.Errorf("lookup: %w", st.Err()), "resolve runtime"))
	if r.Fault != apperror.FaultClient || r.Reason != "runtime.not_found" || r.answer != nil {
		t.Fatalf("report=%+v", r)
	}
	if got := Analyze(context.Background(), status.Error(codes.FailedPrecondition, "team is not active")); got.Reason != "failed_precondition" || got.Fault != apperror.FaultClient {
		t.Fatalf("fallback reason: %+v", got)
	}
	if got := Analyze(context.Background(), status.Error(codes.Unavailable, "down")); got.Fault != apperror.FaultServer || !got.Unlocated {
		t.Fatalf("5xx status: %+v", got)
	}
	remote := remoteStatus(t, codes.Internal, "internal error", "internal", "server")
	if got := Analyze(context.Background(), Wrap(Remote(remote), "call runtime")); got.Fault != apperror.FaultDependency || got.RemoteFault != "server" || !got.Remote {
		t.Fatalf("remote status: %+v", got)
	}
	if got := Analyze(context.Background(), Wrap(Remote(status.Error(codes.Unavailable, "dial tcp 10.0.0.1:9000: connection refused")), "call runtime")); got.answer != nil || got.Reason != "internal" || got.Fault != apperror.FaultDependency {
		t.Fatalf("transport status must not be public: %+v", got)
	}
}

func TestRemoteRejectionOfOurRequestKeepsStatusOffWire(t *testing.T) {
	r := Analyze(context.Background(), Wrap(Remote(remoteStatus(t, codes.PermissionDenied, "workload not allowed", "forbidden", "client")), "call runtime"))
	if r.Fault != apperror.FaultServer || r.answer != nil || r.Reason != "forbidden" || !r.Remote || r.RemoteFault != "client" {
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

// TestFaultOfCode pins the class of every gRPC code: the codes
// google/rpc/code.proto maps to a 4xx HTTP status refuse the request.
func TestFaultOfCode(t *testing.T) {
	t.Parallel()
	client := map[codes.Code]bool{
		codes.InvalidArgument: true, codes.FailedPrecondition: true, codes.OutOfRange: true,
		codes.NotFound: true, codes.AlreadyExists: true, codes.Aborted: true,
		codes.PermissionDenied: true, codes.Unauthenticated: true, codes.ResourceExhausted: true,
	}
	for code := codes.OK; code <= codes.Unauthenticated; code++ {
		want := apperror.FaultServer
		if client[code] {
			want = apperror.FaultClient
		}
		if got := faultOfCode(code); got != want {
			t.Errorf("faultOfCode(%s) = %s, want %s", code, got, want)
		}
	}
}

func TestRecordedKeepsTheErrorAndIsReported(t *testing.T) {
	cause := New("run failed")
	err := Wrap(Recorded(cause), "trigger schedule")
	if !stderrors.Is(err, cause) || err.Error() != "trigger schedule: run failed" {
		t.Fatalf("Recorded changed the error: %q", err.Error())
	}
	if !Analyze(context.Background(), err).Recorded {
		t.Fatal("Report.Recorded = false, want true")
	}
	if Analyze(context.Background(), cause).Recorded || Recorded(nil) != nil {
		t.Fatal("Recorded must be set only by the marker")
	}
}
