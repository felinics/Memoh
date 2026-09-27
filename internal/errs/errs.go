//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"context"
	stderrors "errors"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

// Fault is who a failure is attributed to.
type Fault string

const (
	// FaultClient means the caller's request was refused by this process's rules.
	FaultClient Fault = "client"
	// FaultServer means this process failed: its code, data or configuration,
	// including a bad request this process sent downstream.
	FaultServer Fault = "server"
	// FaultDependency means an external provider, an internal downstream
	// service or the network failed.
	FaultDependency Fault = "dependency"
	// FaultCanceled means the caller canceled, or the caller's deadline passed.
	FaultCanceled Fault = "canceled"
)

// Reasons used when the chain has no public error. They are the catalog codes
// apperror uses for the same two outcomes at the HTTP boundary.
const (
	reasonInternal = "internal"
	reasonCanceled = "canceled"
)

const maxStackFrames = 16

type faultError struct {
	msg      string
	cause    error
	attrs    []slog.Attr
	stack    []Frame
	explicit bool
	panic    bool
}

func (e *faultError) Error() string {
	return render(e)
}

func (e *faultError) Unwrap() error {
	return e.cause
}

// New creates an internal error attributed to this process by default.
func New(msg string, attrs ...slog.Attr) error {
	return makeError(0, msg, nil, false, attrs...)
}

// NewWithDepth is New with the recorded stack starting depth frames further
// up. A helper that builds one kind of error uses it so the source names the
// helper's caller.
func NewWithDepth(depth int, msg string, attrs ...slog.Attr) error {
	return makeError(depth, msg, nil, false, attrs...)
}

// Wrap adds context to err and keeps the chain; errors.Is and errors.As still
// see err. It returns nil when err is nil.
func Wrap(err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(0, msg, err, false, attrs...)
}

// WrapWithDepth is Wrap with the recorded stack starting depth frames further
// up. A wrapping helper uses it so the source is the helper's caller rather
// than the helper; depth 0 is Wrap.
func WrapWithDepth(depth int, err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(depth, msg, err, false, attrs...)
}

// NewDependency creates an error attributed to a dependency.
func NewDependency(msg string, attrs ...slog.Attr) error {
	return makeError(0, msg, nil, true, attrs...)
}

// WrapDependency wraps err and attributes it to a dependency.
func WrapDependency(err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(0, msg, err, true, attrs...)
}

// WrapDependencyWithDepth is WrapDependency with depth as in WrapWithDepth.
func WrapDependencyWithDepth(depth int, err error, msg string, attrs ...slog.Attr) error {
	if err == nil {
		return nil
	}
	return makeError(depth, msg, err, true, attrs...)
}

// makeError must be called directly by an exported constructor: the skip
// count below assumes exactly that frame layout.
func makeError(depth int, msg string, cause error, explicit bool, attrs ...slog.Attr) error {
	e := &faultError{msg: msg, cause: cause, attrs: append([]slog.Attr(nil), attrs...), explicit: explicit}
	if !hasStack(cause) {
		e.stack = captureStack(4+max(depth, 0), maxStackFrames)
	}
	return e
}

// Recovered turns a value from recover into an internal error with a stack.
// Call it directly in the deferred function. The stack starts where the panic
// happened. The panic value can hold user data, so it is not part of the
// error text; only a runtime error keeps its description.
func Recovered(v any) error {
	msg := "panic"
	if re, ok := v.(runtime.Error); ok {
		msg = "panic: " + re.Error()
	}
	stack := captureStack(3, maxStackFrames)
	for i, frame := range stack {
		if frame.Function == "runtime.gopanic" || frame.Function == "panic" {
			stack = stack[i+1:]
			break
		}
	}
	return &faultError{msg: msg, stack: stack, panic: true}
}

