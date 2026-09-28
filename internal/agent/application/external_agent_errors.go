package application

import (
	"errors"
	"strconv"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
)

// ExternalAgentError translates an External Agent failure the user can act on
// into its public error: an agent that is unknown, disabled or not set up, a
// runtime without its owner, a missing workspace dependency, a workspace that
// is not a container, or input the agent cannot take.
//
// An error that already carries a catalog code, and an error that is none of
// these, is returned unchanged.
func ExternalAgentError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	var missing *external.DependencyMissingError
	if errors.As(err, &missing) {
		return apperror.Wrap(apperror.CodeAgentDependencyMissing, err, map[string]string{
			"dep_id":                missing.DependencyID,
			"install_task_id":       missing.TaskID,
			"operation_in_progress": strconv.FormatBool(missing.OperationInProgress),
		})
	}
	if code := externalAgentCode(err); code != "" {
		return apperror.Wrap(code, err, nil)
	}
	return err
}

func externalAgentCode(err error) apperror.Code {
	switch {
	case errors.Is(err, acpagent.ErrAgentNotFound):
		return apperror.CodeACPAgentNotFound
	case errors.Is(err, acpagent.ErrAgentNotEnabled):
		return apperror.CodeACPAgentNotEnabled
	case errors.Is(err, acpagent.ErrAgentNotConfigured):
		return apperror.CodeACPAgentNotConfigured
	case errors.Is(err, acpagent.ErrRuntimeOwnerMissing):
		return apperror.CodeACPRuntimeOwnerMissing
	case errors.Is(err, acpagent.ErrAgentCommandUnavailable):
		// The runtime that admission matched was replaced (or updated its
		// command set) before the prompt; the turn fails closed exactly like
		// admission would have.
		return apperror.CodeRuntimeAgentCommandStale
	case errors.Is(err, acpclient.ErrImagePromptUnsupported):
		return apperror.CodeACPImageInputUnsupported
	case errors.Is(err, acpclient.ErrInvalidPromptImage):
		return apperror.CodeACPAttachmentInvalid
	case errors.Is(err, external.ErrContainerWorkspaceRequired):
		return apperror.CodeExternalAgentContainerWorkspaceRequired
	default:
		return ""
	}
}
