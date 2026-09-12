package acp

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

type controlPool interface {
	RuntimeStatus(sessionID, agentID, projectPath string) RuntimeStatus
	SetMode(context.Context, PromptInput, string) (RuntimeStatus, error)
}

func (d *Driver) Commands(_ context.Context, input external.PromptInput) ([]external.Command, error) {
	pool, ok := d.pool.(controlPool)
	if !ok {
		return nil, nil
	}
	status := pool.RuntimeStatus(input.ThreadID, driverMetadataString(input.RuntimeMetadata, metadataAgentIDKey), driverMetadataString(input.RuntimeMetadata, "project_path"))
	commands := make([]external.Command, 0, len(status.AvailableCommands))
	for _, command := range status.AvailableCommands {
		commands = append(commands, external.Command{Name: command.Name, Description: command.Description, InputHint: command.InputHint, Kind: external.CommandTurn})
	}
	return commands, nil
}

func (*Driver) ReadCommand(context.Context, external.PromptInput) (string, error) {
	return "", external.ErrCommandUnavailable
}

func controlPrompt(input external.PromptInput) PromptInput {
	return PromptInput{
		BotID: input.BotID, SessionID: input.ThreadID,
		AgentID:               driverMetadataString(input.RuntimeMetadata, metadataAgentIDKey),
		ProjectPath:           driverMetadataString(input.RuntimeMetadata, "project_path"),
		RuntimeOwnerAccountID: input.RuntimeOwnerAccountID,
		ChannelIdentityID:     input.ChannelIdentityID, ToolHTTPURL: input.ToolHTTPURL,
	}
}

func (d *Driver) Modes(_ context.Context, input external.PromptInput) (external.ModeState, error) {
	pool, ok := d.pool.(controlPool)
	if !ok {
		return external.ModeState{AvailableModes: []external.Mode{}}, nil
	}
	status := pool.RuntimeStatus(input.ThreadID, driverMetadataString(input.RuntimeMetadata, metadataAgentIDKey), driverMetadataString(input.RuntimeMetadata, "project_path"))
	return runtimeModes(status), nil
}

func (d *Driver) SetMode(ctx context.Context, input external.PromptInput, mode string) (external.ModeState, error) {
	pool, ok := d.pool.(controlPool)
	if !ok {
		return external.ModeState{}, external.ErrControlUnsupported
	}
	status, err := pool.SetMode(ctx, controlPrompt(input), mode)
	switch {
	case errors.Is(err, client.ErrModeSelectionUnsupported):
		return external.ModeState{}, external.ErrControlUnsupported
	case errors.Is(err, client.ErrModeUnavailable), errors.Is(err, client.ErrModeIDRequired):
		return external.ModeState{}, external.ErrModeUnavailable
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