func captureStack(skip, limit int) []Frame {
	pcs := make([]uintptr, limit)
	n := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	out := make([]Frame, 0, n)
	for len(out) < limit {
		frame, more := frames.Next()
		if frame.Function == "" {
			break
		}
		out = append(out, Frame{Function: frame.Function, File: frame.File, Line: frame.Line})
		if !more {
			break
		}
	}
	return out
}

// hasStack follows the same traversal as Analyze, so an error wrapped under a
// public error that is only reachable through Cause() is not given a second
// stack.
func hasStack(err error) bool {
	found := false
	walk(err, false, false, func(n node) bool {
		if e, ok := n.err.(*faultError); ok && len(e.stack) > 0 {
			found = true
			return false
		}
		return true
	})
	return found
}

// Remote marks an error received from an internal RPC. The error value is
// unchanged: status.FromError and status.Code still read the received status.
func Remote(err error) error {
	if err == nil {
		return nil
	}
	return &marker{kind: "remote", err: err}
}

// Forwarded declares that the caller deliberately passed end-user input to
// the downstream, so a remote client fault stays a client fault here.
func Forwarded(err error) error {
	if err == nil {
		return nil
	}
	return &marker{kind: "forwarded", err: err}
}

type marker struct {
	kind string
	err  error
}

func (m *marker) Error() string {
	return Text(m.err)
}

func (m *marker) Unwrap() error {
	return m.err
}

// GRPCStatus lets status.FromError read the marked error's own status, so the
// message does not pick up the text of the whole chain.
func (m *marker) GRPCStatus() *status.Status {
	if carrier, ok := m.err.(interface{ GRPCStatus() *status.Status }); ok {
		return carrier.GRPCStatus()
	}
	return nil
}

var errTimeoutCause = stderrors.New("errs: local timeout")

// WithTimeout creates a timeout this process sets for itself. When it expires
// the context's cause is not context.DeadlineExceeded, so Analyze does not
// attribute the failure to the caller.
func WithTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, d, errTimeoutCause)
}

// Frame is a source frame where an error was produced.
type Frame struct {
	Function, File string
	Line           int
}

// LogValue encodes the frame as a log group.
func (f Frame) LogValue() slog.Value {
	return slog.GroupValue(slog.String("function", f.Function), slog.String("file", f.File), slog.Int("line", f.Line))
}

// Public is the public error found on a chain, independent of transport.
type Public struct {
	// Code is the HTTP status.
	Code int
	// Reason is the apperror catalog code, or the ErrorInfo reason of a
	// native gRPC status.
	Reason string
	// Message is the catalog detail, or the status message.
	Message  string
	Metadata map[string]string
	// Err is the chain node recognized as the public error. A gRPC boundary
	// rebuilds its status from this node so the details survive.
	Err error
}

// Report is what Analyze concludes about an error chain at a boundary.
type Report struct {
	Fault Fault
	// Public is the public error to respond with. It is nil when the response
	// is internal, and also when the chain holds a public error that must not
	// be returned: the caller has canceled, or a remote refused a request this
	// process sent (fault server), which says nothing about this process's
	// caller. Reason still takes that error's reason.
	Public      *Public
	Reason      string
	Text        string
	Source      *Frame
	Stack       []Frame
	Attrs       []slog.Attr
	Remote      bool
	RemoteFault string
	Unlocated   bool
	Panic       bool
}

type node struct {
	err               error
	remote, forwarded bool
}

// walk visits the chain depth first. It expands Unwrap() []error and
// Unwrap() error; an error that implements only Cause() error is expanded
// through Cause(). A type implementing both is expanded once, through Unwrap.
func walk(err error, remote, forwarded bool, visit func(node) bool) {
	if err == nil {
		return
	}
	n := node{err: err, remote: remote, forwarded: forwarded}
	if m, ok := err.(*marker); ok {
		n.remote = remote || m.kind == "remote"
		n.forwarded = forwarded || m.kind == "forwarded"
	}
	if !visit(n) {
		return
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range many.Unwrap() {
			walk(child, n.remote, n.forwarded, visit)
		}
		return
	}
	walk(single(err), n.remote, n.forwarded, visit)
}

