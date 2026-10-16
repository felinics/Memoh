//nolint:errorlint // the tests compare chain nodes by identity, as Analyze reports them.
package errs

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

func TestAppErrorIsPublic(t *testing.T) {
	err := fmt.Errorf("create bot: %w", apperror.New(codeClient, map[string]string{"field": "name", "secret": "x"}))
	r := Analyze(context.Background(), Wrap(err, "handler"))
	if r.Fault != apperror.FaultClient || r.Reason != string(codeClient) || r.answer == nil {
		t.Fatalf("report=%+v", r)
	}
	if !reflect.DeepEqual(apperror.ArgsOf(r.answer), map[string]string{"field": "name"}) {
		t.Fatalf("args=%v, want catalog-allowed args only", apperror.ArgsOf(r.answer))
	}
	if apperror.CodeOf(r.answer) != codeClient {
		t.Fatalf("answer=%v", r.answer)
	}

	server := Analyze(context.Background(), apperror.Wrap(codeServer, New("dial"), nil))
	if server.Fault != apperror.FaultServer || server.Reason != string(codeServer) || apperror.CodeOf(server.answer) != codeServer {
		t.Fatalf("5xx apperror: %+v", server)
	}

	unknown := Analyze(context.Background(), apperror.New("not.registered", nil))
	if unknown.answer != nil || unknown.Reason != "internal" || unknown.Fault != apperror.FaultServer {
		t.Fatalf("unregistered code must not be public: %+v", unknown)
	}
}

