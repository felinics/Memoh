package runtime

import (
	"context"
	"errors"
	"testing"

	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	"github.com/memohai/memoh/domains/runtime/workspace"
	"github.com/memohai/memoh/internal/apperror"
)

func TestWorkspaceTargetError(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		wantKind apperror.Kind
	}{
		"invalid mode":       {workspace.ErrInvalidWorkspaceToolApprovalMode, apperror.KindInvalid},
		"unusable runtime":   {workspace.ErrRemoteRuntimeNotUsable, apperror.KindNotFound},
		"missing target":     {workspace.ErrWorkspaceTargetNotFound, apperror.KindNotFound},
		"owner mismatch":     {workspace.ErrRemoteRuntimeOwnerMismatch, apperror.KindConflict},
		"client too old":     {workspace.ErrRemoteRuntimeClientUpdateNeeded, apperror.KindConflict},
		"unexpected failure": {errors.New("boom"), apperror.KindInternal},
	} {
		t.Run(name, func(t *testing.T) {
			err := workspaceTargetError(nil, "mount workspace target", tc.err)
			if kind := apperror.KindOf(err); kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", kind, tc.wantKind)
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

func (*fakeWorkspaceTargetService) UpdateToolApprovalConfig(context.Context, string, string, settingpersistence.ToolApprovalConfig) error {
	return nil
}

func (*fakeWorkspaceTargetService) DeleteMount(context.Context, string, string) error { return nil }

func TestModeShortcutPreservesAdvancedToolApprovalRules(t *testing.T) {
	config := settingpersistence.DefaultToolApprovalConfig()
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
			Read: settingpersistence.ToolApprovalAllow, Write: settingpersistence.ToolApprovalAsk, Exec: settingpersistence.ToolApprovalDeny,
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
	if updated.Exec.Mode != settingpersistence.ToolApprovalDeny {
		t.Fatalf("exec mode = %q", updated.Exec.Mode)
	}
	if updated.Enabled {
		t.Fatal("mode shortcut unexpectedly re-enabled target approval")
	}
}

func TestTargetApprovalEnabledCanBeUpdatedWithoutChangingRules(t *testing.T) {
	config := settingpersistence.DefaultToolApprovalConfig()
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
