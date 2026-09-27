//nolint:errorlint // the tests compare chain nodes by identity, as Analyze reports them.
package errs

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

// Catalog codes the tests use: a 4xx with an allowed arg, a 4xx without, and
// a 5xx.
const (
	codeClient     = apperror.CodeBotNameTaken         // 409, args: field
	codeClientBare = apperror.CodeCapabilityNotFound   // 404
	codeServer     = apperror.CodeWorkspaceUnreachable // 503
)

// causedError stands in for apperror.Error once it implements Cause(): the
// cause is reachable through Cause() only, never through Unwrap.
type causedError struct {
	code  apperror.Code
	cause error
}

func (e causedError) Error() string { return string(e.code) }
func (e causedError) Cause() error  { return e.cause }

type causeAndUnwrap struct{ cause error }

func (e causeAndUnwrap) Error() string { return "both: " + e.cause.Error() }
func (e causeAndUnwrap) Cause() error  { return e.cause }
func (e causeAndUnwrap) Unwrap() error { return e.cause }

func TestWrapAndFrame(t *testing.T) {
	base := stderrors.New("base")
	wrapped := Wrap(base, "outer", slog.String("id", "outer"))
	wrapped = Wrap(wrapped, "again", slog.String("id", "inner"))
	if !stderrors.Is(wrapped, base) {
		t.Fatal("errors.Is did not penetrate")
	}
	r := Analyze(context.Background(), wrapped)
	if r.Source == nil || !strings.HasSuffix(r.Source.File, "errs_test.go") {
		t.Fatalf("source=%+v", r.Source)
	}
	if r.Attrs[0].Value.String() != "inner" || r.Attrs[0].Key != "id" {
		t.Fatalf("attrs=%v", r.Attrs)
	}
	if r.Fault != FaultServer || r.Unlocated {
		t.Fatalf("report=%+v", r)
	}
}

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

func TestWithTimeout(t *testing.T) {
	ctx, cancel := WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-ctx.Done()
	if got := Analyze(ctx, context.DeadlineExceeded); got.Fault == FaultCanceled {
		t.Fatalf("got canceled: %+v", got)
	}
}

func TestText(t *testing.T) {
	u, _ := url.Parse("https://user:secret@example.test/path?token=abc")
	err := Wrap(causedError{code: codeServer, cause: stderrors.New("see https://x.test/a?sig=secret")}, "request "+u.String())
	text := Text(err)
	if strings.Contains(text, "secret") || strings.Contains(text, "user:") || !strings.Contains(text, "workspace.unreachable: see https://x.test/a") {
		t.Fatalf("text=%q", text)
	}
}

