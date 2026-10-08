package rpc

import (
	"errors"
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/redact"
)

// The error envelope of the internal RPCs is a gRPC status carrying one
// google.rpc.ErrorInfo. The reason is a catalog code, or a reason an RPC
// package registers for one of its sentinels; the metadata carries the
// catalog args and, for a catalog code, the server's attribution under
// MetadataFault. The status message is fixed English and carries no detail.
// Text an adapter produced that the caller needs, such as a channel
// platform's rejection reason, travels redacted under MetadataAdapterMessage.
//
// Each RPC package keeps one Reasons table, read by its server encoding and
// its client decoding.

// errorDomain is the ErrorInfo domain of every internal RPC envelope.
const errorDomain = "memoh.internal"

// MetadataAdapterMessage is the ErrorInfo metadata key for an adapter's own
// error text.
const MetadataAdapterMessage = "adapter_message"

// MetadataFault is the ErrorInfo metadata key for the fault the server
// attributes the failure to. The client's diagnostics read it as the remote
// fault; a status without it, from a server that predates it, is attributed
// as a dependency failure.
const MetadataFault = "fault"

// Reason registers one sentinel of an RPC package on the wire.
type Reason struct {
	// Err is the sentinel. A client restores it so that errors.Is holds.
	Err error
	// Reason is the ErrorInfo reason. It is stable across releases.
	Reason string
	// Code is the status code.
	Code codes.Code
	// Message is the fixed English status message.
	Message string
}

// Status returns the envelope for r. A non-empty adapterMessage is redacted
// and carried under MetadataAdapterMessage.
func (r Reason) Status(adapterMessage string) error {
	var metadata map[string]string
	if adapterMessage != "" {
		metadata = map[string]string{MetadataAdapterMessage: redactAdapterMessage(adapterMessage)}
	}
	return envelope(r.Code, r.Message, r.Reason, metadata)
}

// Reasons is the sentinel table of one RPC package.
type Reasons []Reason

// Lookup returns the first entry whose sentinel err matches.
func (t Reasons) Lookup(err error) (Reason, bool) {
	for _, entry := range t {
		if errors.Is(err, entry.Err) {
			return entry, true
		}
	}
	return Reason{}, false
}

// Decode restores the sentinel registered for the reason of a received
// envelope. The adapter message, when present, becomes the restored error's
// text. It returns nil when err carries no envelope or its reason is not in t.
func (t Reasons) Decode(err error) error {
	info := errorInfoOf(err)
	if info == nil {
		return nil
	}
	for _, entry := range t {
		if entry.Reason == info.GetReason() {
			return Restored(WithAdapterMessage(entry.Err, info.GetMetadata()[MetadataAdapterMessage]), err)
		}
	}
	return nil
}

// AppErrorStatus returns the envelope for an apperror whose code is in the
// catalog: the code is the reason, the catalog detail is the message, and the
// metadata holds the args and the fault this process attributes err to. It
// returns nil for any other error.
func AppErrorStatus(err error) error {
	code := apperror.CodeOf(err)
	definition, ok := apperror.Lookup(code)
	if code == "" || !ok {
		return nil
	}
	metadata := apperror.ArgsOf(err)
	metadata[MetadataFault] = string(errs.FaultOf(err))
	return envelope(statusCodeForHTTP(definition.HTTPStatus), definition.Detail, string(code), metadata)
}

// DecodeAppError restores the apperror of a received envelope whose reason is
// a catalog code, with the catalog args from its metadata. The fault stays on
// the received status, where diagnostics read it. It returns nil otherwise.
func DecodeAppError(err error) error {
	info := errorInfoOf(err)
	if info == nil {
		return nil
	}
	code := apperror.Code(info.GetReason())
	if _, ok := apperror.Lookup(code); !ok {
		return nil
	}
	return Restored(apperror.New(code, info.GetMetadata()), err)
}

// ReasonOf returns the ErrorInfo reason of a received envelope, also when err
// is an error Restored returned.
func ReasonOf(err error) (string, bool) {
	info := errorInfoOf(err)
	if info == nil {
		return "", false
	}
	return info.GetReason(), true
}

// Restored is what an RPC client returns for a failure it restored from the
// received status. It reads as the restored error. Its chain holds the
// restored error and the received status under one remote marker, so errors.Is
// and errors.As find the restored value and diagnostics read the status.
func Restored(restored, received error) error {
	return &restoredError{restored: restored, received: received, chain: errs.Remote(errors.Join(restored, received))}
}

// Received returns the status an RPC client received, from an error Restored
// returned; any other error is returned unchanged.
func Received(err error) error {
	var restored *restoredError
	if errors.As(err, &restored) {
		return restored.received
	}
	return err
}

type restoredError struct {
	restored, received, chain error
}

func (e *restoredError) Error() string { return e.restored.Error() }
func (e *restoredError) Unwrap() error { return e.chain }

// WithAdapterMessage returns an error that reads as message and matches
// sentinel under errors.Is. An empty message returns sentinel.
func WithAdapterMessage(sentinel error, message string) error {
	if message == "" {
		return sentinel
	}
	return &adapterMessageError{sentinel: sentinel, message: message}
}

type adapterMessageError struct {
	sentinel error
	message  string
}

func (e *adapterMessageError) Error() string { return e.message }
func (e *adapterMessageError) Unwrap() error { return e.sentinel }

func envelope(code codes.Code, message, reason string, metadata map[string]string) error {
	st, err := status.New(code, message).WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   errorDomain,
		Metadata: metadata,
	})
	if err != nil {
		// WithDetails fails only for an OK status or a detail that cannot be
		// marshaled; neither applies to a failure status with an ErrorInfo.
		return status.Error(code, message)
	}
	return st.Err()
}

// errorInfoOf reads the ErrorInfo of the status on err's chain. For an error
// Restored returned it reads the received status.
func errorInfoOf(err error) *errdetails.ErrorInfo {
	var carrier interface{ GRPCStatus() *status.Status }
	if !errors.As(Received(err), &carrier) {
		return nil
	}
	st := carrier.GRPCStatus()
	if st == nil {
		return nil
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

func redactAdapterMessage(message string) string {
	return errs.RedactURLs(redact.Text(message))
}

// statusCodeForHTTP maps a catalog HTTP status to a status code, following the HTTP
// Mapping notes in google/rpc/code.proto.
func statusCodeForHTTP(httpStatus int) codes.Code {
	switch httpStatus {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound, http.StatusGone:
		return codes.NotFound
	case http.StatusConflict:
		return codes.Aborted
	case http.StatusPreconditionFailed, http.StatusUpgradeRequired:
		return codes.FailedPrecondition
	case http.StatusRequestEntityTooLarge, http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case 499:
		return codes.Canceled
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	case http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	}
	switch {
	case httpStatus >= 400 && httpStatus < 500:
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}
