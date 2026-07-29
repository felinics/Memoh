package command

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/memohai/memoh/internal/agent/context/compaction"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/providers"
)

// errCompactNoModel is a sentinel returned by buildCompactConfig when neither
// a compaction model nor a chat model is configured. The Handler catches it
// via errors.Is and surfaces a localized user message; other (internal) errors
// flow through friendlyCommandError's looksLikeInternalError path.
var errCompactNoModel = errors.New("compact: no compaction or chat model configured")

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
				return "", err
			}

			res, err := h.compactionService.RunCompactionSync(cc.Ctx, cfg)
			if err != nil {
				return "", fmt.Errorf("compaction failed: %w", err)
			}
			if res.Status != compaction.StatusOK {
				return cc.T("cmd.compact.noop"), nil
			}
			return cc.T("cmd.compact.done"), nil
		},
	})
	return g
}

func (h *Handler) buildCompactConfig(cc CommandContext, sessionID string) (compaction.TriggerConfig, error) {
	botSettings, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to load settings: %w", err)
	}
	modelID := botSettings.CompactionModelID
	if modelID == "" {
		modelID = botSettings.ChatModelID
	}
	if modelID == "" {
		return compaction.TriggerConfig{}, errCompactNoModel
	}

	compactModel, err := h.modelsService.GetByID(cc.Ctx, modelID)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to load compaction model: %w", err)
	}
	if !compactModel.Enable {
		return compaction.TriggerConfig{}, fmt.Errorf("compaction model %s is disabled", compactModel.ModelID)
	}
	compactProvider, err := models.FetchProviderByID(cc.Ctx, h.sqlcQueries, compactModel.ProviderID)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to load provider: %w", err)
	}
	creds, err := h.providersService.ResolveModelCredentials(cc.Ctx, compactProvider)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to resolve credentials: %w", err)
	}

	cfg := compaction.TriggerConfig{
		BotID:                 cc.BotID,
		SessionID:             sessionID,
		ModelID:               compactModel.ModelID,
		ClientType:            compactProvider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(compactProvider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(compactProvider, models.ChatCompletionsCompatConfigKey),
		Ratio:                 100,
		TotalInputTokens:      1,
		PromptCacheTTL:        providers.ProviderConfigString(compactProvider, "prompt_cache_ttl"),
		Manual:                true,
	}
	if compactModel.Config.ContextWindow != nil && *compactModel.Config.ContextWindow > 0 {
		cfg.MaxCompactTokens = *compactModel.Config.ContextWindow * 90 / 100
	}
	return cfg, nil
}