// single returns the one child of err under the traversal rule of walk, or
// nil.
func single(err error) error {
	if one, ok := err.(interface{ Unwrap() error }); ok {
		return one.Unwrap()
	}
	if caused, ok := err.(interface{ Cause() error }); ok {
		return caused.Cause()
	}
	return nil
}

// Analyze attributes err, returned by a unit of work running under ctx. A nil
// ctx means the unit has no caller that could have canceled it.
func Analyze(ctx context.Context, err error) Report {
	r := Report{Fault: FaultServer, Reason: reasonInternal, Text: Text(err)}
	if err == nil {
		return r
	}
	var nodes []node
	walk(err, false, false, func(n node) bool {
		nodes = append(nodes, n)
		return true
	})
	publicForwarded := false
	for i, n := range nodes {
		if n.remote {
			r.Remote = true
		}
		public, ok := publicOf(n)
		if !ok {
			continue
		}
		r.Public = public
		r.Reason = public.Reason
		r.Remote = n.remote || public.Metadata["fault"] != ""
		publicForwarded = n.forwarded
		if r.Remote {
			r.RemoteFault = remoteFaultOf(public, nodes[i+1:])
		}
		break
	}
	for _, n := range nodes {
		if e, ok := n.err.(*faultError); ok && e.panic {
			r.Panic = true
			break
		}
	}
	for _, n := range nodes {
		if e, ok := n.err.(*faultError); ok && len(e.stack) > 0 {
			r.Stack = append([]Frame(nil), e.stack...)
			r.Source = &r.Stack[0]
			break
		}
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		if e, ok := n.err.(*faultError); ok {
			for _, attr := range e.attrs {
				if !seen[attr.Key] {
					r.Attrs = append(r.Attrs, attr)
					seen[attr.Key] = true
				}
			}
		}
	}
	switch {
	case ctx != nil && ctx.Err() != nil && callerEnded(context.Cause(ctx)) && containsCanceled(nodes):
		r.Fault = FaultCanceled
		r.Reason = reasonCanceled
	case r.Public != nil && r.Remote:
		r.Fault = remoteFault(r.RemoteFault, publicForwarded)
	case r.Public != nil && r.Public.Code < http.StatusInternalServerError:
		r.Fault = FaultClient
	case r.Remote:
		r.Fault = FaultDependency
	default:
		for _, n := range nodes {
			if e, ok := n.err.(*faultError); ok && e.explicit {
				r.Fault = FaultDependency
				break
			}
		}
	}
	if r.Fault == FaultCanceled || (r.Remote && r.Fault == FaultServer) {
		r.Public = nil
	}
	r.Unlocated = (r.Fault == FaultServer || r.Fault == FaultDependency) && len(r.Stack) == 0
	return r
}

// publicOf recognizes the two forms of public error: an apperror with a
// catalog code, and a native gRPC status.
func publicOf(n node) (*Public, bool) {
	if public, ok := appPublic(n.err); ok {
		return public, true
	}
	return grpcPublic(n)
}

// appPublic recognizes an apperror whose code is in the catalog. An
// unregistered code is not a public error: the HTTP boundary cannot render it
// as a Problem either, and responds internal.
func appPublic(err error) (*Public, bool) {
	e, ok := err.(*apperror.Error)
	if !ok {
		return nil, false
	}
	code := apperror.CodeOf(e)
	definition, ok := apperror.Lookup(code)
	if !ok {
		return nil, false
	}
	return &Public{Code: definition.HTTPStatus, Reason: string(code), Message: definition.Detail, Metadata: apperror.ArgsOf(e), Err: e}, true
}

