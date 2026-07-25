package codec

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/memohai/memoh/domains/channel"
	"github.com/memohai/memoh/internal/rpc/channel/channelpb"
)

type rpcError struct {
	status *status.Status
	cause  error
}

func (e *rpcError) Error() string              { return e.status.Err().Error() }
func (e *rpcError) Unwrap() error              { return e.cause }
func (e *rpcError) GRPCStatus() *status.Status { return e.status }

func EncodeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "channel operation canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "channel operation deadline exceeded")
	}

	domain := domainError(err)
	cause := domain.Cause
	if cause == nil {
		cause = channel.ErrUnknown
	}
	code := codeFor(cause)
	wire := &channelpb.ErrorDetail{Reason: channelpb.ChannelErrorReason(domain.Detail.Reason), Field: domain.Detail.Field, ResourceId: domain.Detail.ResourceID, Limit: domain.Detail.Limit, Retryable: domain.Detail.Retryable}
	st := status.New(code, cause.Error())
	withDetail, detailErr := st.WithDetails(wire)
	if detailErr != nil {
		return status.Error(codes.Internal, channel.ErrUnknown.Error())
	}
	return withDetail.Err()
}

func DecodeError(err error) error {
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	switch st.Code() {
	case codes.Canceled:
		return &rpcError{status: st, cause: context.Canceled}
	case codes.DeadlineExceeded:
		return &rpcError{status: st, cause: context.DeadlineExceeded}
	case codes.Unauthenticated:
		return &rpcError{status: st, cause: errors.Join(channel.ErrUnavailable, channel.ErrUnauthenticated)}
	case codes.Unavailable:
		return &rpcError{status: st, cause: channel.ErrUnavailable}
	}

	detail := channel.ErrorDetail{}
	for _, item := range st.Details() {
		if wire, ok := item.(*channelpb.ErrorDetail); ok {
			detail = channel.ErrorDetail{Reason: channel.ErrorReason(wire.GetReason()), Field: wire.GetField(), ResourceID: wire.GetResourceId(), Limit: wire.GetLimit(), Retryable: wire.GetRetryable()}
			break
		}
	}
	cause := causeFor(st.Code(), detail.Reason)
	return &rpcError{status: st, cause: channel.NewDomainError(cause, detail)}
}

func domainError(err error) *channel.DomainError {
	var domain *channel.DomainError
	if errors.As(err, &domain) {
		return domain
	}
	return channel.NewDomainError(sentinel(err), channel.ErrorDetail{Reason: reasonFor(err)})
}

func sentinel(err error) error {
	for _, candidate := range []error{channel.ErrInvalidArgument, channel.ErrTeamNotServed, channel.ErrForbidden, channel.ErrConfigNotFound, channel.ErrDiscoveryFailed, channel.ErrEnableFailed, channel.ErrInvalidWebhook, channel.ErrWebhookUnsupported, channel.ErrPayloadTooLarge, channel.ErrProviderFailed} {
		if errors.Is(err, candidate) {
			return candidate
		}
	}
	return channel.ErrUnknown
}

func codeFor(err error) codes.Code {
	switch {
	case errors.Is(err, channel.ErrInvalidArgument), errors.Is(err, channel.ErrInvalidWebhook):
		return codes.InvalidArgument
	case errors.Is(err, channel.ErrTeamNotServed), errors.Is(err, channel.ErrForbidden):
		return codes.PermissionDenied
	case errors.Is(err, channel.ErrConfigNotFound):
		return codes.NotFound
	case errors.Is(err, channel.ErrDiscoveryFailed), errors.Is(err, channel.ErrEnableFailed), errors.Is(err, channel.ErrWebhookUnsupported):
		return codes.FailedPrecondition
	case errors.Is(err, channel.ErrPayloadTooLarge):
		return codes.ResourceExhausted
	default:
		return codes.Internal
	}
}

func reasonFor(err error) channel.ErrorReason {
	switch {
	case errors.Is(err, channel.ErrTeamNotServed):
		return channel.ErrorReasonTeamNotServed
	case errors.Is(err, channel.ErrForbidden):
		return channel.ErrorReasonForbidden
	case errors.Is(err, channel.ErrConfigNotFound):
		return channel.ErrorReasonConfigNotFound
	case errors.Is(err, channel.ErrDiscoveryFailed):
		return channel.ErrorReasonDiscoveryFailed
	case errors.Is(err, channel.ErrEnableFailed):
		return channel.ErrorReasonEnableFailed
	case errors.Is(err, channel.ErrInvalidWebhook):
		return channel.ErrorReasonInvalidWebhook
	case errors.Is(err, channel.ErrWebhookUnsupported):
		return channel.ErrorReasonWebhookUnsupported
	case errors.Is(err, channel.ErrPayloadTooLarge):
		return channel.ErrorReasonPayloadTooLarge
	case errors.Is(err, channel.ErrProviderFailed):
		return channel.ErrorReasonProviderFailed
	default:
		return channel.ErrorReasonUnspecified
	}
}

func causeFor(code codes.Code, reason channel.ErrorReason) error {
	switch reason {
	case channel.ErrorReasonTeamNotServed:
		return channel.ErrTeamNotServed
	case channel.ErrorReasonForbidden:
		return channel.ErrForbidden
	case channel.ErrorReasonConfigNotFound:
		return channel.ErrConfigNotFound
	case channel.ErrorReasonDiscoveryFailed:
		return channel.ErrDiscoveryFailed
	case channel.ErrorReasonEnableFailed:
		return channel.ErrEnableFailed
	case channel.ErrorReasonInvalidWebhook:
		return channel.ErrInvalidWebhook
	case channel.ErrorReasonWebhookUnsupported:
		return channel.ErrWebhookUnsupported
	case channel.ErrorReasonPayloadTooLarge:
		return channel.ErrPayloadTooLarge
	case channel.ErrorReasonProviderFailed:
		return channel.ErrProviderFailed
	}
	switch code {
	case codes.InvalidArgument:
		return channel.ErrInvalidArgument
	case codes.PermissionDenied:
		return channel.ErrTeamNotServed
	case codes.NotFound:
		return channel.ErrConfigNotFound
	default:
		return channel.ErrUnknown
	}
}
