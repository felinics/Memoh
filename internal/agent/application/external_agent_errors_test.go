package application

import (
	"errors"
	"fmt"
	"testing"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
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
			if !isRuntimeConfigurationError(tc.err) {
				t.Fatalf("%v is not a configuration failure", tc.err)
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
	if isRuntimeConfigurationError(plain) {
		t.Fatal("a plain error is a configuration failure")
	}
}

// A turn that fails with an External Agent code before the runtime accepted it
// ran nothing: the caller answers the error and no round is persisted.
func TestExternalAgentCodesAreConfigurationFailures(t *testing.T) {
	t.Parallel()

	for _, code := range []apperror.Code{
		apperror.CodeACPAgentNotFound, apperror.CodeACPAgentNotEnabled, apperror.CodeACPAgentNotConfigured,
		apperror.CodeCodexOAuthIncomplete, apperror.CodeCodexAuthTokenMissing, apperror.CodeACPAgentAuthInvalid,
		apperror.CodeNoWorkspaceExec, apperror.CodeACPRuntimeOwnerMissing, apperror.CodeACPDiscussUnsupported,
		apperror.CodeGroupChatACPUnsupported, apperror.CodeACPProjectModeInvalid, apperror.CodeACPProjectPathInvalid,
		apperror.CodeACPDisplayArgsInvalid, apperror.CodeACPRuntimeStartFailed, apperror.CodeACPRuntimeBusy,
		apperror.CodeACPAttachmentInvalid, apperror.CodeACPAttachmentUnavailable, apperror.CodeRuntimeAgentCommandStale,
		apperror.CodeACPImageInputUnsupported, apperror.CodeInvalidChatRuntime, apperror.CodeAgentDependencyMissing,
		apperror.CodeExternalAgentAccountUnbound, apperror.CodeExternalAgentContainerWorkspaceRequired,
	} {
		if !isRuntimeConfigurationError(fmt.Errorf("prompt: %w", apperror.New(code, nil))) {
			t.Errorf("%s is not a configuration failure", code)
		}
	}
	if isRuntimeConfigurationError(apperror.New(apperror.CodeAgentResponseTimeout, nil)) {
		t.Error("a response timeout is a configuration failure")
	}
}
