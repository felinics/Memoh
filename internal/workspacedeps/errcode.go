package workspacedeps

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// CodeOf maps a failure of this package to the catalog code a user sees, or
// "" when err is not one this package reports. The failed installation record
// stores the code and the handler that answers the request derives it from the
// same function, so a failure reads the same in the API and in the row.
func CodeOf(err error) apperror.Code {
	switch {
	case errors.Is(err, ErrDependencyNotFound):
		return apperror.CodeWorkspaceDependencyNotFound
	case errors.Is(err, ErrPlatformUnsupported):
		return apperror.CodeWorkspaceDependencyPlatformUnsupported
	case errors.Is(err, ErrBusy):
		return apperror.CodeWorkspaceDependencyBusy
	case errors.Is(err, ErrWorkspaceNotRunning):
		return apperror.CodeWorkspaceDependencyWorkspaceNotRunning
	case errors.Is(err, ErrWorkspaceMissing):
		return apperror.CodeWorkspaceDependencyWorkspaceMissing
	case errors.Is(err, ErrRollbackUnavailable):
		return apperror.CodeWorkspaceDependencyRollbackUnavailable
	case errors.Is(err, ErrActionUnsupported):
		return apperror.CodeWorkspaceDependencyActionUnsupported
	case errors.Is(err, ErrCatalogUnavailable):
		return apperror.CodeWorkspaceDependencyCatalogUnavailable
	case errors.Is(err, ErrDefinitionInvalid):
		return apperror.CodeWorkspaceDependencyDefinitionInvalid
	case errors.Is(err, ErrDefinitionUnavailable):
		return apperror.CodeWorkspaceDependencyDefinitionUnavailable
	case errors.Is(err, ErrOperationUncertain):
		return apperror.CodeWorkspaceDependencyOperationUnknown
	case errors.Is(err, ErrInvalidVersion):
		return apperror.CodeWorkspaceDependencyRequestInvalid
	case errors.Is(err, ErrPrerequisitesChanged):
		return apperror.CodeWorkspaceDependencyPrerequisitesChanged
	case errors.Is(err, ErrRequired):
		return apperror.CodeWorkspaceDependencyRequired
	case errors.Is(err, bridge.ErrUnavailable):
		return apperror.CodeWorkspaceUnreachable
	}
	return ""
}

// failureCode is the code written to a failed record. An operation the caller
// or the Server cut short is recorded as interrupted rather than as canceled:
// the row outlives the request that started it, and what the user acts on is
// that the operation ended without an outcome, not that some request ended.
func failureCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return string(apperror.CodeWorkspaceDependencyOperationInterrupted)
	}
	if code := CodeOf(err); code != "" {
		return string(code)
	}
	return string(apperror.CodeWorkspaceDependencyOperationFailed)
}
