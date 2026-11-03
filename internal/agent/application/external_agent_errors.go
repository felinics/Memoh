package application

import (
	"errors"
	"strconv"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
)

// ExternalAgentError translates an External Agent failure the user can act on
// into its public error: a runtime failure ExternalRuntimeError translates, an
// agent that is unknown, disabled or not set up, a runtime without its owner,
// a missing workspace dependency, a workspace that is not a container, or
// input the agent cannot take.
//
// An error that already carries a catalog code, and an error that is none of
// these, is returned unchanged.
func ExternalAgentError(err error) error {
	if err == nil || apperror.CodeOf(err) != "" {
		return err
	}
	if translated := externalRuntimeError(err); translated != nil {
		return translated
	}
	var missing *external.DependencyMissingError
	if errors.As(err, &missing) {
		return apperror.Wrap(apperror.CodeAgentDependencyMissing, err, map[string]string{
			"dep_id":                missing.DependencyID,
			"install_task_id":       missing.TaskID,
			"operation_in_progress": strconv.FormatBool(missing.OperationInProgress),
		})
	}
	if errors.Is(err, acpclient.ErrSessionRestoreFailed) {
		// Its own code: the conversation offers to give the session up.
		return apperror.Wrap(apperror.CodeExternalRuntimeSessionResumeFailed, err, map[string]string{"message": external.PublicMessage(err.Error())})
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

// ExternalRuntimeError translates the failures a runtime driver reports as an
// external.Failure into their public errors. Any other error is returned
// unchanged, so a caller that gives the remaining failures its own code (a
// device login, a credential purge) applies it to exactly those errors.
func ExternalRuntimeError(err error) error {
	if translated := externalRuntimeError(err); translated != nil {
		return translated
	}
	return err
}

// externalRuntimeError returns the public error for a runtime failure, or nil
// when err is not one.
func externalRuntimeError(err error) error {
	var failure *external.Failure
	if !errors.As(err, &failure) {
		return nil
	}
	code, args := runtimeFailureCode(failure)
	if code == "" {
		return nil
	}
	return apperror.Wrap(code, err, args)
}

func runtimeFailureCode(failure *external.Failure) (apperror.Code, map[string]string) {
	switch failure.Kind {
	case external.FailureReported:
		return apperror.CodeExternalRuntimeReported, map[string]string{"message": failure.Message}
	case external.FailureAuthRequired:
		return apperror.CodeExternalRuntimeAuthRequired, nil
	case external.FailureGoalRequiresDefaultMode:
		return apperror.CodeRuntimeControlGoalRequiresDefaultMode, nil
	case external.FailureModeUnavailable:
		return apperror.CodeRuntimeControlModeUnavailable, nil
	case external.FailureRuntimeBusy:
		return apperror.CodeExternalRuntimeBusy, nil
	case external.FailureForkLatestOnly:
		return apperror.CodeRuntimeControlForkLatestOnly, nil
	case external.FailureCredentialBusy:
		return apperror.CodeAgentCredentialRuntimeBusy, nil
	case external.FailureCredential:
		return agentCredentialCode(failure.Err), nil
	default:
		return "", nil
	}
}

func agentCredentialCode(err error) apperror.Code {
	switch {
	case errors.Is(err, agentcredential.ErrNotFound):
		return apperror.CodeAgentCredentialNotFound
	case errors.Is(err, agentcredential.ErrIncompatible):
		return apperror.CodeAgentCredentialIncompatible
	case errors.Is(err, agentcredential.ErrRevoked):
		return apperror.CodeAgentCredentialRevoked
	case errors.Is(err, agentcredential.ErrEncryptionUnavailable):
		return apperror.CodeAgentCredentialEncryptionUnavailable
	default:
		return ""
	}
}

// isRuntimeConfigurationError reports a runtime failure where nothing ran:
// the turn must not persist a round, and the caller surfaces the error
// directly. It reads the driver's error before ExternalAgentError hides it.
func isRuntimeConfigurationError(err error) bool {
	var failure *external.Failure
	if errors.As(err, &failure) {
		switch failure.Kind {
		case external.FailureAuthRequired, external.FailureGoalRequiresDefaultMode, external.FailureModeUnavailable:
			return true
		default:
			return false
		}
	}
	var missing *external.DependencyMissingError
	return errors.As(err, &missing) || errors.Is(err, acpclient.ErrSessionRestoreFailed) || externalAgentCode(err) != ""
}
