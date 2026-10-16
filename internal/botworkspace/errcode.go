package botworkspace

import (
	"errors"

	"github.com/felinics/memoh/internal/apperror"
)

var (
	// ErrImageNotFound marks an image the registry or runtime does not have,
	// or whose name cannot be resolved. Retrying will not change the answer.
	ErrImageNotFound = errors.New("workspace image not found")
	// ErrImageRegistryUnavailable marks an image pull that could not reach
	// the registry.
	ErrImageRegistryUnavailable = errors.New("workspace image registry unavailable")
)

// FailureCode is the catalog code a failed provisioning step is recorded
// with in last_error_code. The step's error stays with the unit's result
// record; the row keeps only what the user can act on.
func FailureCode(step *StepError) apperror.Code {
	switch {
	case errors.Is(step.Err, ErrImageNotFound):
		return apperror.CodeWorkspaceImageNotFound
	case errors.Is(step.Err, ErrImageRegistryUnavailable):
		return apperror.CodeWorkspaceImageRegistryUnavailable
	case step.Phase == PhaseBootstrap:
		return apperror.CodeWorkspaceTemplateBootstrapFailed
	}
	return apperror.CodeWorkspaceSetupFailed
}
