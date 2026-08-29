package command

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

// errCompactNoModel is a sentinel returned by buildCompactConfig when neither
// a compaction model nor a chat model is configured. The Handler catches it
// via errors.Is and surfaces a localized user message; other (internal) errors
// flow through friendlyCommandError's looksLikeInternalError path.
var (
	errCompactNoModel = errors.New("compact: no compaction or chat model configured")
	// errCompactModelUnavailable covers every other resolution failure (model
	// or provider disabled, no output cap, unknown window).
	errCompactModelUnavailable = errors.New("compact: compaction model unavailable")
)

func (h *Handler) buildCompactGroup() *CommandGroup {
	g := newCommandGroup("compact", "Compact conversation context")
	g.DefaultAction = "run"
	g.Register(SubCommand{
		Name:    "run",
		Usage:   "run - Compact the current session's context immediately",
		IsWrite: true,
		Handler: func(cc CommandContext) (string, error) {
			if h.compactionService == nil {
				return cc.T("cmd.compact.unavailable"), nil
			}
			sessionID := cc.SessionID
			if sessionID == "" {
				botUUID, err := db.ParseUUID(cc.BotID)
				if err != nil {
					// cc.BotID is framework-set so this only fires if the
					// framework injects a malformed UUID — a deep internal
					// bug. Log the diagnostic and surface a generic friendly
					// message rather than leaking "invalid UUID length: 5"
					// to the user verbatim.
					if h.logger != nil {
						h.logger.Warn("compact: parse bot id failed", slog.String("bot_id", cc.BotID), slog.Any("error", err))
					}
					return cc.T("cmd.error.generic", map[string]any{"command": CmdRef("compact")}), nil
				}
				latestUUID, err := h.queries.GetLatestSessionIDByBot(cc.Ctx, botUUID)
				if err != nil {
					return cc.T("cmd.session.noActive"), nil
				}
				sessionID = uuid.UUID(latestUUID.Bytes).String()
			}

			cfg, err := h.buildCompactConfig(cc, sessionID)
			if err != nil {
				if errors.Is(err, errCompactNoModel) {
					return cc.T("cmd.compact.noModel"), nil
				}
				if errors.Is(err, errCompactModelUnavailable) {
					return cc.T("cmd.compact.modelUnavailable"), nil
				}
				return "", err
			}

			res, err := h.compactionService.RunCompactionSync(cc.Ctx, cfg)
			if err != nil {
				return h.compactRunError(cc, err), nil
			}
			if res.Status != compaction.StatusOK {
				return cc.T("cmd.compact.noop"), nil
			}
			return cc.T("cmd.compact.done"), nil
		},
	})
	return g
}

// compactRunError maps a summarizer run failure to a localized chat message:
// a too-small summarizer window is actionable by the user, every other cause
// stays in the server log — run errors carry window/budget/provider
// diagnostics that must not reach chat verbatim.
func (h *Handler) compactRunError(cc CommandContext, err error) string {
	if errors.Is(err, compaction.ErrSummaryWindowTooSmall) {
		return cc.T("cmd.compact.windowTooSmall")
	}
	if h.logger != nil {
		h.logger.Error("compact: run failed", slog.String("bot_id", cc.BotID), slog.Any("error", err))
	}
	return cc.T("cmd.error.generic", map[string]any{"command": CmdRef("compact")})
}

func (h *Handler) buildCompactConfig(cc CommandContext, sessionID string) (compaction.TriggerConfig, error) {
	botSettings, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to load settings: %w", err)
	}
	sessionModelID := ""
	if strings.TrimSpace(botSettings.CompactionModelID) == "" {
		sessionModelID = models.LatestSessionModelID(cc.Ctx, h.sqlcQueries, sessionID)
	}
	resolution, err := models.ResolveCompactionModel(
		cc.Ctx,
		h.modelsService,
		h.sqlcQueries,
		botSettings.CompactionModelID,
		sessionModelID,
		botSettings.ChatModelID,
	)
	if errors.Is(err, models.ErrCompactionModelNotConfigured) {
		return compaction.TriggerConfig{}, errCompactNoModel
	}
	if models.IsCompactionModelUnavailable(err) {
		return compaction.TriggerConfig{}, errCompactModelUnavailable
	}
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	creds, err := h.providersService.ResolveModelCredentials(cc.Ctx, resolution.Provider)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to resolve credentials: %w", err)
	}
	cfg := compaction.NewTriggerConfig(compaction.TriggerModel{
		Slug:                  resolution.Model.ModelID,
		RecordID:              resolution.Model.ID,
		ClientType:            resolution.Provider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(resolution.Provider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(resolution.Provider, models.ChatCompletionsCompatConfigKey),
		PromptCacheTTL:        providers.ProviderConfigString(resolution.Provider, "prompt_cache_ttl"),
		WindowTokens:          resolution.WindowTokens,
	})
	if chatModelID := strings.TrimSpace(botSettings.ChatModelID); chatModelID != "" {
		chatModel, chatErr := h.modelsService.GetByID(cc.Ctx, chatModelID)
		if chatErr == nil && chatModel.Config.ContextWindow != nil && *chatModel.Config.ContextWindow > 0 {
			cfg.ContextWindowTokens = *chatModel.Config.ContextWindow
		}
	}
	cfg.BotID = cc.BotID
	cfg.SessionID = sessionID
	cfg.Ratio = 100
	cfg.TotalInputTokens = 1
	cfg.Manual = true
	cfg.AllowFrontierFusion = true
	return cfg, nil
}
