package apperror

import (
	"net/http"

	"google.golang.org/grpc/codes"
)

// Kind is the transport-neutral error category. It is the only mandatory part
// of an error's identity and it carries no HTTP concept, which is what lets
// application and service layers produce errors without depending on a
// transport.
//
// The enum is closed: it mirrors the gRPC canonical codes folded down to the
// twelve categories this project distinguishes. Adding a Kind is a change to
// docs/architecture/error-contract-decision.md, not a routine edit.
type Kind uint8

const (
	// KindInternal is the zero value so that an unclassified error degrades to
	// the safest possible rendering rather than leaking through as a 200.
	KindInternal Kind = iota
	KindInvalid
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindFailedPrecondition
	KindExhausted
	KindCanceled
	KindDeadlineExceeded
	KindUnavailable
	KindUnimplemented
)

// statusCanceled is nginx's "client closed request". It is not IANA
// registered; a canceled request usually has no reader left, so this mostly
// feeds logs and metrics.
const statusCanceled = 499

type kindProfile struct {
	name       string
	httpStatus int
	grpcCode   codes.Code
	jsonRPC    int
	title      string
	detail     string
}

// kindProfiles is the single source of truth for every Kind projection. Tests
// assert it covers all twelve Kinds and that no two entries collide.
var kindProfiles = map[Kind]kindProfile{
	KindInternal: {
		name: "internal", httpStatus: http.StatusInternalServerError,
		grpcCode: codes.Internal, jsonRPC: -32603,
		title:  "Internal Server Error",
		detail: "Something went wrong on our side. Please try again.",
	},
	KindInvalid: {
		name: "invalid", httpStatus: http.StatusBadRequest,
		grpcCode: codes.InvalidArgument, jsonRPC: -32602,
		title:  "Bad Request",
		detail: "The request is invalid.",
	},
	KindUnauthenticated: {
		name: "unauthenticated", httpStatus: http.StatusUnauthorized,
		grpcCode: codes.Unauthenticated, jsonRPC: -32001,
		title:  "Unauthorized",
		detail: "Authentication is required.",
	},
	KindForbidden: {
		name: "forbidden", httpStatus: http.StatusForbidden,
		grpcCode: codes.PermissionDenied, jsonRPC: -32003,
		title:  "Forbidden",
		detail: "You do not have permission to perform this action.",
	},
	KindNotFound: {
		name: "not_found", httpStatus: http.StatusNotFound,
		grpcCode: codes.NotFound, jsonRPC: -32004,
		title:  "Not Found",
		detail: "The requested resource was not found.",
	},
	KindConflict: {
		name: "conflict", httpStatus: http.StatusConflict,
		grpcCode: codes.AlreadyExists, jsonRPC: -32009,
		title:  "Conflict",
		detail: "The request conflicts with the current state.",
	},
	KindFailedPrecondition: {
		name: "failed_precondition", httpStatus: http.StatusUnprocessableEntity,
		grpcCode: codes.FailedPrecondition, jsonRPC: -32022,
		title:  "Unprocessable Entity",
		detail: "The request cannot be completed in the current state.",
	},
	KindExhausted: {
		name: "exhausted", httpStatus: http.StatusTooManyRequests,
		grpcCode: codes.ResourceExhausted, jsonRPC: -32029,
		title:  "Too Many Requests",
		detail: "Too many requests. Please try again later.",
	},
	KindCanceled: {
		name: "canceled", httpStatus: statusCanceled,
		grpcCode: codes.Canceled, jsonRPC: -32099,
		title:  "Client Closed Request",
		detail: "The request was canceled.",
	},
	KindDeadlineExceeded: {
		name: "deadline_exceeded", httpStatus: http.StatusGatewayTimeout,
		grpcCode: codes.DeadlineExceeded, jsonRPC: -32005,
		title:  "Gateway Timeout",
		detail: "The request timed out. Please try again.",
	},
	KindUnavailable: {
		name: "unavailable", httpStatus: http.StatusServiceUnavailable,
		grpcCode: codes.Unavailable, jsonRPC: -32000,
		title:  "Service Unavailable",
		detail: "The service is temporarily unavailable. Please try again.",
	},
	KindUnimplemented: {
		name: "unimplemented", httpStatus: http.StatusNotImplemented,
		grpcCode: codes.Unimplemented, jsonRPC: -32601,
		title:  "Not Implemented",
		detail: "This operation is not supported.",
	},
}

