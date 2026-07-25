package engine

import (
	"log/slog"

	"github.com/memohai/memoh/domains/agent/extension/hooks"
	"github.com/memohai/memoh/domains/agent/tool"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

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
	BridgeProvider bridge.Provider
	HookService    *hooks.Service
	Logger         *slog.Logger
	Limits         Limits
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

func (l Limits) ToolOutputLimit() tool.ToolOutputLimit {
	l = l.Normalize()
	return tool.ToolOutputLimit{
		MaxBytes: l.ToolOutputMaxBytes,
		MaxLines: l.ToolOutputMaxLines,
	}
}
