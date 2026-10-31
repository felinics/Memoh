package application

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
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
		{"session restore failed", fmt.Errorf("%w: session/resume: agent did not answer in time", acpclient.ErrSessionRestoreFailed), apperror.CodeExternalRuntimeSessionResumeFailed},
		{"not a container", external.ErrContainerWorkspaceRequired, apperror.CodeExternalAgentContainerWorkspaceRequired},
		{"dependency missing", &external.DependencyMissingError{DependencyID: "codex"}, apperror.CodeAgentDependencyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExternalAgentError(fmt.Errorf("start: %w", tc.err))
			if code := apperror.CodeOf(got); code != tc.want {
				t.Fatalf("code = %q, want %q", code, tc.want)
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
		{"auth required", external.Fail(external.FailureAuthRequired, nil), true},
		{"goal outside default mode", external.Fail(external.FailureGoalRequiresDefaultMode, nil), true},
		{"model or mode not offered", external.Fail(external.FailureModeUnavailable, acpclient.ErrModelUnavailable), true},
		{"session restore failed", acpclient.ErrSessionRestoreFailed, true},
		{"dependency missing", &external.DependencyMissingError{DependencyID: "codex"}, true},
		{"unknown agent", acpagent.ErrAgentNotFound, true},
		{"disabled agent", acpagent.ErrAgentNotEnabled, true},
		{"agent not set up", acpagent.ErrAgentNotConfigured, true},
		{"runtime without owner", acpagent.ErrRuntimeOwnerMissing, true},
		{"command gone", acpagent.ErrAgentCommandUnavailable, true},
		{"image unsupported", acpclient.ErrImagePromptUnsupported, true},
		{"image invalid", acpclient.ErrInvalidPromptImage, true},
		{"not a container", external.ErrContainerWorkspaceRequired, true},

		{"start failed", external.Upstream(cause), false},
		{"credential unusable", external.CredentialError(agentcredential.ErrRevoked), false},
		{"credential busy", external.Fail(external.FailureCredentialBusy, nil), false},
		{"agent reported", external.Reported(external.FailureReported, "rate limit reached"), false},
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
		fault apperror.Fault
	}{
		{"start failed", external.Upstream(errors.New("agent process exited with code 1: bad flag")), apperror.CodeExternalRuntimeReported, map[string]string{"message": "agent process exited with code 1: bad flag"}, apperror.FaultDependency},
		{"auth required", external.Fail(external.FailureAuthRequired, nil), apperror.CodeExternalRuntimeAuthRequired, nil, apperror.FaultClient},
		{"goal outside default mode", external.Fail(external.FailureGoalRequiresDefaultMode, nil), apperror.CodeRuntimeControlGoalRequiresDefaultMode, nil, apperror.FaultClient},
		{"mode unavailable", external.Fail(external.FailureModeUnavailable, nil), apperror.CodeRuntimeControlModeUnavailable, nil, apperror.FaultClient},
		{"runtime draining", external.Fail(external.FailureRuntimeBusy, errors.New("SECRET predecessor")), apperror.CodeExternalRuntimeBusy, nil, apperror.FaultClient},
		{"fork latest only", external.Fail(external.FailureForkLatestOnly, errors.New("SECRET anchor")), apperror.CodeRuntimeControlForkLatestOnly, nil, apperror.FaultClient},
		{"credential busy", external.Fail(external.FailureCredentialBusy, nil), apperror.CodeAgentCredentialRuntimeBusy, nil, apperror.FaultClient},
		{"agent reported", external.Reported(external.FailureReported, "Rate limit reached at https://relay.example/v1?key=SECRET"), apperror.CodeExternalRuntimeReported, map[string]string{"message": "Rate limit reached at https://relay.example/v1"}, apperror.FaultDependency},
		{"agent reported sign-in", external.Reported(external.FailureAuthRequired, "Invalid API key"), apperror.CodeExternalRuntimeAuthRequired, nil, apperror.FaultClient},
		{"credential not found", external.CredentialError(agentcredential.ErrNotFound), apperror.CodeAgentCredentialNotFound, nil, apperror.FaultClient},
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, nil, apperror.FaultClient},
		{"credential revoked", external.CredentialError(agentcredential.ErrRevoked), apperror.CodeAgentCredentialRevoked, nil, apperror.FaultClient},
		{"credential encryption unavailable", external.CredentialError(agentcredential.ErrEncryptionUnavailable), apperror.CodeAgentCredentialEncryptionUnavailable, nil, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for name, translate := range map[string]func(error) error{
				"ExternalAgentError":   ExternalAgentError,
				"ExternalRuntimeError": ExternalRuntimeError,
			} {
				returned := fmt.Errorf("prompt: %w", tc.err)
				got := translate(returned)
				if _, ok := apperror.Lookup(apperror.CodeOf(got)); !ok || apperror.CodeOf(got) != tc.code {
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
				if definition, _ := apperror.Lookup(apperror.CodeOf(got)); strings.Contains(definition.Detail, "SECRET") {
					t.Fatalf("%s detail leaked the cause: %q", name, definition.Detail)
				}
			}
		})
	}
}
