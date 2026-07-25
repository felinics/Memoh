package channel

import "errors"

var (
	ErrUnknown            = errors.New("unknown channel error")
	ErrInvalidArgument    = errors.New("invalid channel argument")
	ErrUnauthenticated    = errors.New("channel transport unauthenticated")
	ErrUnavailable        = errors.New("channel transport unavailable")
	ErrTeamNotServed      = errors.New("channel team not served")
	ErrForbidden          = errors.New("channel operation forbidden")
	ErrConfigNotFound     = errors.New("channel config not found")
	ErrDiscoveryFailed    = errors.New("channel discovery failed")
	ErrEnableFailed       = errors.New("channel enable failed")
	ErrInvalidWebhook     = errors.New("invalid channel webhook")
	ErrWebhookUnsupported = errors.New("channel webhook unsupported")
	ErrPayloadTooLarge    = errors.New("channel payload too large")
	ErrProviderFailed     = errors.New("channel provider failed")
)

type ErrorReason int32

const (
	ErrorReasonUnspecified        ErrorReason = 0
	ErrorReasonConfigNotFound     ErrorReason = 1
	ErrorReasonDiscoveryFailed    ErrorReason = 2
	ErrorReasonEnableFailed       ErrorReason = 3
	ErrorReasonInvalidWebhook     ErrorReason = 4
	ErrorReasonWebhookUnsupported ErrorReason = 5
	ErrorReasonPayloadTooLarge    ErrorReason = 6
	ErrorReasonProviderFailed     ErrorReason = 7
	ErrorReasonTeamNotServed      ErrorReason = 8
	ErrorReasonForbidden          ErrorReason = 9
)

// ErrorDetail is the stable Channel-owned detail attached to canonical gRPC status.
type ErrorDetail struct {
	Reason     ErrorReason
	Field      string
	ResourceID string
	Limit      uint64
	Retryable  bool
}

// DomainError preserves a Channel-owned sentinel together with structured detail.
type DomainError struct {
	Cause  error
	Detail ErrorDetail
}

func NewDomainError(cause error, detail ErrorDetail) *DomainError {
	if cause == nil {
		cause = ErrUnknown
	}
	return &DomainError{Cause: cause, Detail: detail}
}

func (e *DomainError) Error() string {
	if e == nil || e.Cause == nil {
		return ErrUnknown.Error()
	}
	return e.Cause.Error()
}

func (e *DomainError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrUnknown
	}
	return e.Cause
}
