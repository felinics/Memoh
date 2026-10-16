package codex

import (
	"context"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
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
		AvailableModes: []external.Mode{{ID: "default", I18nKey: "runtime.planModes.default"}, {ID: "plan", I18nKey: "runtime.planModes.plan"}},
	}, nil
}

// The preset overrides model and effort, so carry the actual thread model
// along with the switch. Never substitute a hard-coded model or plan prompt.
//
// The effort is this turn's alone. turn/start keeps a thread's earlier level
// when none is sent, and only a preset can drop it, so a turn that selects no
// level on a thread still holding one sends the preset even without a mode
// switch. Otherwise "endpoint default" would keep running the old level.
func applyCollaborationMode(params *protocol.TurnStartParams, input external.PromptInput, settings protocol.Settings) error {
	mode := metadataString(input.RuntimeMetadata, "collaboration_mode")
	if params.Model != nil {
		settings.Model = *params.Model
	}
	if mode == "" {
		if params.Effort != nil || settings.ReasoningEffort == nil || settings.Model == "" {
			return nil
		}
		mode = "default"
	}
	if _, err := codexPlanMode(mode); err != nil {
		return err
	}
	settings.ReasoningEffort = params.Effort
	if settings.Model == "" {
		return errs.NewDependency("codex thread did not report its model for collaboration mode")
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