func TestRedactURLs(t *testing.T) {
	got := RedactURLs(`upstream said "https://u:p@blob.test/o?sig=abc#f", retry`)
	if want := `upstream said "https://blob.test/o", retry`; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRedactURLsAnyScheme(t *testing.T) {
	for in, want := range map[string]string{ //nolint:gosec // fake credentials the redaction must remove
		`parse database url: postgres://app:hunter2@db:5432/memoh?sslmode=disable`: `parse database url: postgres://db:5432/memoh`,
		`dial postgres:///memoh?host=/tmp&password=hunter2`:                        `dial postgres:///memoh`,
		`bad dsn postgres://app:hun%zzter2@db/memoh`:                               `bad dsn postgres://[redacted]`,
		`redis://:hunter2@cache:6379/0 refused`:                                    `redis://cache:6379/0 refused`,
	} {
		if got := RedactURLs(in); got != want || strings.Contains(got, "hunter2") {
			t.Errorf("RedactURLs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinAndAs(t *testing.T) {
	pub := apperror.New(codeClientBare, nil)
	err := stderrors.Join(Wrap(stderrors.New("a"), "one"), fmt.Errorf("other: %w", pub))
	var got *apperror.Error
	if !stderrors.As(err, &got) || got != pub {
		t.Fatal("errors.As failed")
	}
	if Analyze(context.Background(), err).Public.Err != pub {
		t.Fatal("analysis failed")
	}
}

func TestLogAttrsOmission(t *testing.T) {
	r := Analyze(context.Background(), New("bad"))
	for _, a := range r.LogAttrs() {
		if a.Key == "remote" {
			t.Fatal("remote must be omitted")
		}
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

func TestStackOrigin(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	err := New("origin") // line + 1
	r := Analyze(context.Background(), WrapDependency(err, "outer"))
	if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
		t.Fatalf("first frame = %+v, want %s:%d", r.Source, file, line+1)
	}
	if len(r.Stack) == 0 || len(r.Stack) > 16 || r.Stack[0] != *r.Source {
		t.Fatalf("stack = %+v, source = %+v", r.Stack, r.Source)
	}
	if Wrap(nil, "ignored") != nil || WrapDependency(nil, "ignored") != nil {
		t.Fatal("wrapping nil must return nil")
	}
}

// wrapProviderFailure is a wrapping helper: the source must be the line that
// calls it, not this one.
func wrapProviderFailure(err error, dependency bool) error {
	if dependency {
		return WrapDependencyWithDepth(1, err, "provider call")
	}
	return WrapWithDepth(1, err, "provider call")
}

func invalidSetting(field string) error {
	return NewWithDepth(1, "invalid setting", slog.String("field", field))
}

func TestNewWithDepthLocatesHelperCaller(t *testing.T) {
	_, file, line, _ := runtime.Caller(0)
	err := invalidSetting("app_id") // line + 1
	r := Analyze(context.Background(), err)
	if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
		t.Fatalf("source = %+v, want %s:%d", r.Source, file, line+1)
	}
	if r.Fault != FaultServer {
		t.Fatalf("fault = %s, want server", r.Fault)
	}
}

func TestWithDepthLocatesHelperCaller(t *testing.T) {
	for _, dependency := range []bool{true, false} {
		_, file, line, _ := runtime.Caller(0)
		err := wrapProviderFailure(stderrors.New("down"), dependency) // line + 1
		r := Analyze(context.Background(), err)
		if r.Source == nil || r.Source.File != file || r.Source.Line != line+1 {
			t.Fatalf("dependency=%t source = %+v, want %s:%d", dependency, r.Source, file, line+1)
		}
		if want := map[bool]Fault{true: FaultDependency, false: FaultServer}[dependency]; r.Fault != want {
			t.Fatalf("dependency=%t fault = %s, want %s", dependency, r.Fault, want)
		}
	}
	if WrapWithDepth(1, nil, "ignored") != nil || WrapDependencyWithDepth(1, nil, "ignored") != nil {
		t.Fatal("wrapping nil must return nil")
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

func TestTextRendering(t *testing.T) {
	signedURL := "https://user:secret@files.example.test/download?token=abc" //nolint:gosec // fake credentials the redaction must remove
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"url error", &url.Error{Op: "GET", URL: signedURL, Err: stderrors.New("failed")}, `GET "https://files.example.test/download": failed`},
		{"plain text", New("download " + signedURL), "download https://files.example.test/download"},
		{"fmt public", fmt.Errorf("request: %w", apperror.New(codeClient, map[string]string{"field": "not-for-log"})), "request: bot.name_taken"},
		{"public error", apperror.New(codeServer, nil), "workspace.unreachable"},
		{"cause only", causedError{code: codeServer, cause: New("source")}, "workspace.unreachable: source"},
		{"joined branches", stderrors.Join(New("first"), New("second")), "first; second"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.err); got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
	wrapped := Wrap(&url.Error{Op: "GET", URL: signedURL, Err: stderrors.New("failed")}, "fetch")
	if got := wrapped.Error(); strings.Contains(got, "secret") || strings.Contains(got, "token") {
		t.Fatalf("Error() leaked URL: %q", got)
	}
}

func jsonLogAttrs(t *testing.T, r Report) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.LogAttrs(context.Background(), slog.LevelError, "failure", r.LogAttrs()...)
	var data map[string]any
	if err := json.Unmarshal(buf.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLogAttrsJSON(t *testing.T) {
	wrapped := WrapDependency(New("root", slog.String("id", "inner"), slog.Int("count", 3)), "outer", slog.String("id", "outer"))
	remote := Remote(stderrors.Join(remoteStatus(t, codes.Unavailable, "unavailable", "down", "server"), wrapped))
	r := Analyze(context.Background(), remote)
	r.Panic = true
	data := jsonLogAttrs(t, r)
	for key, want := range map[string]any{"fault": "dependency", "reason": "down", "remote": true, "remote_fault": "server", "panic": true} {
		if data[key] != want {
			t.Errorf("%s = %v, want %v", key, data[key], want)
		}
	}
	if text, _ := data["error"].(string); !strings.HasSuffix(text, "; outer: root") {
		t.Errorf("error = %q", text)
	}
	attrs, ok := data["error_attrs"].(map[string]any)
	if !ok || attrs["id"] != "outer" || attrs["count"] != float64(3) {
		t.Fatalf("attrs = %#v", data["error_attrs"])
	}
	source, ok := data["error_source"].(map[string]any)
	if !ok || source["file"] == "" || source["function"] == "" || source["line"] == nil {
		t.Fatalf("source = %#v", data["error_source"])
	}
	stack, ok := data["error_stack"].([]any)
	if !ok || len(stack) < 1 || len(stack) > 16 || !reflect.DeepEqual(source, stack[0]) {
		t.Fatalf("stack = %#v, source = %#v", data["error_stack"], source)
	}

	bare := jsonLogAttrs(t, Analyze(context.Background(), stderrors.New("bare")))
	if bare["fault"] != "server" || bare["reason"] != "internal" || bare["error"] != "bare" {
		t.Fatalf("bare = %#v", bare)
	}
	for _, key := range []string{"error_source", "error_stack", "error_attrs", "remote", "remote_fault", "panic"} {
		if _, exists := bare[key]; exists {
			t.Errorf("bare log must omit %q", key)
		}
	}
	if !Analyze(context.Background(), stderrors.New("bare")).Unlocated {
		t.Fatal("bare error must be unlocated")
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

func panicAt(v any) (err error) {
	defer func() {
		err = Wrap(Recovered(recover()), "job")
	}()
	panic(v) // panic site
}

func TestRecovered(t *testing.T) {
	err := panicAt("secret user input")
	r := Analyze(context.Background(), err)
	if !r.Panic || r.Fault != FaultServer || r.Unlocated {
		t.Fatalf("report=%+v", r)
	}
	if strings.Contains(r.Text, "secret") || r.Text != "job: panic" {
		t.Fatalf("text=%q", r.Text)
	}
	if r.Source == nil || !strings.HasSuffix(r.Source.Function, ".panicAt") {
		t.Fatalf("source=%+v, want panicAt", r.Source)
	}

	var m map[string]int
	err = func() (err error) {
		defer func() { err = Recovered(recover()) }()
		m["x"] = 1
		return nil
	}()
	if got := Text(err); got != "panic: assignment to entry in nil map" {
		t.Fatalf("text=%q", got)
	}
	if Analyze(context.Background(), New("plain")).Panic {
		t.Fatal("plain error must not be panic")
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

func TestCanceledDropsPublic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := status.New(codes.InvalidArgument, "bad").Err()
	r := Analyze(ctx, Wrap(stderrors.Join(st, context.Canceled), "call"))
	if r.Fault != FaultCanceled || r.Public != nil {
		t.Fatalf("canceled report kept a public error: %+v", r)
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
