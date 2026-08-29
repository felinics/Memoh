package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

func TestCreateBotHTTPErrorMapsNameConflictToStableCode(t *testing.T) {
	err := createBotHTTPError(bots.ErrBotNameTaken, true)
	if got := apperror.CodeOf(err); got != apperror.CodeBotNameTaken {
		t.Fatalf("code = %q, want %q", got, apperror.CodeBotNameTaken)
	}
	if got := apperror.ArgsOf(err)["field"]; got != "name" {
		t.Fatalf("field arg = %q, want name", got)
	}
}

func TestCreateBotHTTPErrorMapsWorkspaceContractFailureToStableCode(t *testing.T) {
	cause := errors.Join(
		workspace.ErrWorkspaceImageIncompatible,
		errors.New("missing /opt/memoh/toolkit/bin/node"),
	)
	err := createBotHTTPError(cause, true)
	if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceImageIncompatible {
		t.Fatalf("code = %q, want %q", got, apperror.CodeWorkspaceImageIncompatible)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, workspace.ErrWorkspaceImageIncompatible) {
		t.Fatalf("cause = %v, want workspace image incompatibility", got)
	}
}

func TestUpdateBotHTTPErrorMapsNameConflictToStableCode(t *testing.T) {
	err := updateBotHTTPError(bots.ErrBotNameTaken)
	if got := apperror.CodeOf(err); got != apperror.CodeBotNameTaken {
		t.Fatalf("code = %q, want %q", got, apperror.CodeBotNameTaken)
	}
}

func TestFSHTTPErrorKeepsUnavailableCausePrivate(t *testing.T) {
	cause := errors.Join(bridge.ErrUnavailable, errors.New("connection refused"))
	err := fsHTTPError(cause)
	if got := apperror.CodeOf(err); got != apperror.CodeWorkspaceUnreachable {
		t.Fatalf("code = %q, want %q", got, apperror.CodeWorkspaceUnreachable)
	}
	if got := apperror.CauseOf(err); !errors.Is(got, bridge.ErrUnavailable) {
		t.Fatalf("cause = %v, want bridge unavailable", got)
	}
}

func TestDisplayPrepareAppErrorUsesSharedWorkspaceCode(t *testing.T) {
	event := newDisplayPrepareAppError(
		"checking",
		apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("private cause"), nil),
		"req-1",
	)
	if event.Code != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("code = %q", event.Code)
	}
	if event.I18nKey != "" {
		t.Fatalf("new AppError event exposed i18n_key = %q", event.I18nKey)
	}
	if event.Message != "The workspace could not be reached." {
		t.Fatalf("message = %q", event.Message)
	}
	if event.Detail != event.Message {
		t.Fatalf("detail = %q, message = %q", event.Detail, event.Message)
	}
	if event.RequestID != "req-1" {
		t.Fatalf("request_id = %q", event.RequestID)
	}
}

func TestDisplayPrepareStreamBreakUsesPrepareFailedCode(t *testing.T) {
	event := newDisplayPrepareAppError(
		"installing",
		apperror.Wrap(apperror.CodeWorkspaceDisplayPrepareFailed, errors.New("rpc error: stream reset"), nil),
		"req-2",
	)
	if event.Code != string(apperror.CodeWorkspaceDisplayPrepareFailed) {
		t.Fatalf("code = %q", event.Code)
	}
	if event.Message != "Display preparation failed." {
		t.Fatalf("message = %q", event.Message)
	}
	if strings.Contains(event.Message, "stream reset") || strings.Contains(event.Detail, "stream reset") {
		t.Fatal("private cause leaked into the stream event")
	}
}

func TestWorkspaceSetupAppErrorKeepsContractDiagnosticPrivate(t *testing.T) {
	cause := errors.Join(
		workspace.ErrWorkspaceImageIncompatible,
		errors.New("missing /opt/memoh/toolkit/bin/node"),
	)
	event, ok := newWorkspaceSetupAppError(cause, "req-contract")
	if !ok {
		t.Fatal("newWorkspaceSetupAppError() did not recognize contract error")
	}
	if event.Code != string(apperror.CodeWorkspaceImageIncompatible) {
		t.Fatalf("code = %q", event.Code)
	}
	if event.I18nKey != "" {
		t.Fatalf("i18n_key = %q, want empty", event.I18nKey)
	}
	if event.Detail != "The workspace image is incompatible with this version of Memoh." {
		t.Fatalf("detail = %q", event.Detail)
	}
	if event.Message != event.Detail {
		t.Fatalf("message = %q, detail = %q", event.Message, event.Detail)
	}
	if strings.Contains(event.Message, "/opt/memoh") || strings.Contains(event.Detail, "/opt/memoh") {
		t.Fatal("private workspace path leaked into SSE event")
	}
	if event.RequestID != "req-contract" {
		t.Fatalf("request_id = %q", event.RequestID)
	}
}
