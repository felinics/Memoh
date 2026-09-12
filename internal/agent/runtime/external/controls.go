package external

import (
	"context"
	"errors"

	"github.com/felinics/memoh/internal/agent/turn"
)

var (
	ErrAuthRequired       = errors.New("runtime authentication is required")
	ErrControlUnsupported = errors.New("runtime control is unsupported")
	ErrCommandUnavailable = errors.New("runtime command is no longer available")
	ErrModeUnavailable    = errors.New("runtime mode is unavailable")
	ErrThreadUnavailable  = errors.New("runtime thread has not started")
)

type (
	CommandKind = turn.RuntimeCommandKind
	Command     = turn.RuntimeCommand
)

const (
	CommandTurn      = turn.RuntimeCommandTurn
	CommandRead      = turn.RuntimeCommandRead
	CommandOperation = turn.RuntimeCommandOperation
)

// CommandProvider owns both the command vocabulary and read-only dispatch.
// Turn commands use Driver.Prompt; operation commands use Compactor.
type CommandProvider interface {
	Commands(context.Context, PromptInput) ([]Command, error)
	ReadCommand(context.Context, PromptInput) (string, error)
}

type (
	Mode      = turn.RuntimeMode
	ModeState = turn.RuntimeModeState
)

type ModeProvider interface {
	Modes(context.Context, PromptInput) (ModeState, error)
	SetMode(context.Context, PromptInput, string) (ModeState, error)
}

// PlanModeProvider declares planning independently of tool permission presets.
type PlanModeProvider interface {
	PlanMode(context.Context, PromptInput) (ModeState, error)
	SetPlanMode(context.Context, PromptInput, string) (ModeState, error)
}

// Compactor completes only when the runtime finishes compaction. Cancellation
// interrupts the operation. The caller owns the thread's execution slot and
// persists returned runtime metadata, without adding conversation messages.
type Compactor interface {
	Compact(context.Context, PromptInput) (map[string]any, error)
}

type (
	ControlCapabilities = turn.RuntimeControlCapabilities
	Controls            = turn.RuntimeControls
)

type Goal = turn.RuntimeGoal

type GoalProvider interface {
	Goal(context.Context, PromptInput) (*Goal, error)
	ControlGoal(context.Context, PromptInput, string) error
}

func ReadControls(ctx context.Context, driver Driver, input PromptInput) (Controls, error) {
	out := Controls{SessionID: input.ThreadID, Commands: []Command{}, Modes: ModeState{AvailableModes: []Mode{}}}
	if provider, ok := driver.(CommandProvider); ok {
		commands, err := provider.Commands(ctx, input)
		if err != nil {
			return out, err
		}
		if commands != nil {
			out.Commands = commands
		}
	}
	if provider, ok := driver.(ModeProvider); ok {
		modes, err := provider.Modes(ctx, input)
		if err != nil {
			return out, err
		}
		out.Modes = modes
		out.Capabilities.PermissionModes = modes.Supported
	}
	_, out.Capabilities.Compact = driver.(Compactor)
	if provider, ok := driver.(PlanModeProvider); ok {
		state, err := provider.PlanMode(ctx, input)
		if err != nil {
			return out, err
		}
		out.PlanMode = &state
		out.Capabilities.PlanMode = state.Supported
	}
	_, out.Capabilities.Goal = driver.(GoalProvider)
	return out, nil
}

func FindCommand(commands []Command, name string) (Command, bool) {
	for _, command := range commands {
		if command.Name == name {
			return command, true
		}
	}
	return Command{}, false
}
