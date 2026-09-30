// Package providerfail maps provider domain failures at application boundaries.
package providerfail

import (
	"context"
	"errors"
	"log/slog"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chatgptplan"
)

func ChatGPT(ctx context.Context, log *slog.Logger, err error) error {
	var code apperror.Code
	switch {
	case errors.Is(err, chatgptplan.ErrEncryptionUnavailable):
		code = apperror.CodeChatGPTEncryptionUnavailable
	case errors.Is(err, chatgptplan.ErrOwner):
		code = apperror.CodeChatGPTOwnerRequired
	case errors.Is(err, chatgptplan.ErrNotConnected), errors.Is(err, chatgptplan.ErrRefreshInvalid):
		code = apperror.CodeChatGPTNotConnected
	case errors.Is(err, chatgptplan.ErrInvalidAuthorization):
		code = apperror.CodeChatGPTAuthorizationInvalid
	case errors.Is(err, chatgptplan.ErrQuota):
		code = apperror.CodeChatGPTUsageLimit
	case errors.Is(err, chatgptplan.ErrNotEligible):
		code = apperror.CodeChatGPTNotEligible
	case errors.Is(err, chatgptplan.ErrCapability):
		code = apperror.CodeChatGPTCapabilityUnsupported
	case errors.Is(err, chatgptplan.ErrPermission):
		code = apperror.CodeChatGPTPermissionRequired
	case errors.Is(err, chatgptplan.ErrInterrupted):
		code = apperror.CodeAgentResponseInterrupted
	case errors.Is(err, chatgptplan.ErrUpstream):
		code = apperror.CodeChatGPTUnavailable
	default:
		return err
	}
	var upstream *chatgptplan.UpstreamError
	if errors.As(err, &upstream) && log != nil {
		log.WarnContext(ctx, "ChatGPT upstream request failed", slog.Any("error", upstream))
	}
	return apperror.Wrap(code, err, nil)
}
