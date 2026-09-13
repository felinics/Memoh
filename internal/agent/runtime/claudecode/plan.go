package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func nativePermissionMode(mode string) bool {
	switch mode {
	case "default", "acceptEdits", "bypassPermissions", "plan", "dontAsk", "auto":
		return true
	default:
		return false
	}
}

func unavailablePermissionModes(model initializeModel) []string {
	if !model.SupportsAutoMode {
		return []string{"auto"}
	}
	return nil
}

func (t *turnRunner) configureModes(ctx context.Context, cfg claudecfg.Config, models []initializeModel) error {
	mode := firstNonEmpty(metadataString(t.input.RuntimeMetadata, "permission_mode"), cfg.PermissionMode, "inherit")
	if !claudecfg.ValidPermissionMode(mode) {
		return fmt.Errorf("invalid claude permission mode %q", mode)
	}
	plan := metadataString(t.input.RuntimeMetadata, "collaboration_mode")
	if plan != "" {
		if _, err := claudePlanMode(plan); err != nil {
			return err
		}
	}
	if mode == "auto" {
		selected := firstNonEmpty(t.input.ModelID, cfg.Model)
		for _, model := range models {
			if (selected == strings.TrimSpace(model.Value) || selected == strings.TrimSpace(model.ResolvedModel)) && !model.SupportsAutoMode {
				return external.ErrModeUnavailable
			}
		}
	}
	if mode != "inherit" {
		// Let Claude remember the accepted ordinary mode when entering Plan;
		// its ExitPlanMode tool restores that mode after the user's approval.
		if err := t.setNativeMode(ctx, mode); err != nil {
			return err
		}
	} else if plan == "default" {
		settings, err := t.getSettings(ctx)
		if err != nil {
			return err
		}
		// An explicit Plan-off choice overrides a workspace that starts in
		// Plan. Other inherited modes remain the CLI's decision: effective
		// settings are raw configuration, not its accepted permission state.
		if settings.Effective.Permissions.DefaultMode == "plan" {
			return t.setNativeMode(ctx, "default")
		}
	}
	if plan == "plan" {
		return t.setNativeMode(ctx, "plan")
	}
	return nil
}

func (t *turnRunner) setNativeMode(ctx context.Context, mode string) error {
	raw, err := t.callControl(ctx, "set_permission_mode", map[string]any{"mode": mode})
	if err != nil {
		var rejected *controlRejection
		if errors.As(err, &rejected) {
			return fmt.Errorf("%w: %w", external.ErrModeUnavailable, err)
		}
		return err
	}
	var result struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.Mode != mode {
		return fmt.Errorf("%w: claude did not confirm permission mode %q", external.ErrModeUnavailable, mode)
	}
	t.observePermissionMode(mode)
	return nil
}

func (t *turnRunner) observePermissionMode(mode string) {
	if !nativePermissionMode(mode) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.permissionMode == mode {
		return
	}
	// The initial mode only reports the CLI/workspace state; it must not
	// manufacture a Plan override. A later transition into or out of Plan
	// belongs to the active workflow and must survive the next process.
	if t.permissionMode != "" && (t.permissionMode == "plan") != (mode == "plan") {
		t.runtimeMetadata["collaboration_mode"] = "default"
		if mode == "plan" {
			t.runtimeMetadata["collaboration_mode"] = "plan"
		}
	}
	// The application persists these observations with the turn result,
	// including interrupted turns. The protocol reader never waits on storage.
	t.permissionMode = mode
	t.runtimeMetadata["claude_effective_permission_mode"] = mode
}
