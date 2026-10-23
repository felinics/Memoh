package application

import (
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/hooks"
)

// answersOnlyThroughTools reports whether a session type delivers nothing
// without tools: a discuss bot speaks only through its messaging tools, and a
// scheduled run's plain text is logged only.
func answersOnlyThroughTools(sessionType string) bool {
	switch strings.ToLower(strings.TrimSpace(sessionType)) {
	case sessionmode.Discuss, sessionmode.Schedule:
		return true
	default:
		return false
	}
}

// requireToolsFor rejects a turn of a session that answers only through tools
// when its tools were withheld. The text-only answer a chat turn falls back to
// would reach nobody, so the turn fails as it did before chat turns could
// continue without tools.
func requireToolsFor(sessionType string, cfg native.RunConfig) error {
	if cfg.ToolsWithheld == "" || !answersOnlyThroughTools(sessionType) {
		return nil
	}
	return toolsWithheldError(strings.TrimSpace(sessionType)+" turn cannot run without tools", cfg.ToolsWithheld)
}

// toolsWithheldError names why something that needs the turn's tools cannot
// run: an unreachable workspace keeps its public code, and a hooks
// configuration that could not be loaded is ErrConfigUnavailable.
func toolsWithheldError(what string, reason native.ToolsWithheldReason) error {
	if reason == native.ToolsWithheldWorkspaceUnavailable {
		return apperror.New(apperror.CodeWorkspaceUnreachable, nil)
	}
	return fmt.Errorf("%s: %w", what, hooks.ErrConfigUnavailable)
}
