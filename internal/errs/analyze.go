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

// Reasons used when the chain has no public error. They are the catalog codes
// apperror uses for the same two outcomes at the HTTP boundary.
const (
	reasonInternal = "internal"
	reasonCanceled = "canceled"
)

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
