package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
)

func TestExternalAgentErrorTranslatesRuntimeErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want apperror.Code
	}{
		{"unknown agent", acpagent.ErrAgentNotFound, apperror.CodeACPAgentNotFound},
		{"disabled agent", acpagent.ErrAgentNotEnabled, apperror.CodeACPAgentNotEnabled},
		{"agent not set up", acpagent.ErrAgentNotConfigured, apperror.CodeACPAgentNotConfigured},
		{"runtime without owner", acpagent.ErrRuntimeOwnerMissing, apperror.CodeACPRuntimeOwnerMissing},
		{"command gone", acpagent.ErrAgentCommandUnavailable, apperror.CodeRuntimeAgentCommandStale},
		{"image unsupported", acpclient.ErrImagePromptUnsupported, apperror.CodeACPImageInputUnsupported},
		{"image invalid", acpclient.ErrInvalidPromptImage, apperror.CodeACPAttachmentInvalid},
		{"not a container", external.ErrContainerWorkspaceRequired, apperror.CodeExternalAgentContainerWorkspaceRequired},
		{"dependency missing", &external.DependencyMissingError{DependencyID: "codex"}, apperror.CodeAgentDependencyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExternalAgentError(fmt.Errorf("start: %w", tc.err))
			if code := apperror.CodeOf(got); code != tc.want {
				t.Fatalf("code = %q, want %q", code, tc.want)
			}
			if !apperror.IsExternalAgentCode(tc.want) {
				t.Fatalf("%q is not an External Agent code", tc.want)
			}
		})
	}
}

func TestExternalAgentErrorKeepsDependencyArgs(t *testing.T) {
	t.Parallel()

	got := ExternalAgentError(&external.DependencyMissingError{DependencyID: "codex", TaskID: "task-1", OperationInProgress: true})
	args := apperror.ArgsOf(got)
	if args["dep_id"] != "codex" || args["install_task_id"] != "task-1" || args["operation_in_progress"] != "true" {
		t.Fatalf("args = %v", args)
	}
}

func TestExternalAgentErrorLeavesOtherErrors(t *testing.T) {
	t.Parallel()

	coded := apperror.New(apperror.CodeWorkspaceUnreachable, nil)
	if got := ExternalAgentError(coded); !errors.Is(got, coded) || apperror.CodeOf(got) != apperror.CodeWorkspaceUnreachable {
		t.Fatalf("coded error was re-translated: %v", got)
	}
	plain := errors.New("driver exited")
	if got := ExternalAgentError(plain); !errors.Is(got, plain) || apperror.CodeOf(got) != "" {
		t.Fatalf("plain error was translated: %v", got)
	}
	if ExternalAgentError(nil) != nil {
		t.Fatal("nil error was translated")
	}
}

