package apperror

import (
	"context"
	"errors"
	"strings"
)

// Error is the only error carrier in this project. Its identity has three
// segments: Kind (mandatory, closed enum), Code (optional, stable business
// identity) and op/cause (never leaves the process, diagnostics only).
//
// Unlike the pre-v1 design this type does implement Unwrap. Keeping causes
// reachable is what lets errors.Is(err, context.Canceled) work in the middle
// layers; the guarantee that a cause never reaches a client is enforced by the
// renderers, which only ever read Kind, Code and Args.
type Error struct {
	kind     Kind
	code     Code
	args     map[string]string
	fields   []FieldError
	upstream *Upstream
	op       string
	cause    error
}

// FieldCode is the closed set of reasons a single input can be rejected.
//
// It exists so that the hundreds of hand-written sentences this codebase used
// for client errors ("bot id is required", "skills is required") converge on a
// reusable, localizable identity instead of minting a catalog Code each. A
// catalog Code answers "which business rule failed"; a FieldCode answers "what
// is wrong with this input", and the latter repeats across the whole API.
type FieldCode string

const (
	FieldRequired    FieldCode = "required"
	FieldInvalid     FieldCode = "invalid"
	FieldTooLong     FieldCode = "too_long"
	FieldOutOfRange  FieldCode = "out_of_range"
	FieldTaken       FieldCode = "taken"
	FieldUnsupported FieldCode = "unsupported"
)

var fieldCodes = map[FieldCode]struct{}{
	FieldRequired:    {},
	FieldInvalid:     {},
	FieldTooLong:     {},
	FieldOutOfRange:  {},
	FieldTaken:       {},
	FieldUnsupported: {},
}

// FieldError is a per-field validation detail rendered into the RFC 9457
// errors[] extension member.
type FieldError struct {
	// Pointer is an RFC 6901 JSON Pointer into the request body.
	Pointer string    `json:"pointer"`
	Code    FieldCode `json:"code"`
}

// Required reports a missing input. This is the most common client error in the
// codebase, so it gets a one-argument constructor: the field is both the op and
// the pointer, which keeps the call site shorter than the string it replaces.
func Required(field string) *Error {
	return Field(field, FieldRequired)
}

// Field reports a single rejected input.
func Field(field string, code FieldCode) *Error {
	return Invalid(field, nil).WithFields(FieldError{Pointer: field, Code: code})
}

// pointer normalizes a field name into an RFC 6901 JSON Pointer so that call
// sites may pass either "bot_id" or "/bot_id".
func pointer(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	if strings.HasPrefix(field, "/") {
		return field
	}
	return "/" + field
}

func newError(kind Kind, op string, cause error) *Error {
	return &Error{kind: kind, op: strings.TrimSpace(op), cause: cause}
}

// The twelve constructors below need no registration: an error that carries
// only a Kind still renders as a complete, localizable Problem.
//
// op describes the code site, not the data: it must stay a static lowercase
// phrase such as "create bot" or "archive directory". Interpolating an ID, a
// path or user input makes it both a high-cardinality metric label and a leak
// surface.

func Internal(op string, cause error) *Error { return newError(KindInternal, op, cause) }
func Invalid(op string, cause error) *Error  { return newError(KindInvalid, op, cause) }
func NotFound(op string, cause error) *Error { return newError(KindNotFound, op, cause) }
func Conflict(op string, cause error) *Error { return newError(KindConflict, op, cause) }
func Canceled(op string, cause error) *Error { return newError(KindCanceled, op, cause) }

func Unauthenticated(op string, cause error) *Error {
	return newError(KindUnauthenticated, op, cause)
}
func Forbidden(op string, cause error) *Error { return newError(KindForbidden, op, cause) }

func FailedPrecondition(op string, cause error) *Error {
	return newError(KindFailedPrecondition, op, cause)
}
func Exhausted(op string, cause error) *Error { return newError(KindExhausted, op, cause) }

func DeadlineExceeded(op string, cause error) *Error {
	return newError(KindDeadlineExceeded, op, cause)
}
func Unavailable(op string, cause error) *Error   { return newError(KindUnavailable, op, cause) }
func Unimplemented(op string, cause error) *Error { return newError(KindUnimplemented, op, cause) }

// OfKind builds an error from a Kind resolved at runtime, for example when
// translating an inbound gRPC status. Prefer the named constructors.
func OfKind(kind Kind, op string, cause error) *Error { return newError(kind, op, cause) }

// New creates a public application error identified by a catalog code.
func New(code Code, args map[string]string) *Error {
	return newCoded(code, nil, args)
}

// Wrap retains a private cause for boundary logging. Only catalog-allowed args
// are kept for serialization.
func Wrap(code Code, cause error, args map[string]string) *Error {
	return newCoded(code, cause, args)
}

