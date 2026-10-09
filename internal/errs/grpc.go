//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
)

// Remote marks an error received from an internal RPC. The error value is
// unchanged: status.FromError and status.Code still read the received status.
func Remote(err error) error {
	if err == nil {
		return nil
	}
	return &marker{kind: markerRemote, err: err}
}

// Forwarded declares that the caller deliberately passed end-user input to
// the downstream, so a remote client fault stays a client fault here.
func Forwarded(err error) error {
	if err == nil {
		return nil
	}
	return &marker{kind: markerForwarded, err: err}
}

// Recorded marks a failure another result record in this process has already
// recorded: a nested unit that wrote its own record when it ended. An outer
// boundary still records it, at no more than WARN. The error value is
// unchanged.
func Recorded(err error) error {
	if err == nil {
		return nil
	}
	return &marker{kind: markerRecorded, err: err}
}

// markerKind is what a marker declares about the error it wraps.
type markerKind int

const (
	markerRemote markerKind = iota + 1
	markerForwarded
	markerRecorded
)

type marker struct {
	kind markerKind
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

// grpcPublic recognizes a native gRPC status as a public error.
// Canceled and DeadlineExceeded are produced by the gRPC client when a context
// ends and are not public. A remote-marked status whose ErrorInfo carries no
// fault was produced by the gRPC client itself, or by a peer that does not
// write the envelope; its text may name internal addresses and is not public.
func grpcPublic(n node) (*public, bool) {
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
	return &public{class: faultOfCode(st.Code()), reason: reason, metadata: metadata}, true
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
func remoteFaultOf(p *public, below []node) apperror.Fault {
	if f, ok := apperror.ParseFault(p.metadata["fault"]); ok {
		return f
	}
	return reportedFault(below)
}

// reportedFault is the fault written on the first received status among
// nodes that carries one, or empty.
func reportedFault(nodes []node) apperror.Fault {
	for _, n := range nodes {
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
			if f, ok := apperror.ParseFault(info.GetMetadata()["fault"]); ok {
				return f
			}
		}
	}
	return ""
}

// faultOfCode is the fault a gRPC code alone gives: client for the codes that
// refuse the request, the ones google/rpc/code.proto maps to a 4xx HTTP
// status, and server for the rest.
func faultOfCode(code codes.Code) apperror.Fault {
	switch code {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange,
		codes.NotFound, codes.AlreadyExists, codes.Aborted,
		codes.PermissionDenied, codes.Unauthenticated, codes.ResourceExhausted:
		return apperror.FaultClient
	default:
		return apperror.FaultServer
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

func remoteFault(reported apperror.Fault, forwarded bool) apperror.Fault {
	if reported == apperror.FaultClient {
		if forwarded {
			return apperror.FaultClient
		}
		return apperror.FaultServer
	}
	return apperror.FaultDependency
}
