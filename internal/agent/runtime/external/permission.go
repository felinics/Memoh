package external

import "strings"

// PermissionModeState is a runtime's permission menu with current selected.
// A mode not on the menu is unavailable.
func PermissionModeState(menu []Mode, current string) (ModeState, error) {
	for _, mode := range menu {
		if mode.ID == current {
			return ModeState{Kind: "permission", ApplyOnNextTurn: true, Supported: true, CurrentModeID: current, AvailableModes: menu}, nil
		}
	}
	return ModeState{}, Fail(FailureModeUnavailable, nil)
}

// SelectedPermissionMode is the conversation's permission mode: its own
// choice, else the Agent's, else the runtime's default.
func SelectedPermissionMode(runtimeMetadata, agentMetadata map[string]any, defaultMode string) string {
	return FirstNonEmpty(MetadataString(runtimeMetadata, "permission_mode"), MetadataString(agentMetadata, "permission_mode"), defaultMode)
}

// PlanModeState is the plan-mode switch every runtime with one offers:
// default or plan.
func PlanModeState(mode string) (ModeState, error) {
	if mode != "default" && mode != "plan" {
		return ModeState{}, Fail(FailureModeUnavailable, nil)
	}
	return ModeState{Supported: true, CurrentModeID: mode, AvailableModes: []Mode{
		{ID: "default", I18nKey: "runtime.planModes.default"},
		{ID: "plan", I18nKey: "runtime.planModes.plan"},
	}}, nil
}

// MetadataString reads a trimmed string from metadata; anything else is
// empty.
func MetadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// FirstNonEmpty returns the first value that is not blank, trimmed.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
