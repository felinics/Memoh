package native

import (
	"context"
	"log/slog"

	agenttools "github.com/memohai/memoh/internal/agent/tool"
	"github.com/memohai/memoh/internal/hooks"
	"github.com/memohai/memoh/internal/workspace/bridge"
)

// ContextViewApplier rebuilds provider-facing fields from the authoritative
// context fragments immediately before generate options are assembled.
type ContextViewApplier func(context.Context, RunConfig) (RunConfig, error)

const (
	DefaultToolOutputMaxBytes  = 64 * 1024
	DefaultToolOutputMaxLines  = 2000
	DefaultSystemFilesMaxBytes = 32 * 1024
)

type Limits struct {
	ToolOutputMaxBytes  int
	ToolOutputMaxLines  int
	SystemFilesMaxBytes int
}

// Deps holds all service dependencies for the Agent.
type Deps struct {
	BridgeProvider     bridge.Provider
	HookService        *hooks.Service
	Logger             *slog.Logger
	Limits             Limits
	ContextViewApplier ContextViewApplier
}

func DefaultLimits() Limits {
	return Limits{
		ToolOutputMaxBytes:  DefaultToolOutputMaxBytes,
		ToolOutputMaxLines:  DefaultToolOutputMaxLines,
		SystemFilesMaxBytes: DefaultSystemFilesMaxBytes,
	}
}

func LimitsFromValues(toolOutputMaxBytes, toolOutputMaxLines, systemFilesMaxBytes int) Limits {
	return Limits{
		ToolOutputMaxBytes:  toolOutputMaxBytes,
		ToolOutputMaxLines:  toolOutputMaxLines,
		SystemFilesMaxBytes: systemFilesMaxBytes,
	}.Normalize()
}

func (l Limits) Normalize() Limits {
	defaults := DefaultLimits()
	if l.ToolOutputMaxBytes <= 0 {
		l.ToolOutputMaxBytes = defaults.ToolOutputMaxBytes
	}
	if l.ToolOutputMaxLines <= 0 {
		l.ToolOutputMaxLines = defaults.ToolOutputMaxLines
	}
	if l.SystemFilesMaxBytes <= 0 {
		l.SystemFilesMaxBytes = defaults.SystemFilesMaxBytes
	}
	return l
}

func (l Limits) ToolOutputLimit() agenttools.ToolOutputLimit {
	l = l.Normalize()
	return agenttools.ToolOutputLimit{
		MaxBytes: l.ToolOutputMaxBytes,
		MaxLines: l.ToolOutputMaxLines,
	}
}
