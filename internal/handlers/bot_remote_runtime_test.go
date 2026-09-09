package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/settings"
	"github.com/felinics/memoh/internal/workspace"
)

func TestWorkspaceTargetHTTPError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"invalid mode":       {workspace.ErrInvalidWorkspaceToolApprovalMode, http.StatusBadRequest},
		"unusable runtime":   {workspace.ErrRemoteRuntimeNotUsable, http.StatusNotFound},
		"missing target":     {workspace.ErrWorkspaceTargetNotFound, http.StatusNotFound},
		"owner mismatch":     {workspace.ErrRemoteRuntimeOwnerMismatch, http.StatusConflict},
		"client too old":     {workspace.ErrRemoteRuntimeClientUpdateNeeded, http.StatusConflict},
		"unexpected failure": {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			err := workspaceTargetHTTPError(nil, tc.err)
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.code {
				t.Fatalf("error = %v, want HTTP %d", err, tc.code)
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
