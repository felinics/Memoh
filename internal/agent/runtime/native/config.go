package native

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

// ContextViewApplier rebuilds provider-facing fields from the authoritative
// context fragments immediately before generate options are assembled.
type ContextViewApplier func(context.Context, RunConfig) (RunConfig, error)

var ErrContextRecompose = errors.New("context requires recomposition after compaction")

const (
	DefaultToolOutputMaxBytes  = 32 * 1024
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
	// OnDemandTools selects whether rarely used tool groups load on demand
	// (OnDemandToolsAuto, the default) or ride every request (OnDemandToolsOff).
	OnDemandTools string
}

const (
	// OnDemandToolsAuto loads rarely used tool groups through tool_describe /
	// tool_call when that makes the request smaller.
	OnDemandToolsAuto = "auto"
	// OnDemandToolsOff sends every tool definition with every request.
	OnDemandToolsOff = "off"
)

// NormalizeOnDemandTools maps a configured value to OnDemandToolsAuto or
// OnDemandToolsOff; anything but an explicit off keeps the default.
func NormalizeOnDemandTools(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case OnDemandToolsOff, "false", "disabled":
		return OnDemandToolsOff
	default:
		return OnDemandToolsAuto
	}
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
