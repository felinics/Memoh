package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace"
)

func TestWorkspaceTargetHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err        error
		wantCode   apperror.Code
		wantStatus int
		wantFault  apperror.Fault
	}{
		"runtime offline":       {workspace.ErrRemoteRuntimeOffline, apperror.CodeWorkspaceTargetOffline, http.StatusConflict, ""},
		"runtime revoked":       {workspace.ErrRemoteRuntimeRevoked, apperror.CodeWorkspaceTargetRevoked, http.StatusConflict, ""},
		"owner mismatch":        {workspace.ErrRemoteRuntimeOwnerMismatch, apperror.CodeWorkspaceTargetOwnerMismatch, http.StatusConflict, ""},
		"client too old":        {workspace.ErrRemoteRuntimeClientUpdateNeeded, apperror.CodeWorkspaceTargetClientUpdateRequired, http.StatusConflict, ""},
		"workspace unreachable": {bridgeUnavailable(), apperror.CodeWorkspaceUnreachable, http.StatusServiceUnavailable, apperror.FaultDependency},
		"unexpected failure":    {errors.New("boom"), "", 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := workspaceTargetHTTPError(tc.err)
			var httpErr *echo.HTTPError
			if tc.wantCode == "" {
				// An unexpected failure is returned with its cause for the
				// boundary to answer as internal and record.
				if errors.As(err, &httpErr) || !errors.Is(err, tc.err) {
					t.Fatalf("error = %v, want the cause without an HTTP status", err)
				}
				return
			}
			if got := apperror.CodeOf(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q", got, tc.wantCode)
			}
			if !errors.Is(apperror.CauseOf(err), tc.err) {
				t.Fatalf("cause = %v, want %v", apperror.CauseOf(err), tc.err)
			}
			if def, _ := apperror.Lookup(tc.wantCode); def.HTTPStatus != tc.wantStatus {
				t.Fatalf("%s status = %d, want %d", tc.wantCode, def.HTTPStatus, tc.wantStatus)
			}
			if tc.wantFault != "" {
				if got := errs.FaultOf(err); got != tc.wantFault {
					t.Fatalf("fault = %q, want %q", got, tc.wantFault)
				}
			}
		})
	}
}

type fakeWorkspaceTargetService struct {
	target workspace.WorkspaceTarget
}

func (*fakeWorkspaceTargetService) Mount(context.Context, string, string) (workspace.WorkspaceTarget, error) {
	return workspace.WorkspaceTarget{}, nil
}

func (s *fakeWorkspaceTargetService) GetMount(context.Context, string, string) (workspace.WorkspaceTarget, error) {
	return s.target, nil
}

func (*fakeWorkspaceTargetService) SetPrimary(context.Context, string, string) error { return nil }

func (*fakeWorkspaceTargetService) UpdateToolApprovalConfig(context.Context, string, string, settings.ToolApprovalConfig) error {
	return nil
}

func (*fakeWorkspaceTargetService) DeleteMount(context.Context, string, string) error { return nil }

func TestModeShortcutPreservesAdvancedToolApprovalRules(t *testing.T) {
	config := settings.DefaultToolApprovalConfig()
	config.Enabled = false
	config.Write.BypassGlobs = []string{"projects/safe/**"}
	config.Exec.ForceReviewCommands = []string{"rm *"}
	handler := &BotRemoteRuntimeHandler{service: &fakeWorkspaceTargetService{target: workspace.WorkspaceTarget{
		TargetID: "44444444-4444-4444-8444-444444444444", ToolApprovalConfig: config,
	}}}

	updated, err := handler.resolveToolApprovalUpdate(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		"44444444-4444-4444-8444-444444444444",
		workspace.UpdateWorkspaceTargetToolApprovalRequest{
			Read: settings.ToolApprovalAllow, Write: settings.ToolApprovalAsk, Exec: settings.ToolApprovalDeny,
		},
	)
	if err != nil {
		t.Fatalf("resolveToolApprovalUpdate: %v", err)
	}
	if len(updated.Write.BypassGlobs) != 1 || updated.Write.BypassGlobs[0] != "projects/safe/**" {
		t.Fatalf("write bypasses were lost: %#v", updated.Write.BypassGlobs)
	}
	if len(updated.Exec.ForceReviewCommands) != 1 || updated.Exec.ForceReviewCommands[0] != "rm *" {
		t.Fatalf("exec force rules were lost: %#v", updated.Exec.ForceReviewCommands)
	}
	if updated.Exec.Mode != settings.ToolApprovalDeny {
		t.Fatalf("exec mode = %q", updated.Exec.Mode)
	}
	if updated.Enabled {
		t.Fatal("mode shortcut unexpectedly re-enabled target approval")
	}
}

func TestTargetApprovalEnabledCanBeUpdatedWithoutChangingRules(t *testing.T) {
	config := settings.DefaultToolApprovalConfig()
	config.Enabled = true
	config.Write.BypassGlobs = []string{"projects/safe/**"}
	handler := &BotRemoteRuntimeHandler{service: &fakeWorkspaceTargetService{target: workspace.WorkspaceTarget{
		TargetID: "44444444-4444-4444-8444-444444444444", ToolApprovalConfig: config,
	}}}
	disabled := false

	updated, err := handler.resolveToolApprovalUpdate(
		context.Background(),
		"11111111-1111-4111-8111-111111111111",
		"44444444-4444-4444-8444-444444444444",
		workspace.UpdateWorkspaceTargetToolApprovalRequest{Enabled: &disabled},
	)
	if err != nil {
		t.Fatalf("resolveToolApprovalUpdate: %v", err)
	}
	if updated.Enabled || len(updated.Write.BypassGlobs) != 1 || updated.Write.BypassGlobs[0] != "projects/safe/**" {
		t.Fatalf("enabled-only update changed saved policy: %#v", updated)
	}
}