// A turn whose runtime fails before it accepts the prompt ran nothing: the
// caller answers the error and no round is persisted. The check reads the error
// the driver returned, before ExternalAgentError translates it.
func TestRuntimeConfigurationFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("SECRET cause")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"runtime unavailable", external.Unavailable(cause), true},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), true},
		{"session resume failed", external.Fail(external.FailureSessionResumeFailed, cause), true},
		{"goal outside default mode", external.Fail(external.FailureGoalRequiresDefaultMode, nil), true},
		{"model selection unsupported", &acpagent.PromptError{Err: acpclient.ErrModelSelectionUnsupported}, true},
		{"model id required", &acpagent.PromptError{Err: acpclient.ErrModelIDRequired}, true},
		{"model unavailable", &acpagent.PromptError{Err: acpclient.ErrModelUnavailable}, true},
		{"reasoning unsupported", &acpagent.PromptError{Err: acpclient.ErrReasoningSelectionUnsupported}, true},
		{"reasoning effort required", &acpagent.PromptError{Err: acpclient.ErrReasoningEffortRequired}, true},
		{"reasoning effort unavailable", &acpagent.PromptError{Err: acpclient.ErrReasoningEffortUnavailable}, true},
		{"config update failed", &acpagent.PromptError{Err: acpagent.ErrRuntimeConfigUpdateFailed}, true},
		{"dependency missing", &external.DependencyMissingError{DependencyID: "codex"}, true},
		{"unknown agent", acpagent.ErrAgentNotFound, true},
		{"disabled agent", acpagent.ErrAgentNotEnabled, true},
		{"agent not set up", acpagent.ErrAgentNotConfigured, true},
		{"runtime without owner", acpagent.ErrRuntimeOwnerMissing, true},
		{"command gone", acpagent.ErrAgentCommandUnavailable, true},
		{"image unsupported", acpclient.ErrImagePromptUnsupported, true},
		{"image invalid", acpclient.ErrInvalidPromptImage, true},
		{"not a container", external.ErrContainerWorkspaceRequired, true},

		{"command not found", &acpagent.PromptError{Err: &acpclient.CommandNotFoundError{Command: "devin"}}, false},
		{"credential unusable", external.CredentialError(agentcredential.ErrRevoked), false},
		{"credential busy", external.Fail(external.FailureCredentialBusy, nil), false},
		{"control failed", external.Fail(external.FailureControlFailed, cause), false},
		{"mode unavailable", external.Fail(external.FailureModeUnavailable, external.ErrModeUnavailable), false},
		{"canceled", context.Canceled, false},
		{"response timeout", apperror.New(apperror.CodeAgentResponseTimeout, nil), false},
		{"plain error", cause, false},
	} {
		if got := isRuntimeConfigurationError(fmt.Errorf("prompt: %w", tc.err)); got != tc.want {
			t.Errorf("%s: isRuntimeConfigurationError() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Each failure a runtime driver reports becomes its public error in the
// application: the code, the args taken from the failure, and the fault. The
// driver's own error stays behind the public one for diagnostics.
func TestExternalAgentErrorTranslatesRuntimeFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		err   error
		code  apperror.Code
		args  map[string]string
		fault errs.Fault
	}{
		{"runtime unavailable", external.Unavailable(errors.New("SECRET exit")), apperror.CodeExternalRuntimeUnavailable, nil, errs.FaultServer},
		{"runtime unreachable", external.Unavailable(errs.WrapDependency(errors.New("SECRET refused"), "dial bridge")), apperror.CodeExternalRuntimeUnavailable, nil, errs.FaultDependency},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired, nil, errs.FaultClient},
		{"session resume failed", external.Fail(external.FailureSessionResumeFailed, errors.New("SECRET rpc")), apperror.CodeExternalRuntimeSessionResumeFailed, nil, errs.FaultDependency},
		{"goal outside default mode", external.Fail(external.FailureGoalRequiresDefaultMode, nil), apperror.CodeRuntimeControlGoalRequiresDefaultMode, nil, errs.FaultClient},
		{"mode unavailable", external.Fail(external.FailureModeUnavailable, external.ErrModeUnavailable), apperror.CodeRuntimeControlModeUnavailable, nil, errs.FaultClient},
		{"control failed", external.Fail(external.FailureControlFailed, errors.New("SECRET control")), apperror.CodeRuntimeControlFailed, nil, errs.FaultServer},
		{"credential busy", external.Fail(external.FailureCredentialBusy, nil), apperror.CodeAgentCredentialRuntimeBusy, nil, errs.FaultClient},
		{"credential not found", external.CredentialError(agentcredential.ErrNotFound), apperror.CodeAgentCredentialNotFound, nil, errs.FaultClient},
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, nil, errs.FaultClient},
		{"credential revoked", external.CredentialError(agentcredential.ErrRevoked), apperror.CodeAgentCredentialRevoked, nil, errs.FaultClient},
		{"credential encryption unavailable", external.CredentialError(agentcredential.ErrEncryptionUnavailable), apperror.CodeAgentCredentialEncryptionUnavailable, nil, errs.FaultServer},
		{"command not found", &acpagent.PromptError{Err: fmt.Errorf("start devin: %w", &acpclient.CommandNotFoundError{Command: "devin"})}, apperror.CodeACPCommandNotFound, map[string]string{"command": "devin"}, errs.FaultClient},
		{"model selection unsupported", &acpagent.PromptError{Err: acpclient.ErrModelSelectionUnsupported}, apperror.CodeACPModelSelectionUnsupported, nil, errs.FaultClient},
		{"model id required", &acpagent.PromptError{Err: acpclient.ErrModelIDRequired}, apperror.CodeACPModelIDRequired, nil, errs.FaultClient},
		{"model unavailable", &acpagent.PromptError{Err: acpclient.ErrModelUnavailable}, apperror.CodeACPModelUnavailable, nil, errs.FaultClient},
		{"reasoning unsupported", &acpagent.PromptError{Err: acpclient.ErrReasoningSelectionUnsupported}, apperror.CodeACPReasoningUnsupported, nil, errs.FaultClient},
		{"reasoning effort required", &acpagent.PromptError{Err: acpclient.ErrReasoningEffortRequired}, apperror.CodeACPReasoningEffortRequired, nil, errs.FaultClient},
		{"reasoning effort unavailable", &acpagent.PromptError{Err: acpclient.ErrReasoningEffortUnavailable}, apperror.CodeACPReasoningUnavailable, nil, errs.FaultClient},
		{"config update failed", &acpagent.PromptError{Err: fmt.Errorf("%w: SECRET", acpagent.ErrRuntimeConfigUpdateFailed)}, apperror.CodeACPConfigUpdateFailed, nil, errs.FaultDependency},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for name, translate := range map[string]func(error) error{
				"ExternalAgentError":   ExternalAgentError,
				"ExternalRuntimeError": ExternalRuntimeError,
			} {
				returned := fmt.Errorf("prompt: %w", tc.err)
				got := translate(returned)
				public, ok := apperror.PublicFrom(got, "")
				if !ok || public.Code != tc.code {
					t.Fatalf("%s code = %q, want %q", name, apperror.CodeOf(got), tc.code)
				}
				if args := apperror.ArgsOf(got); !maps.Equal(args, tc.args) {
					t.Fatalf("%s args = %v, want %v", name, args, tc.args)
				}
				if fault := errs.FaultOf(got); fault != tc.fault {
					t.Fatalf("%s fault = %q, want %q", name, fault, tc.fault)
				}
				if !errors.Is(apperror.CauseOf(got), returned) {
					t.Fatalf("%s cause = %v, want the driver's error", name, apperror.CauseOf(got))
				}
				if strings.Contains(public.Detail, "SECRET") {
					t.Fatalf("%s detail leaked the cause: %q", name, public.Detail)
				}
			}
		})
	}
}

// ExternalRuntimeError leaves every other error as it is, so a caller that
// gives the rest its own code applies it to exactly those errors.
func TestExternalRuntimeErrorLeavesOtherErrors(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		&external.DependencyMissingError{DependencyID: "codex"},
		external.ErrContainerWorkspaceRequired,
		acpagent.ErrAgentNotFound,
		apperror.New(apperror.CodeWorkspaceUnreachable, nil),
		errors.New("driver exited"),
	} {
		if got := ExternalRuntimeError(err); got != err { //nolint:errorlint // the unchanged error itself is expected
			t.Errorf("ExternalRuntimeError(%v) = %v, want it unchanged", err, got)
		}
	}
	if ExternalRuntimeError(nil) != nil {
		t.Fatal("nil error was translated")
	}
}