// grpcPublic recognizes a native gRPC status as a public error.
// Canceled and DeadlineExceeded are produced by the gRPC client when a context
// ends and are not public. A remote-marked status whose ErrorInfo carries no
// fault was produced by the gRPC client itself, or by a peer that does not
// write the envelope; its text may name internal addresses and is not public.
func grpcPublic(n node) (*Public, bool) {
	if _, ok := n.err.(*marker); ok {
		return nil, false
	}
	carrier, ok := n.err.(interface{ GRPCStatus() *status.Status })
	if !ok {
		return nil, false
	}
	st := carrier.GRPCStatus()
	if st == nil || st.Code() == codes.OK || st.Code() == codes.Canceled || st.Code() == codes.DeadlineExceeded {
		return nil, false
	}
	reason := ""
	var metadata map[string]string
	if info := errorInfo(st); info != nil {
		reason = info.GetReason()
		metadata = make(map[string]string, len(info.GetMetadata()))
		for key, value := range info.GetMetadata() {
			metadata[key] = value
		}
	}
	if n.remote && metadata["fault"] == "" {
		return nil, false
	}
	if reason == "" {
		reason = codeReason(st.Code())
	}
	return &Public{Code: httpCode(st.Code()), Reason: reason, Message: st.Message(), Metadata: metadata, Err: n.err}, true
}

