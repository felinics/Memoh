package application

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
)

// RuntimeControlError translates a runtime control failure into its public
// error. It is the single sentinel table for every transport that serves
// runtime controls: the service methods apply it before returning, and HTTP
// paths that reach a driver without the service apply it themselves.
//
// An error that already carries a catalog code is returned unchanged, so a
// second translation never masks the first one. An External Agent failure
// keeps the code ExternalAgentError gives it.
func RuntimeControlError(err error) error {
	if err == nil {
		return nil
	}
	if translated := ExternalAgentError(err); apperror.CodeOf(translated) != "" {
		return translated
	}
	return apperror.Wrap(runtimeControlCode(err), err, nil)
}

func runtimeControlCode(err error) apperror.Code {
	switch {
	case errors.Is(err, context.Canceled):
		return apperror.CodeRuntimeControlCancelled
	case errors.Is(err, approval.ErrForbidden):
		return apperror.CodeRuntimeControlForbidden
	case errors.Is(err, turn.ErrSessionBusy):
		return apperror.CodeSessionBusy
	case errors.Is(err, external.ErrControlUnsupported):
		return apperror.CodeRuntimeControlUnsupported
	case errors.Is(err, external.ErrCommandUnavailable):
		return apperror.CodeRuntimeControlCommandUnavailable
	case errors.Is(err, external.ErrModeUnavailable):
		return apperror.CodeRuntimeControlModeUnavailable
	case errors.Is(err, external.ErrThreadUnavailable):
		return apperror.CodeRuntimeControlThreadUnavailable
	case errors.Is(err, external.ErrAuthRequired):
		return apperror.CodeExternalRuntimeAuthRequired
	default:
		return apperror.CodeRuntimeControlFailed
	}
}
