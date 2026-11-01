package acp

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
)

// Commands reports what the conversation's session offers, opening it when
// it has none live: a new conversation, or one whose process closed, still
// takes the commands its Agent offers.
func (d *Driver) Commands(ctx context.Context, input external.PromptInput) ([]external.Command, error) {
	available, err := d.pool.Commands(ctx, controlPrompt(input))
	if err != nil {
		return nil, err
	}
	commands := make([]external.Command, 0, len(available))
	for _, command := range available {
		commands = append(commands, external.Command{Name: command.Name, Description: command.Description, InputHint: command.InputHint, Kind: external.CommandTurn})
	}
	return commands, nil
}

func (*Driver) ReadCommand(context.Context, external.PromptInput) (external.CommandResult, error) {
	return external.CommandResult{}, external.ErrCommandUnavailable
}

func controlPrompt(input external.PromptInput) PromptInput {
	return PromptInput{
		BotID: input.BotID, BotAgentID: input.BotAgentID, SessionID: input.ThreadID,
		ProjectPath:           external.MetadataString(input.RuntimeMetadata, "project_path"),
		RuntimeOwnerAccountID: input.RuntimeOwnerAccountID,
		ChannelIdentityID:     input.ChannelIdentityID, ToolHTTPURL: input.ToolHTTPURL,
	}
}

func (d *Driver) Modes(_ context.Context, input external.PromptInput) (external.ModeState, error) {
	return runtimeModes(d.pool.RuntimeStatus(controlPrompt(input))), nil
}

func (d *Driver) SetMode(ctx context.Context, input external.PromptInput, mode string) (external.ModeState, error) {
	status, err := d.pool.SetMode(ctx, controlPrompt(input), mode)
	switch {
	case errors.Is(err, client.ErrModeSelectionUnsupported):
		return external.ModeState{}, external.ErrControlUnsupported
	case errors.Is(err, client.ErrModeUnavailable), errors.Is(err, client.ErrModeIDRequired):
		return external.ModeState{}, external.Fail(external.FailureModeUnavailable, nil)
	case err != nil:
		return external.ModeState{}, err
	}
	return runtimeModes(status), nil
}

func runtimeModes(status RuntimeStatus) external.ModeState {
	out := external.ModeState{AvailableModes: []external.Mode{}}
	if status.Modes == nil {
		return out
	}
	out.Supported = status.Modes.Supported
	out.CurrentModeID = status.Modes.CurrentModeID
	for _, mode := range status.Modes.Available {
		out.AvailableModes = append(out.AvailableModes, external.Mode{ID: mode.ID, Name: mode.Name, Description: mode.Description})
	}
	return out
}