func TestFaults(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want apperror.Fault
	}{
		{"client", Wrap(apperror.New(codeClientBare, nil), "context"), apperror.FaultClient},
		{"dependency", NewDependency("down"), apperror.FaultDependency},
		{"public server", apperror.Wrap(codeServer, New("cause"), nil), apperror.FaultServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Analyze(context.Background(), tc.err).Fault; got != tc.want {
				t.Fatalf("got %s", got)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Analyze(ctx, stderrors.New("not canceled")).Fault; got == apperror.FaultCanceled {
		t.Fatal("unrelated error became canceled")
	}
	if got := Analyze(ctx, fmt.Errorf("%w", context.Canceled)).Fault; got != apperror.FaultCanceled {
		t.Fatal(got)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Hour)
	defer cancel2()
	if got := Analyze(ctx2, context.DeadlineExceeded).Fault; got != apperror.FaultServer {
		t.Fatal(got)
	}
}

func TestCanceledRequiresCallerCause(t *testing.T) {
	internal := stderrors.New("lease lost")
	cases := map[string]struct {
		cancel func(context.CancelCauseFunc)
		want   apperror.Fault
	}{
		"plain cancel":   {func(c context.CancelCauseFunc) { c(nil) }, apperror.FaultCanceled},
		"canceled cause": {func(c context.CancelCauseFunc) { c(context.Canceled) }, apperror.FaultCanceled},
		"deadline cause": {func(c context.CancelCauseFunc) { c(context.DeadlineExceeded) }, apperror.FaultCanceled},
		"internal cause": {func(c context.CancelCauseFunc) { c(internal) }, apperror.FaultServer},
		"wrapped cancel": {func(c context.CancelCauseFunc) { c(fmt.Errorf("stop: %w", context.Canceled)) }, apperror.FaultServer},
		// The idle watchdog cancels with a public error wrapping DeadlineExceeded.
		"public cause": {func(c context.CancelCauseFunc) {
			c(apperror.Wrap(codeServer, context.DeadlineExceeded, nil))
		}, apperror.FaultServer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			tc.cancel(cancel)
			if got := Analyze(ctx, Wrap(context.Canceled, "wait")); got.Fault != tc.want {
				t.Fatalf("fault = %s, want %s", got.Fault, tc.want)
			}
		})
	}
}

type sliceError struct{ values []string }

func (e sliceError) Error() string { return strings.Join(e.values, ",") }

func TestNonComparableError(t *testing.T) {
	err := Wrap(sliceError{values: []string{"not", "comparable"}}, "wrapped")
	if got := Analyze(context.Background(), err); got.Source == nil || got.Unlocated {
		t.Fatalf("report=%+v", got)
	}
}

func TestAttributionCases(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name        string
		ctx         context.Context
		err         error
		want        apperror.Fault
		reason      string
		remote      bool
		remoteFault apperror.Fault
	}{
		{"local client dominates inner dependency", context.Background(), apperror.Wrap(codeClientBare, WrapDependency(stderrors.New("down"), "down"), nil), apperror.FaultClient, string(codeClientBare), false, ""},
		{"remote client", context.Background(), Wrap(Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client")), "calling remote"), apperror.FaultServer, "bad", true, "client"},
		{"forwarded remote client", context.Background(), Wrap(Forwarded(Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client"))), "forwarding"), apperror.FaultClient, "bad", true, "client"},
		{"remote server", context.Background(), Remote(remoteStatus(t, codes.Internal, "failed", "fail", "server")), apperror.FaultDependency, "fail", true, "server"},
		{"remote dependency", context.Background(), Remote(remoteStatus(t, codes.Internal, "failed", "fail", "dependency")), apperror.FaultDependency, "fail", true, "dependency"},
		{"remote fault absent", context.Background(), Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "")), apperror.FaultDependency, "internal", true, ""},
		// An RPC server writes its fault on the status it returns; its own
		// result record attributes that status as one this process built.
		{"unmarked status with fault", context.Background(), remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client"), apperror.FaultClient, "bad", false, ""},
		{"local 5xx over remote", context.Background(), apperror.Wrap(codeServer, Remote(status.Error(codes.Unavailable, "down")), nil), apperror.FaultDependency, string(codeServer), false, ""},
		{"local 5xx over remote server", context.Background(), apperror.Wrap(codeServer, Remote(remoteStatus(t, codes.Internal, "failed", "fail", "server")), nil), apperror.FaultDependency, string(codeServer), false, ""},
		{"local 5xx over remote refusal", context.Background(), apperror.Wrap(codeServer, Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client")), nil), apperror.FaultServer, string(codeServer), false, ""},
		{"remote canceled with fault", context.Background(), Remote(remoteStatus(t, codes.Canceled, "turn canceled", "", "server")), apperror.FaultDependency, "internal", true, ""},
		{"dependency declared", context.Background(), WrapDependency(stderrors.New("network"), "provider"), apperror.FaultDependency, "internal", false, ""},
		{"server by default", context.Background(), New("bug"), apperror.FaultServer, "internal", false, ""},
		{"context canceled", cancelCtx, Wrap(context.Canceled, "work"), apperror.FaultCanceled, "canceled", false, ""},
		{"grpc canceled", cancelCtx, status.Error(codes.Canceled, "stopped"), apperror.FaultCanceled, "canceled", false, ""},
		{"grpc deadline", cancelCtx, status.Error(codes.DeadlineExceeded, "deadline"), apperror.FaultCanceled, "canceled", false, ""},
		{"grpc canceled without canceled ctx", context.Background(), status.Error(codes.Canceled, "stopped"), apperror.FaultServer, "internal", false, ""},
		{"remote 504", cancelCtx, Remote(remoteStatus(t, codes.DeadlineExceeded, "timeout", "timeout", "dependency")), apperror.FaultCanceled, "canceled", true, ""},
		{"unrelated canceled ctx", cancelCtx, stderrors.New("unrelated"), apperror.FaultServer, "internal", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Analyze(tc.ctx, tc.err)
			if r.Fault != tc.want || r.Reason != tc.reason || r.Remote != tc.remote || r.RemoteFault != tc.remoteFault {
				t.Fatalf("report = %+v; want fault=%s reason=%q remote=%v remote_fault=%q", r, tc.want, tc.reason, tc.remote, tc.remoteFault)
			}
		})
	}
}

// A catalog entry that declares its fault is attributed by the declaration,
// not by its status: a provider's 401 and 429 are the provider's answer, and a
// provider's 503 is not this process failing. The response still renders the
// public error.
func TestCatalogDeclaredFault(t *testing.T) {
	for _, code := range []apperror.Code{
		apperror.CodeAgentProviderAuthFailed,
		apperror.CodeAgentProviderRateLimited,
		apperror.CodeAgentProviderOverloaded,
		apperror.CodeAgentResponseInterrupted,
	} {
		t.Run(string(code), func(t *testing.T) {
			r := Analyze(context.Background(), Wrap(apperror.Wrap(code, stderrors.New("api error"), nil), "run turn"))
			if r.Fault != apperror.FaultDependency || r.Reason != string(code) || apperror.CodeOf(r.answer) != code {
				t.Fatalf("report = %+v; want fault=dependency reason=%s with the public error", r, code)
			}
		})
	}
	undeclared := Analyze(context.Background(), apperror.New(codeClientBare, nil))
	if undeclared.Fault != apperror.FaultClient || undeclared.answer == nil {
		t.Fatalf("undeclared 4xx report = %+v; want client from the status", undeclared)
	}
}

func TestJoinTraversal(t *testing.T) {
	sentinel := stderrors.New("sentinel")
	_, file, line, _ := runtime.Caller(0)
	first := Wrap(sentinel, "first", slog.String("id", "first")) // line + 1
	second := WrapDependency(stderrors.New("second"), "second", slog.String("id", "second"))
	public := apperror.New(codeServer, nil)
	err := fmt.Errorf("top: %w", stderrors.Join(second, fmt.Errorf("source: %w", first), public))
	if !stderrors.Is(err, sentinel) {
		t.Fatal("errors.Is did not cross the join")
	}
	r := Analyze(context.Background(), err)
	if r.answer != public || r.Fault != apperror.FaultDependency || r.Source == nil || r.Source.File != file || r.Source.Line != line+2 {
		t.Fatalf("report = %+v, expected second branch source at %s:%d", r, file, line+2)
	}
	if r.Attrs[0].Value.String() != "second" || r.Text != "top: second: second; source: first: sentinel; workspace.unreachable" {
		t.Fatalf("report = %+v", r)
	}
	if len(r.Attrs) != 1 {
		// The later branch's id is shadowed by the first branch.
		t.Fatalf("attrs = %v", r.Attrs)
	}
}

func TestOuterPublicPrecedence(t *testing.T) {
	inner := Remote(remoteStatus(t, codes.Internal, "down", "down", "server"))
	outer := apperror.Wrap(codeClientBare, WrapDependency(inner, "dependency"), nil)
	r := Analyze(context.Background(), outer)
	if r.answer != outer || r.Fault != apperror.FaultClient || r.Reason != string(codeClientBare) {
		t.Fatalf("outer public should decide attribution: %+v", r)
	}
}

func TestCanceledDropsPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := status.New(codes.InvalidArgument, "bad").Err()
	r := Analyze(ctx, Wrap(stderrors.Join(st, context.Canceled), "call"))
	if r.Fault != apperror.FaultCanceled || r.answer != nil {
		t.Fatalf("canceled report kept a public error: %+v", r)
	}
}

func TestCauseTraversal(t *testing.T) {
	source := New("source", slog.String("id", "source"))

	// Through apperror itself once it implements Cause(), and through any
	// type that implements only Cause().
	for name, err := range map[string]error{
		"cause only": causedError{code: codeClientBare, cause: source},
		"wrapped":    fmt.Errorf("handler: %w", causedError{code: codeClientBare, cause: fmt.Errorf("use case: %w", source)}),
	} {
		t.Run(name, func(t *testing.T) {
			r := Analyze(context.Background(), err)
			if r.Source == nil || len(r.Attrs) != 1 || r.Attrs[0].Value.String() != "source" {
				t.Fatalf("Cause() not traversed: %+v", r)
			}
			if !hasStack(err) {
				t.Fatal("hasStack did not follow Cause()")
			}
		})
	}

	// hasStack follows Cause(), so wrapping above such an error does not
	// record a second, shallower stack.
	outer := Wrap(causedError{code: codeServer, cause: source}, "outer")
	if e, ok := outer.(*faultError); !ok || len(e.stack) != 0 {
		t.Fatal("Wrap recorded a stack although one exists under Cause()")
	}

	// Cancellation below Cause() still counts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Analyze(ctx, causedError{code: codeServer, cause: context.Canceled}).Fault; got != apperror.FaultCanceled {
		t.Fatalf("fault=%s, want canceled", got)
	}

	visits := 0
	walk(causeAndUnwrap{cause: source}, false, false, func(n node) bool {
		if n.err == source {
			visits++
		}
		return true
	})
	if visits != 1 {
		t.Fatalf("source visited %d times, want 1", visits)
	}
}

func TestCallerEndedOnlyForTheCallersOwnCancellation(t *testing.T) {
	t.Parallel()
	ended := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
	live, stop := context.WithCancel(context.Background())
	defer stop()
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{name: "nil", ctx: nil},
		{name: "live", ctx: live},
		{name: "canceled", ctx: ended(context.Canceled), want: true},
		{name: "deadline", ctx: ended(context.DeadlineExceeded), want: true},
		{name: "own cause wrapping a deadline", ctx: ended(fmt.Errorf("idle timeout: %w", context.DeadlineExceeded))},
		{name: "own cause", ctx: ended(stderrors.New("run ownership lost"))},
	}
	for _, tt := range tests {
		if got := CallerEnded(tt.ctx); got != tt.want {
			t.Errorf("%s: CallerEnded = %v, want %v", tt.name, got, tt.want)
		}
	}
}
