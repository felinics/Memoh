package command

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (h *Handler) buildStatusGroup() *CommandGroup {
	g := newCommandGroup("status", "View current session status")
	g.DefaultAction = "show"
	g.Register(SubCommand{
		Name:  "show",
		Usage: "show - Show current session status",
		Handler: func(cc CommandContext) (string, error) {
			if strings.TrimSpace(cc.SessionID) == "" {
				return cc.T("cmd.session.noActive"), nil
			}
			return h.renderSessionStatus(cc, cc.SessionID, cc.T("cmd.status.scopeCurrent"))
		},
	})
	g.Register(SubCommand{
		Name:  "latest",
		Usage: "latest - Show the latest session status for this bot",
		Handler: func(cc CommandContext) (string, error) {
			if h.queries == nil {
				return cc.T("cmd.session.unavailable"), nil
			}
			sessionID, err := h.queries.GetLatestSessionIDByBot(cc.Ctx, cc.BotID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return cc.T("cmd.session.noneForBot"), nil
				}
				return "", err
			}
			return h.renderSessionStatus(cc, sessionID, cc.T("cmd.status.scopeLatest"))
		},
	})
	return g
}

func (h *Handler) renderSessionStatus(cc CommandContext, sessionID string, scope string) (string, error) {
	if h.queries == nil {
		return cc.T("cmd.session.unavailable"), nil
	}
	msgCount, err := h.queries.CountMessagesBySession(cc.Ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("count messages: %w", err)
	}

	var usedTokens int64
	latestUsage, err := h.queries.GetLatestAssistantUsage(cc.Ctx, sessionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("get usage: %w", err)
	}
	if err == nil {
		usedTokens = latestUsage
	}

	cacheRow, err := h.queries.GetSessionCacheStats(cc.Ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("get cache: %w", err)
	}

	var cacheHitRate float64
	if cacheRow.TotalInputTokens > 0 {
		cacheHitRate = float64(cacheRow.CacheReadTokens) / float64(cacheRow.TotalInputTokens) * 100
	}

	skills, _ := h.queries.GetSessionUsedSkills(cc.Ctx, sessionID)

	contextUsage := formatTokens(usedTokens)
	if contextWindow := h.resolveContextWindow(cc); contextWindow != "" {
		contextUsage = contextUsage + " / " + contextWindow
	}

	pairs := make([]kv, 0, 6)
	// Lead with the model — the single most load-bearing "where am I" fact.
	if s, err := h.getBotSettings(cc); err == nil {
		if m := h.resolveModelName(cc, s.ChatModelID); m != "" && m != cc.T("cmd.common.none") {
			pairs = append(pairs, kv{cc.T("cmd.status.fieldModel"), m})
		}
	}
	pairs = append(pairs,
		kv{cc.T("cmd.status.fieldMessages"), strconv.FormatInt(msgCount, 10)},
		kv{cc.T("cmd.status.fieldContext"), contextUsage},
	)
	// On a brand-new session, cache stats are forced to 0 and read as a
	// measured-bad result rather than "no data" — show them only once there is
	// input to measure.
	if cacheRow.TotalInputTokens > 0 {
		pairs = append(pairs,
			kv{cc.T("cmd.status.fieldCacheHitRate"), fmt.Sprintf("%.1f%%", cacheHitRate)},
			kv{cc.T("cmd.status.fieldCacheRead"), formatTokens(cacheRow.CacheReadTokens)},
		)
	}
	if len(skills) > 0 {
		pairs = append(pairs, kv{cc.T("cmd.status.fieldSkills"), strings.Join(skills, ", ")})
	}
	title := cc.T("cmd.status.title")
	if s := strings.TrimSpace(scope); s != "" {
		title += " — " + s
	}
	return formatKVTitled(title, pairs), nil
}

func (h *Handler) resolveContextWindow(cc CommandContext) string {
	w := h.resolveContextWindowTokens(cc)
	if w == 0 {
		return ""
	}
	return formatTokens(w)
}

// resolveContextWindowTokens returns the chat model's context window in tokens
// (0 if unknown), the raw value behind resolveContextWindow — used by /context
// to compute a percentage and bar.
func (h *Handler) resolveContextWindowTokens(cc CommandContext) int64 {
	if h.settingsService == nil || h.modelsService == nil {
		return 0
	}
	s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
	if err != nil || s.ChatModelID == "" {
		return 0
	}
	m, err := h.modelsService.GetByID(cc.Ctx, s.ChatModelID)
	if err != nil || m.Config.ContextWindow == nil {
		return 0
	}
	return int64(*m.Config.ContextWindow)
}
