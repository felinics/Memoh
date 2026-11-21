package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/errs"
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
	mode := external.FirstNonEmpty(external.MetadataString(t.input.RuntimeMetadata, "permission_mode"), cfg.PermissionMode, "inherit")
	if !claudecfg.ValidPermissionMode(mode) {
		return errs.New(fmt.Sprintf("invalid claude permission mode %q", mode))
	}
	plan := external.MetadataString(t.input.RuntimeMetadata, "collaboration_mode")
	if plan != "" {
		if _, err := external.PlanModeState(plan); err != nil {
			return err
		}
	}
	if mode == "auto" {
		selected := external.FirstNonEmpty(t.input.ModelID, cfg.Model)
		for _, model := range models {
			if (selected == strings.TrimSpace(model.Value) || selected == strings.TrimSpace(model.ResolvedModel)) && !model.SupportsAutoMode {
				return external.Fail(external.FailureModeUnavailable, nil)
			}
		}
	}
	if mode == "inherit" {
		return t.inheritNativeMode(ctx, plan)
	}
	if plan == "plan" && mode != "plan" {
		// Let Claude remember the accepted ordinary mode when entering Plan;
		// its ExitPlanMode tool restores that mode after the user's approval.
		if err := t.ensureNativeMode(ctx, mode); err != nil {
			return err
		}
		mode = "plan"
	}
	return t.ensureNativeMode(ctx, mode)
}

// inheritNativeMode keeps the workspace's own permission mode. A process
// still in the mode it started with already runs it; one that left it in an
// earlier turn returns to it.
func (t *turnRunner) inheritNativeMode(ctx context.Context, plan string) error {
	t.s.mu.Lock()
	current := t.s.mode
	t.s.mu.Unlock()
	if current == "" && plan != "default" {
		if plan == "plan" {
			return t.setNativeMode(ctx, "plan")
		}
		return nil
	}
	settings, err := t.getSettings(ctx)
	if err != nil {
		return err
	}
	inherited := settings.Effective.Permissions.DefaultMode
	if !nativePermissionMode(inherited) {
		inherited = "default"
	}
	target := inherited
	switch {
	case plan == "plan":
		target = "plan"
	case plan == "default" && target == "plan":
		// An explicit Plan-off choice overrides a workspace that starts in
		// Plan. Other inherited modes remain the CLI's decision: effective
		// settings are raw configuration, not its accepted permission state.
		target = "default"
	}
	if current == "" {
		current = inherited
	}
	if current == target {
		return nil
	}
	return t.setNativeMode(ctx, target)
}

// ensureNativeMode puts the process in mode unless it already is.
func (t *turnRunner) ensureNativeMode(ctx context.Context, mode string) error {
	t.s.mu.Lock()
	current := t.s.mode
	t.s.mu.Unlock()
	if current == mode {
		return nil
	}
	return t.setNativeMode(ctx, mode)
}

func (t *turnRunner) setNativeMode(ctx context.Context, mode string) error {
	raw, err := t.callControl(ctx, "set_permission_mode", map[string]any{"mode": mode})
	if err != nil {
		var rejected *controlRejection
		if errors.As(err, &rejected) {
			return fmt.Errorf("%w: %w", external.Fail(external.FailureModeUnavailable, nil), err)
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
		return fmt.Errorf("%w: claude did not confirm permission mode %q", external.Fail(external.FailureModeUnavailable, nil), mode)
	}
	t.observePermissionMode(mode)
	return nil
}

func (t *turnRunner) observePermissionMode(mode string) {
	if !nativePermissionMode(mode) {
		return
	}
	t.s.mu.Lock()
	t.s.mode = mode
	t.s.mu.Unlock()
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

// applySettings moves the process to the turn's model and effort when they
// differ from what it runs with. Empty returns to the CLI's default.
func (t *turnRunner) applySettings(ctx context.Context, model, effort string) error {
	s := t.s
	s.mu.Lock()
	currentModel, currentEffort := s.model, s.effort
	s.mu.Unlock()
	if model != currentModel {
		var value any
		if model != "" {
			value = model
		}
		if _, err := t.callControl(ctx, "set_model", map[string]any{"model": value}); err != nil {
			return err
		}
	}
	if effort != currentEffort {
		var level any
		if effort != "" {
			level = effort
		}
		if _, err := t.callControl(ctx, "apply_flag_settings", map[string]any{"settings": map[string]any{"effortLevel": level}}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.model, s.effort = model, effort
	s.mu.Unlock()
	return nil
}
