package command

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// buildContextGroup registers /context — a focused token/context-window view for
// the current session (a Claude-Code-style budget card). It reuses the same
// session queries as /status; it is session-scoped, so the channel processor
// resolves the active session before dispatching (see isStatusCommand).
func (h *Handler) buildContextGroup() *CommandGroup {
	g := newCommandGroup("context", "Show context window usage")
	g.DefaultAction = "show"
	g.Register(SubCommand{
		Name:  "show",
		Usage: "show - Show context window usage for the current session",
		Handler: func(cc CommandContext) (string, error) {
			if strings.TrimSpace(cc.SessionID) == "" {
				return cc.T("cmd.session.noActive"), nil
			}
			return h.renderContextUsage(cc, cc.SessionID)
		},
	})
	return g
}

func (h *Handler) renderContextUsage(cc CommandContext, sessionID string) (string, error) {
	if h.queries == nil {
		return cc.T("cmd.session.unavailable"), nil
	}
	pgSessionID, err := parseCommandUUID(sessionID)
	if err != nil {
		return "", err
	}
	observation, err := h.contextObservation(cc, pgSessionID)
	if err != nil {
		return "", fmt.Errorf("get usage: %w", err)
	}
	used := observation.UsedTokens
	msgCount, err := h.queries.CountMessagesBySession(cc.Ctx, pgSessionID)
	if err != nil {
		return "", fmt.Errorf("count messages: %w", err)
	}
	cacheRow, _ := h.queries.GetSessionCacheStats(cc.Ctx, pgSessionID)
	var cacheHit float64
	if cacheRow.TotalInputTokens > 0 {
		cacheHit = float64(cacheRow.CacheReadTokens) / float64(cacheRow.TotalInputTokens) * 100
	}
	window := h.contextWindowFor(cc, observation)

	var b strings.Builder
	b.WriteString(MdBold(cc.T("cmd.context.title")))
	b.WriteString("\n\n")
	switch {
	case !observation.Known:
		b.WriteString(cc.T("cmd.context.unavailable"))
	case window > 0:
		frac := float64(used) / float64(window)
		fmt.Fprintf(&b, "%s  %s", renderProgressBar(frac, 12), cc.T("cmd.context.usedWithWindow", map[string]any{
			"percent": fmt.Sprintf("%.0f%%", frac*100),
			"used":    formatTokens(used),
			"window":  formatTokens(window),
		}))
	default:
		fmt.Fprintf(&b, "%s", cc.T("cmd.context.tokensUsed", map[string]any{"used": formatTokens(used)}))
	}
	fmt.Fprintf(&b, "\n\n- %s: %d", cc.T("cmd.status.fieldMessages"), msgCount)
	if cacheRow.TotalInputTokens > 0 {
		fmt.Fprintf(&b, "\n- %s: %.1f%%", cc.T("cmd.context.fieldCacheHit"), cacheHit)
	}
	return b.String(), nil
}

// renderProgressBar draws a fixed-width unicode bar (█ filled, ░ empty). The
// glyphs are plain unicode and survive both the Telegram HTML pass and the
// plain-text strip unchanged.
func renderProgressBar(frac float64, cells int) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(cells) + 0.5)
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
}

// contextObservation reads the session's newest context state.
func (h *Handler) contextObservation(cc CommandContext, sessionID pgtype.UUID) (messagepkg.ContextObservation, error) {
	row, err := h.queries.GetLatestContextUsage(cc.Ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return messagepkg.ResolveContextObservation("", "", nil, nil), nil
	}
	if err != nil {
		return messagepkg.ContextObservation{}, err
	}
	return messagepkg.ResolveContextObservation(row.SessionRuntimeType, row.MessageRuntimeType, row.Usage, row.ContextUsage), nil
}

// contextWindowFor is the window the observation is drawn against: the
// runtime's own window from the same observation, or the chat model's window
// for a native session. A runtime observation never borrows the chat model's.
func (h *Handler) contextWindowFor(cc CommandContext, observation messagepkg.ContextObservation) int64 {
	if observation.Basis == messagepkg.ContextBasisRuntime {
		return observation.WindowTokens
	}
	return h.resolveContextWindowTokens(cc)
}
