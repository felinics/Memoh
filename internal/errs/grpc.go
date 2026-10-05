//nolint:errorlint // walk visits every node of the chain and each check is about that node alone; errors.As would match a deeper node instead.
package errs

import (
	"net/http"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func remoteFault(metadataFault string, forwarded bool) Fault {
	if f, ok := validFault(metadataFault); ok && f == FaultClient {
		if forwarded {
			return FaultClient
		}
		return FaultServer
	}
	return FaultDependency
}
