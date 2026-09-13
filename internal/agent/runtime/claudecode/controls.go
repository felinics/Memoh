package claudecode

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/felinics/memoh/internal/agent/runtime/claudecode/claudecfg"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

var (
	_ external.ModeProvider     = (*Driver)(nil)
	_ external.PlanModeProvider = (*Driver)(nil)
	_ external.CommandProvider  = (*Driver)(nil)
)

func (d *Driver) Modes(ctx context.Context, input external.PromptInput) (external.ModeState, error) {
	agent, err := d.agents.Get(ctx, input.BotID, input.BotAgentID)
	if err != nil {
		return external.ModeState{}, err
	}
	return claudeModes(firstNonEmpty(metadataString(input.RuntimeMetadata, "permission_mode"), metadataString(agent.Metadata, "permission_mode"), "inherit"))
}

func (*Driver) Commands(_ context.Context, input external.PromptInput) ([]external.Command, error) {
	commands := []external.Command{
		{Name: "status", I18nKey: "runtime.claudeCode.commands.status", Kind: external.CommandRead},
		{Name: "skills", I18nKey: "runtime.claudeCode.commands.skills", Kind: external.CommandRead},
		{Name: "mcp", I18nKey: "runtime.claudeCode.commands.mcp", Kind: external.CommandRead},
		{Name: "compact", I18nKey: "runtime.claudeCode.commands.compact", Kind: external.CommandOperation},
		{Name: "goal", I18nKey: "runtime.claudeCode.commands.goal", Kind: external.CommandTurn},
	}
	for _, command := range claudeTurnCommands(recordedClaudeCommands(input.RuntimeMetadata), recordedClaudeSkillNames(input.RuntimeMetadata)) {
		if i := slices.IndexFunc(commands, func(existing external.Command) bool { return existing.Name == command.Name }); i >= 0 {
			if commands[i].Kind == external.CommandTurn {
				commands[i].Description = command.Description
				commands[i].InputHint = command.ArgumentHint
			}
			continue
		}
		commands = append(commands, external.Command{Name: command.Name, Description: command.Description, InputHint: command.ArgumentHint, Kind: external.CommandTurn})
	}
	return commands, nil
}

// system/init identifies prompt-backed skills. Intersect with initialize's
// invocable list so hidden skills stay hidden and local management commands
// cannot change the host-owned session or configuration through this path.
func claudeTurnCommands(commands []initializeCommand, skills []string) []initializeCommand {
	var out []initializeCommand
	for _, command := range commands {
		// Native scheduling requires a process that outlives an admitted turn.
		if command.Name == "loop" {
			continue
		}
		known := slices.Contains([]string{"code-review", "security-review", "simplify", "verify", "init", "recap", "reload-skills", "goal"}, command.Name)
		if !known && !slices.Contains(skills, command.Name) {
			continue
		}
		out = append(out, command)
		for _, alias := range command.Aliases {
			aliased := command
			aliased.Name = alias
			out = append(out, aliased)
		}
	}
	return out
}

func recordedClaudeSkillNames(metadata map[string]any) []string {
	raw, _ := json.Marshal(metadata["claude_skills"])
	var names []string
	_ = json.Unmarshal(raw, &names)
	return names
}

func (t *turnRunner) reloadSkillNames(ctx context.Context) ([]string, error) {
	raw, err := t.callControl(ctx, "reload_skills", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Skills []initializeCommand `json:"skills"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(response.Skills))
	for _, skill := range response.Skills {
		names = append(names, skill.Name)
	}
	return names, nil
}

func recordedClaudeCommands(metadata map[string]any) []initializeCommand {
	raw, err := json.Marshal(metadata["claude_commands"])
	if err != nil {
		return nil
	}
	var commands []initializeCommand
	_ = json.Unmarshal(raw, &commands)
	return commands
}

func (*Driver) ReadCommand(_ context.Context, input external.PromptInput) (external.CommandResult, error) {
	var value any
	switch input.Command {
	case "status":
		value = map[string]any{"session_id": input.RuntimeMetadata[metadataSessionIDKey], "model": input.RuntimeMetadata["claude_model"], "permission_mode": input.RuntimeMetadata["claude_effective_permission_mode"], "usage": input.RuntimeMetadata["claude_usage"]}
	case "skills":
		value = input.RuntimeMetadata["claude_skills"]
	case "mcp":
		value = input.RuntimeMetadata["claude_mcp_servers"]
	default:
		return external.CommandResult{}, external.ErrCommandUnavailable
	}
	if metadataString(input.RuntimeMetadata, metadataSessionIDKey) == "" {
		return external.CommandResult{}, external.ErrThreadUnavailable
	}
	return external.CommandResult{Data: value, Notice: "last_observed"}, nil
}

func (*Driver) SetMode(_ context.Context, _ external.PromptInput, mode string) (external.ModeState, error) {
	return claudeModes(mode)
}

func claudeModes(mode string) (external.ModeState, error) {
	if mode == "" || !claudecfg.ValidPermissionMode(mode) {
		return external.ModeState{}, external.ErrModeUnavailable
	}
	modes := []external.Mode{
		{ID: "inherit", I18nKey: "runtime.claudeCode.modes.inherit", Icon: "shield"},
		{ID: "default", I18nKey: "runtime.claudeCode.modes.default", Icon: "hand"},
		{ID: "acceptEdits", I18nKey: "runtime.claudeCode.modes.acceptEdits", Icon: "file-pen"},
		{ID: "auto", I18nKey: "runtime.claudeCode.modes.auto", Icon: "shield-terminal"},
		{ID: "bypassPermissions", I18nKey: "runtime.claudeCode.modes.bypassPermissions", Icon: "shield-alert", Warning: true},
	}
	return external.ModeState{Kind: "permission", ApplyOnNextTurn: true, Supported: true, CurrentModeID: mode, AvailableModes: modes}, nil
}

func (*Driver) PlanMode(_ context.Context, input external.PromptInput) (external.ModeState, error) {
	mode := metadataString(input.RuntimeMetadata, "collaboration_mode")
	if mode == "" {
		mode = "default"
		if metadataString(input.RuntimeMetadata, "claude_effective_permission_mode") == "plan" {
			mode = "plan"
		}
	}
	return claudePlanMode(mode)
}

func (*Driver) SetPlanMode(_ context.Context, _ external.PromptInput, mode string) (external.ModeState, error) {
	return claudePlanMode(mode)
}

func claudePlanMode(mode string) (external.ModeState, error) {
	if mode != "default" && mode != "plan" {
		return external.ModeState{}, external.ErrModeUnavailable
	}
	return external.ModeState{Supported: true, CurrentModeID: mode, AvailableModes: []external.Mode{{ID: "default", I18nKey: "runtime.planModes.default"}, {ID: "plan", I18nKey: "runtime.planModes.plan"}}}, nil
}
