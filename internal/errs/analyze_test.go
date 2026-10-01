//nolint:errorlint // the tests compare chain nodes by identity, as Analyze reports them.
package errs

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"net/http"
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
	if r.Fault != FaultClient || r.Reason != string(codeClient) || r.Public == nil {
		t.Fatalf("report=%+v", r)
	}
	definition, _ := apperror.Lookup(codeClient)
	if r.Public.Code != definition.HTTPStatus || r.Public.Message != definition.Detail {
		t.Fatalf("public=%+v", r.Public)
	}
	if !reflect.DeepEqual(r.Public.Metadata, map[string]string{"field": "name"}) {
		t.Fatalf("metadata=%v, want catalog-allowed args only", r.Public.Metadata)
	}
	if apperror.CodeOf(r.Public.Err) != codeClient {
		t.Fatalf("public err=%v", r.Public.Err)
	}

	server := Analyze(context.Background(), apperror.Wrap(codeServer, New("dial"), nil))
	if server.Fault != FaultServer || server.Reason != string(codeServer) || server.Public == nil || server.Public.Code != http.StatusServiceUnavailable {
		t.Fatalf("5xx apperror: %+v", server)
	}

	unknown := Analyze(context.Background(), apperror.New("not.registered", nil))
	if unknown.Public != nil || unknown.Reason != "internal" || unknown.Fault != FaultServer {
		t.Fatalf("unregistered code must not be public: %+v", unknown)
	}
}

func TestFaults(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Fault
	}{
		{"client", Wrap(apperror.New(codeClientBare, nil), "context"), FaultClient},
		{"dependency", NewDependency("down"), FaultDependency},
		{"public server", apperror.Wrap(codeServer, New("cause"), nil), FaultServer},
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
	if got := Analyze(ctx, stderrors.New("not canceled")).Fault; got == FaultCanceled {
		t.Fatal("unrelated error became canceled")
	}
	if got := Analyze(ctx, fmt.Errorf("%w", context.Canceled)).Fault; got != FaultCanceled {
		t.Fatal(got)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Hour)
	defer cancel2()
	if got := Analyze(ctx2, context.DeadlineExceeded).Fault; got != FaultServer {
		t.Fatal(got)
	}
}

func TestCanceledRequiresCallerCause(t *testing.T) {
	internal := stderrors.New("lease lost")
	cases := map[string]struct {
		cancel func(context.CancelCauseFunc)
		want   Fault
	}{
		"plain cancel":   {func(c context.CancelCauseFunc) { c(nil) }, FaultCanceled},
		"canceled cause": {func(c context.CancelCauseFunc) { c(context.Canceled) }, FaultCanceled},
		"deadline cause": {func(c context.CancelCauseFunc) { c(context.DeadlineExceeded) }, FaultCanceled},
		"internal cause": {func(c context.CancelCauseFunc) { c(internal) }, FaultServer},
		"wrapped cancel": {func(c context.CancelCauseFunc) { c(fmt.Errorf("stop: %w", context.Canceled)) }, FaultServer},
		// The idle watchdog cancels with a public error wrapping DeadlineExceeded.
		"public cause": {func(c context.CancelCauseFunc) {
			c(apperror.Wrap(codeServer, context.DeadlineExceeded, nil))
		}, FaultServer},
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
		want        Fault
		reason      string
		remote      bool
		remoteFault string
	}{
		{"local client dominates inner dependency", context.Background(), apperror.Wrap(codeClientBare, WrapDependency(stderrors.New("down"), "down"), nil), FaultClient, string(codeClientBare), false, ""},
		{"remote client", context.Background(), Wrap(Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client")), "calling remote"), FaultServer, "bad", true, "client"},
		{"forwarded remote client", context.Background(), Wrap(Forwarded(Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client"))), "forwarding"), FaultClient, "bad", true, "client"},
		{"remote server", context.Background(), Remote(remoteStatus(t, codes.Internal, "failed", "fail", "server")), FaultDependency, "fail", true, "server"},
		{"remote dependency", context.Background(), Remote(remoteStatus(t, codes.Internal, "failed", "fail", "dependency")), FaultDependency, "fail", true, "dependency"},
		{"remote fault absent", context.Background(), Remote(remoteStatus(t, codes.InvalidArgument, "bad", "bad", "")), FaultDependency, "internal", true, ""},
		{"metadata detects remote", context.Background(), remoteStatus(t, codes.InvalidArgument, "bad", "bad", "client"), FaultServer, "bad", true, "client"},
		{"dependency declared", context.Background(), WrapDependency(stderrors.New("network"), "provider"), FaultDependency, "internal", false, ""},
		{"server by default", context.Background(), New("bug"), FaultServer, "internal", false, ""},
		{"context canceled", cancelCtx, Wrap(context.Canceled, "work"), FaultCanceled, "canceled", false, ""},
		{"grpc canceled", cancelCtx, status.Error(codes.Canceled, "stopped"), FaultCanceled, "canceled", false, ""},
		{"grpc deadline", cancelCtx, status.Error(codes.DeadlineExceeded, "deadline"), FaultCanceled, "canceled", false, ""},
		{"grpc canceled without canceled ctx", context.Background(), status.Error(codes.Canceled, "stopped"), FaultServer, "internal", false, ""},
		{"remote 504", cancelCtx, Remote(remoteStatus(t, codes.DeadlineExceeded, "timeout", "timeout", "dependency")), FaultCanceled, "canceled", true, ""},
		{"unrelated canceled ctx", cancelCtx, stderrors.New("unrelated"), FaultServer, "internal", false, ""},
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
	if r.Public == nil || r.Public.Err != public || r.Fault != FaultDependency || r.Source == nil || r.Source.File != file || r.Source.Line != line+2 {
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
	if r.Public == nil || r.Public.Err != outer || r.Fault != FaultClient || r.Reason != string(codeClientBare) {
		t.Fatalf("outer public should decide attribution: %+v", r)
	}
}

func TestCanceledDropsPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := status.New(codes.InvalidArgument, "bad").Err()
	r := Analyze(ctx, Wrap(stderrors.Join(st, context.Canceled), "call"))
	if r.Fault != FaultCanceled || r.Public != nil {
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
	if got := Analyze(ctx, causedError{code: codeServer, cause: context.Canceled}).Fault; got != FaultCanceled {
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
