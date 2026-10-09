//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

// public is a public error found on a chain: a catalog apperror, or a native
// gRPC status. Both take part in attribution; only the apperror can be an
// answer.
type public struct {
	// app is the catalog apperror. It is nil for a native gRPC status.
	app *apperror.Error
	// status is the HTTP status of the catalog entry. It is zero for a native
	// gRPC status: its gRPC code is not converted to an HTTP status here.
	status int
	// class is the fault the status alone gives: client for a 4xx catalog
	// status or a gRPC code that refuses the request, server otherwise.
	class apperror.Fault
	// reason is the apperror catalog code, or the ErrorInfo reason of a
	// native gRPC status.
	reason   string
	metadata map[string]string
	// fault is the attribution the catalog entry declares for the code. It is
	// empty when the entry declares none and for a native gRPC status; the
	// fault then follows from the status and the rest of the chain.
	fault apperror.Fault
}

// Report is what Analyze concludes about an error chain at a boundary.
type Report struct {
	Fault       apperror.Fault
	Reason      string
	Text        string
	Source      *Frame
	Stack       []Frame
	Attrs       []slog.Attr
	Remote      bool
	RemoteFault apperror.Fault
	Unlocated   bool
	Panic       bool
	// answer is the catalog error Answer returns. It is nil when the chain
	// holds none, and also when the chain holds one that must not be
	// returned: the caller has canceled, or a remote refused a request this
	// process sent (fault server), which says nothing about this process's
	// caller. Reason still takes that error's reason.
	answer *apperror.Error
	// Recorded reports that a nested unit in this process has already
	// recorded this failure (see Recorded).
	Recorded bool
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
		n.remote = remote || m.kind == markerRemote
		n.forwarded = forwarded || m.kind == markerForwarded
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
	return analyze(err, CallerEnded(ctx))
}

// CallerEnded reports whether ctx has ended because its caller canceled it
// or its deadline passed: its cause is exactly context.Canceled or
// context.DeadlineExceeded. A context this process ended for its own reason,
// such as an idle timeout or a lost run, did not end by its caller. A nil ctx
// has no caller and never ends.
func CallerEnded(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil && callerEnded(context.Cause(ctx))
}

// Answer is the public error a boundary answers err with, and the fault it
// attributes err to. ctx is the unit's own context; nil means no caller. Every
// transport renders its answer from it, in this order:
//
//  1. The caller has ended and the chain holds a cancellation: canceled.
//  2. The outermost catalog apperror on the chain, unless it came from a
//     remote that refused a request this process sent and was not forwarded.
//  3. The generic code for the fault: http.bad_request for a client fault,
//     internal otherwise.
//
// A native gRPC status takes part in the attribution but is never the
// answer. The answer of a nil err is nil.
func Answer(ctx context.Context, err error) (*apperror.Error, apperror.Fault) {
	if err == nil {
		return nil, ""
	}
	r := Analyze(ctx, err)
	switch {
	case r.Fault == apperror.FaultCanceled:
		return apperror.Wrap(apperror.CodeCanceled, err, nil), r.Fault
	case r.answer != nil:
		return r.answer, r.Fault
	case r.Fault == apperror.FaultClient:
		return apperror.Wrap(apperror.CodeHTTPBadRequest, err, nil), r.Fault
	default:
		return apperror.Wrap(apperror.CodeInternal, err, nil), r.Fault
	}
}

// FaultOf is the fault Analyze attributes err to when no caller has canceled
// the unit. An RPC server sends it to its client with the error.
func FaultOf(err error) apperror.Fault {
	return analyze(err, false).Fault
}

// analyze attributes err. ended reports that the unit's caller has
// canceled it or its deadline has passed.
func analyze(err error, ended bool) Report {
	r := Report{Fault: apperror.FaultServer, Reason: string(apperror.CodeInternal), Text: Text(err)}
	if err == nil {
		return r
	}
	var nodes []node
	walk(err, false, false, func(n node) bool {
		nodes = append(nodes, n)
		return true
	})
	var found *public
	publicForwarded := false
	for i, n := range nodes {
		if n.remote {
			r.Remote = true
		}
		p, ok := publicOf(n)
		if !ok {
			continue
		}
		found = p
		r.Reason = p.reason
		r.Remote = n.remote
		publicForwarded = n.forwarded
		if r.Remote {
			r.RemoteFault = remoteFaultOf(p, nodes[i+1:])
		}
		break
	}
	for _, n := range nodes {
		if m, ok := n.err.(*marker); ok && m.kind == markerRecorded {
			r.Recorded = true
			break
		}
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
	case ended && containsCanceled(nodes):
		r.Fault = apperror.FaultCanceled
		r.Reason = string(apperror.CodeCanceled)
	case found != nil && r.Remote:
		r.Fault = remoteFault(r.RemoteFault, publicForwarded)
	case found != nil && found.fault != "":
		r.Fault = found.fault
	case found != nil && found.class == apperror.FaultClient:
		r.Fault = apperror.FaultClient
	default:
		r.Fault = causeFault(nodes)
	}
	if found != nil && r.Fault != apperror.FaultCanceled && (!r.Remote || r.Fault != apperror.FaultServer) {
		r.answer = found.app
	}
	r.Unlocated = (r.Fault == apperror.FaultServer || r.Fault == apperror.FaultDependency) && len(r.Stack) == 0
	return r
}

// causeFault attributes a failure this process answers with a 5xx public
// error of its own, or with none. It is a dependency's when its cause is
// marked with WrapDependency or was received from another service, unless
// that service reported that it refused this process's request; otherwise it
// is this process's.
func causeFault(nodes []node) apperror.Fault {
	for i, n := range nodes {
		if e, ok := n.err.(*faultError); ok && e.dependency {
			return apperror.FaultDependency
		}
		if n.remote {
			if reportedFault(nodes[i:]) == apperror.FaultClient {
				return apperror.FaultServer
			}
			return apperror.FaultDependency
		}
	}
	return apperror.FaultServer
}

// publicOf recognizes the two forms of public error: an apperror with a
// catalog code, and a native gRPC status.
func publicOf(n node) (*public, bool) {
	if p, ok := appPublic(n.err); ok {
		return p, true
	}
	return grpcPublic(n)
}

// appPublic recognizes an apperror whose code is in the catalog, with the
// fault its entry declares. An unregistered code is not a public error: the
// HTTP boundary cannot render it as a Problem either, and responds internal.
func appPublic(err error) (*public, bool) {
	e, ok := err.(*apperror.Error)
	if !ok {
		return nil, false
	}
	code := apperror.CodeOf(e)
	definition, ok := apperror.Lookup(code)
	if !ok {
		return nil, false
	}
	class := apperror.FaultServer
	if definition.HTTPStatus < http.StatusInternalServerError {
		class = apperror.FaultClient
	}
	return &public{app: e, status: definition.HTTPStatus, class: class, reason: string(code), metadata: apperror.ArgsOf(e), fault: definition.Fault}, true
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
		if p, ok := publicOf(n); ok {
			// A 499 or 504 catalog code received from a peer is the peer
			// reporting a cancellation or timeout; one built in this process is
			// a public error. A native status never matches: grpcPublic does not
			// recognize Canceled or DeadlineExceeded, and its status is zero.
			if n.remote && (p.status == 499 || p.status == http.StatusGatewayTimeout) {
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