func errorInfo(st *status.Status) *errdetails.ErrorInfo {
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

// remoteFaultOf reads the fault the remote reported. An apperror an RPC
// client restored from the ErrorInfo reason cannot carry fault in its args;
// the client keeps the received status on the chain under the same Remote
// marker, and the fault is read from there. Statuses outside the marker
// belong to other calls and are not read.
func remoteFaultOf(public *Public, below []node) string {
	if f, ok := validFault(public.Metadata["fault"]); ok {
		return string(f)
	}
	for _, n := range below {
		if !n.remote {
			continue
		}
		carrier, ok := n.err.(interface{ GRPCStatus() *status.Status })
		if !ok {
			continue
		}
		st := carrier.GRPCStatus()
		if st == nil {
			continue
		}
		if info := errorInfo(st); info != nil {
			if f, ok := validFault(info.GetMetadata()["fault"]); ok {
				return string(f)
			}
		}
	}
	return ""
}

// httpCode maps a gRPC code to an HTTP status, following the HTTP Mapping
// notes in google/rpc/code.proto.
func httpCode(code codes.Code) int {
	switch code {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return 499
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// codeReason is the reason of a status without ErrorInfo: the code name in
// lower snake case, NotFound as not_found, matching the lower-case catalog
// codes.
func codeReason(code codes.Code) string {
	name := code.String()
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// callerEnded reports whether the caller ended the context: its cause is
// exactly context.Canceled or context.DeadlineExceeded. A context ended for
// another reason — a timeout this process set, or an internal cause passed to
// a CancelCauseFunc — was ended by this process.
func callerEnded(cause error) bool {
	return cause == context.Canceled || cause == context.DeadlineExceeded
}

func isCancellation(err error) bool {
	if reflect.TypeOf(err).Comparable() && (err == context.Canceled || err == context.DeadlineExceeded) {
		return true
	}
	if matcher, ok := err.(interface{ Is(error) bool }); ok {
		return matcher.Is(context.Canceled) || matcher.Is(context.DeadlineExceeded)
	}
	return false
}

func containsCanceled(nodes []node) bool {
	for _, n := range nodes {
		if isCancellation(n.err) {
			return true
		}
		if public, ok := publicOf(n); ok {
			// A 499 or 504 received from a peer is the peer reporting a
			// cancellation or timeout; one built in this process is a public error.
			if (n.remote || public.Metadata["fault"] != "") && (public.Code == 499 || public.Code == http.StatusGatewayTimeout) {
				return true
			}
			continue
		}
		if g, ok := n.err.(interface{ GRPCStatus() *status.Status }); ok {
			if st := g.GRPCStatus(); st != nil && (st.Code() == codes.Canceled || st.Code() == codes.DeadlineExceeded) {
				return true
			}
		}
	}
	return false
}

func validFault(s string) (Fault, bool) {
	f := Fault(s)
	return f, f == FaultClient || f == FaultServer || f == FaultDependency || f == FaultCanceled
}

func remoteFault(metadataFault string, forwarded bool) Fault {
	if f, ok := validFault(metadataFault); ok && f == FaultClient {
		if forwarded {
			return FaultClient
		}
		return FaultServer
	}
	return FaultDependency
}

// LogAttrs returns the error fields of a result or event record.
func (r Report) LogAttrs() []slog.Attr {
	out := []slog.Attr{slog.String("fault", string(r.Fault)), slog.String("reason", r.Reason), slog.String("error", r.Text)}
	if r.Source != nil {
		out = append(out, slog.Any("error_source", *r.Source))
	}
	if len(r.Stack) > 0 {
		values := make([]map[string]any, len(r.Stack))
		for i, frame := range r.Stack {
			values[i] = map[string]any{
				"function": frame.Function,
				"file":     frame.File,
				"line":     frame.Line,
			}
		}
		out = append(out, slog.Any("error_stack", values))
	}
	if len(r.Attrs) > 0 {
		values := make([]any, len(r.Attrs))
		for i := range r.Attrs {
			values[i] = r.Attrs[i]
		}
		out = append(out, slog.Group("error_attrs", values...))
	}
	if r.Remote {
		out = append(out, slog.Bool("remote", true))
	}
	if r.RemoteFault != "" {
		out = append(out, slog.String("remote_fault", r.RemoteFault))
	}
	if r.Panic {
		out = append(out, slog.Bool("panic", true))
	}
	return out
}

// urlPattern matches a URL of any scheme: a database DSN
// (postgres://user:pass@…) carries credentials too.
var urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s]+`)

// RedactURLs removes the userinfo, query and fragment of every URL in text.
// Rendering a chain applies it; text from outside the process (an upstream
// error message) must go through it before it becomes an attribute. A URL
// that does not parse keeps only its scheme, since the rest may hold
// credentials.
func RedactURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		leading, trailing := "", ""
		for raw != "" && strings.ContainsRune("([{\"'", rune(raw[0])) {
			leading, raw = leading+raw[:1], raw[1:]
		}
		for raw != "" && strings.ContainsRune(".,;:!?)]}\"'", rune(raw[len(raw)-1])) {
			trailing, raw = raw[len(raw)-1:]+trailing, raw[:len(raw)-1]
		}
		u, err := url.Parse(raw)
		if err != nil {
			return leading + raw[:strings.Index(raw, "://")+3] + "[redacted]" + trailing
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return leading + u.String() + trailing
	})
}

// Text returns the redacted text of the whole chain.
func Text(err error) string {
	return render(err)
}

func render(err error) string {
	if err == nil {
		return ""
	}
	switch e := err.(type) {
	case *faultError:
		switch {
		case e.cause == nil:
			return RedactURLs(e.msg)
		case e.msg == "":
			return render(e.cause)
		}
		return RedactURLs(e.msg) + ": " + render(e.cause)
	case *marker:
		return render(e.err)
	case interface{ Unwrap() []error }:
		parts := make([]string, 0)
		for _, child := range e.Unwrap() {
			if text := render(child); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "; ")
	}
	text := err.Error()
	child := single(err)
	if child == nil {
		return RedactURLs(text)
	}
	if childText := child.Error(); strings.HasSuffix(text, childText) {
		return RedactURLs(strings.TrimSuffix(text, childText)) + render(child)
	}
	if _, unwraps := err.(interface{ Unwrap() error }); !unwraps {
		// A Cause()-only error keeps its cause out of Error() on purpose, as
		// apperror does: Error() is the code alone. The diagnostic text still
		// needs the cause.
		if childText := render(child); childText != "" {
			return RedactURLs(text) + ": " + childText
		}
	}
	return RedactURLs(text)
}