func (k Kind) profile() kindProfile {
	if profile, ok := kindProfiles[k]; ok {
		return profile
	}
	return kindProfiles[KindInternal]
}

// String is the stable snake_case identity used for logs, the OpenTelemetry
// error.type attribute, and the errors.kind.* frontend i18n keys.
func (k Kind) String() string { return k.profile().name }

func (k Kind) HTTPStatus() int      { return k.profile().httpStatus }
func (k Kind) GRPCCode() codes.Code { return k.profile().grpcCode }
func (k Kind) JSONRPCCode() int     { return k.profile().jsonRPC }

// Title is the RFC 9457 title used when an error carries no catalog code.
func (k Kind) Title() string { return k.profile().title }

// Detail is the user-safe English fallback shown when an error carries no
// catalog code. Clients localize it through errors.kind.<name>.
func (k Kind) Detail() string { return k.profile().detail }

// httpStatusKinds is the migration lookup table: it turns a hand-written HTTP
// status into a Kind without requiring judgement at the call site. 502 folds
// into KindUnavailable on purpose, so those endpoints now answer 503.
var httpStatusKinds = map[int]Kind{
	http.StatusBadRequest:          KindInvalid,
	http.StatusUnauthorized:        KindUnauthenticated,
	http.StatusForbidden:           KindForbidden,
	http.StatusNotFound:            KindNotFound,
	http.StatusConflict:            KindConflict,
	http.StatusUnprocessableEntity: KindFailedPrecondition,
	http.StatusTooManyRequests:     KindExhausted,
	statusCanceled:                 KindCanceled,
	http.StatusInternalServerError: KindInternal,
	http.StatusNotImplemented:      KindUnimplemented,
	http.StatusBadGateway:          KindUnavailable,
	http.StatusServiceUnavailable:  KindUnavailable,
	http.StatusGatewayTimeout:      KindDeadlineExceeded,
}

// KindFromHTTPStatus classifies a status produced outside the contract, such
// as a legacy echo.HTTPError still awaiting migration. Unmapped statuses fold
// by class so that an unexpected 5xx never renders as a client error.
func KindFromHTTPStatus(status int) Kind {
	if kind, ok := httpStatusKinds[status]; ok {
		return kind
	}
	if status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return KindInvalid
	}
	return KindInternal
}

// grpcCodeKinds folds the four canonical codes this project does not
// distinguish: ABORTED and OUT_OF_RANGE carry no handling that CONFLICT and
// INVALID_ARGUMENT do not already cover, and DATA_LOSS and UNKNOWN are
// internal faults.
var grpcCodeKinds = map[codes.Code]Kind{
	codes.Canceled:           KindCanceled,
	codes.Unknown:            KindInternal,
	codes.InvalidArgument:    KindInvalid,
	codes.DeadlineExceeded:   KindDeadlineExceeded,
	codes.NotFound:           KindNotFound,
	codes.AlreadyExists:      KindConflict,
	codes.PermissionDenied:   KindForbidden,
	codes.ResourceExhausted:  KindExhausted,
	codes.FailedPrecondition: KindFailedPrecondition,
	codes.Aborted:            KindConflict,
	codes.OutOfRange:         KindInvalid,
	codes.Unimplemented:      KindUnimplemented,
	codes.Internal:           KindInternal,
	codes.Unavailable:        KindUnavailable,
	codes.DataLoss:           KindInternal,
	codes.Unauthenticated:    KindUnauthenticated,
}

// KindFromGRPCCode classifies an inbound gRPC status.
func KindFromGRPCCode(code codes.Code) Kind {
	if kind, ok := grpcCodeKinds[code]; ok {
		return kind
	}
	return KindInternal
}
