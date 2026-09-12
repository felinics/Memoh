package codex

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var _ external.PlanModeProvider = (*Driver)(nil)

func (*Driver) PlanMode(_ context.Context, input external.PromptInput) (external.ModeState, error) {
	return codexPlanMode(firstNonEmpty(metadataString(input.RuntimeMetadata, "collaboration_mode"), "default"))
}

func (*Driver) SetPlanMode(_ context.Context, _ external.PromptInput, mode string) (external.ModeState, error) {
	return codexPlanMode(mode)
}

func codexPlanMode(mode string) (external.ModeState, error) {
	if mode != "plan" && mode != "default" {
		return external.ModeState{}, external.ErrModeUnavailable
	}
	return external.ModeState{
		Supported: true, CurrentModeID: mode,
		AvailableModes: []external.Mode{{ID: "default", Name: "Default"}, {ID: "plan", Name: "Plan"}},
	}, nil
}

// The preset overrides model and effort, so carry the actual thread settings
// along with the switch. Never substitute a hard-coded model or plan prompt.
func applyCollaborationMode(params *protocol.TurnStartParams, input external.PromptInput, settings protocol.Settings) error {
	mode := metadataString(input.RuntimeMetadata, "collaboration_mode")
	if mode == "" {
		return nil
	}
	if _, err := codexPlanMode(mode); err != nil {
		return err
	}
	if params.Model != nil {
		settings.Model = *params.Model
	}
	if params.Effort != nil {
		settings.ReasoningEffort = params.Effort
	}
	if settings.Model == "" {
		return errors.New("codex thread did not report its model for collaboration mode")
	}
	// nil selects Codex's built-in mode instructions.
	settings.DeveloperInstructions = nil
	params.CollaborationMode = &protocol.CollaborationMode{Mode: protocol.ModeKind(mode), Settings: settings}
	return nil
}

func (s *appServer) rememberThreadSettings(threadID, model string, effort *protocol.ReasoningEffort) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadSettings == nil {
		s.threadSettings = make(map[string]protocol.Settings)
	}
	s.threadSettings[threadID] = protocol.Settings{Model: model, ReasoningEffort: effort}
}

func (s *appServer) settingsForThread(threadID string) protocol.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadSettings[threadID]
}