func newCoded(code Code, cause error, args map[string]string) *Error {
	kind := KindInternal
	if definition, ok := catalog[code]; ok {
		kind = KindFromHTTPStatus(definition.HTTPStatus)
	}
	return &Error{
		kind:  kind,
		code:  code,
		args:  sanitizeArgs(code, args),
		cause: cause,
	}
}

// WithCode promotes an error to a stable business identity, which is how a
// coded error acquires an op:
//
//	apperror.Conflict("create search provider", err).WithCode(apperror.CodeProviderNameTaken, nil)
//
// The catalog owns the Kind of a coded error, so promoting also adopts the
// registered Kind. Otherwise a call site could pair a 409 code with a 500 Kind
// and the status and the code in the same response would disagree.
//
// The code must be registered; an unregistered code is ignored so that a typo
// degrades to the generic Kind rendering instead of an empty contract.
func (e *Error) WithCode(code Code, args map[string]string) *Error {
	if e == nil {
		return nil
	}
	definition, ok := catalog[code]
	if !ok {
		return e
	}
	clone := *e
	clone.kind = KindFromHTTPStatus(definition.HTTPStatus)
	clone.code = code
	clone.args = sanitizeArgs(code, args)
	return &clone
}

// WithFields attaches per-field validation details. Pointers are normalized and
// an unregistered FieldCode degrades to FieldInvalid, so a typo weakens the
// detail instead of putting an unlocalizable string on the wire.
func (e *Error) WithFields(fields ...FieldError) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.fields = append([]FieldError(nil), e.fields...)
	for _, field := range fields {
		if _, ok := fieldCodes[field.Code]; !ok {
			field.Code = FieldInvalid
		}
		field.Pointer = pointer(field.Pointer)
		clone.fields = append(clone.fields, field)
	}
	return &clone
}

// WithUpstream attaches a third party's verbatim error, for the calls where
// this service is a proxy rather than the author of the failure. The message
// must already be free of credentials: the caller owns them, apperror cannot
// recognize them.
//
// The Kind still classifies the failure for status and retry decisions. The
// quotation only adds what the provider said, it never overrides what we
// concluded.
func (e *Error) WithUpstream(provider, message string) *Error {
	if e == nil {
		return nil
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return e
	}
	clone := *e
	clone.upstream = &Upstream{Provider: strings.TrimSpace(provider), Message: message}
	return &clone
}

// Error renders the diagnostic form: "op: identity: cause". It is for logs
// only. Never pass it to a client; forbidigo rejects err.Error() inside
// response construction and 5xx renderings discard handler-supplied text.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	var out strings.Builder
	if e.op != "" {
		out.WriteString(e.op)
		out.WriteString(": ")
	}
	if e.code != "" {
		out.WriteString(string(e.code))
	} else {
		out.WriteString(e.kind.String())
	}
	if e.cause != nil {
		out.WriteString(": ")
		out.WriteString(e.cause.Error())
	}
	return out.String()
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func As(err error) (*Error, bool) {
	var appErr *Error
	if !errors.As(err, &appErr) {
		return nil, false
	}
	return appErr, true
}

// KindOf classifies any error. Errors produced outside the contract fold to
// the safest category, except for the two standard-library cancellations whose
// intent is unambiguous.
func KindOf(err error) Kind {
	if err == nil {
		return KindInternal
	}
	if appErr, ok := As(err); ok {
		return appErr.kind
	}
	switch {
	case errors.Is(err, context.Canceled):
		return KindCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return KindDeadlineExceeded
	default:
		return KindInternal
	}
}

func CodeOf(err error) Code {
	appErr, ok := As(err)
	if !ok {
		return ""
	}
	return appErr.code
}

func ArgsOf(err error) map[string]string {
	appErr, ok := As(err)
	if !ok {
		return map[string]string{}
	}
	return cloneArgs(appErr.args)
}

func FieldsOf(err error) []FieldError {
	appErr, ok := As(err)
	if !ok || len(appErr.fields) == 0 {
		return nil
	}
	return append([]FieldError(nil), appErr.fields...)
}

// UpstreamOf returns the quoted third-party error, if any.
func UpstreamOf(err error) *Upstream {
	appErr, ok := As(err)
	if !ok || appErr.upstream == nil {
		return nil
	}
	quoted := *appErr.upstream
	return &quoted
}

// OpOf returns the logical operation trail for logging. It replaces stack
// traces: the op chain locates the failure without a stack library and without
// anything that could reach a client.
func OpOf(err error) string {
	appErr, ok := As(err)
	if !ok {
		return ""
	}
	return appErr.op
}

// CauseOf returns the private diagnostic cause for boundary logging.
func CauseOf(err error) error {
	appErr, ok := As(err)
	if !ok {
		return nil
	}
	return appErr.cause
}
