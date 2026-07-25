package command

import (
	"errors"
	"fmt"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
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
				if h.queries == nil {
					return cc.T("cmd.session.noActive"), nil
				}
				latestSessionID, err := h.queries.GetLatestSessionIDByBot(cc.Ctx, cc.BotID)
				if err != nil {
					return cc.T("cmd.session.noActive"), nil
				}
				sessionID = latestSessionID
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
	compactProvider, err := modelcatalog.FetchProviderByID(cc.Ctx, h.modelProviderResolver, compactModel.ProviderID)
	if err != nil {
		return compaction.TriggerConfig{}, fmt.Errorf("failed to load provider: %w", err)
	}

	cfg := compaction.TriggerConfig{
		BotID:            cc.BotID,
		SessionID:        sessionID,
		ModelID:          compactModel.ModelID,
		ClientType:       string(compactProvider.ClientType),
		APIKey:           compactProvider.APIKey,
		CodexAccountID:   compactProvider.CodexAccountID,
		BaseURL:          compactProvider.BaseURL,
		Ratio:            100,
		TotalInputTokens: 1,
		PromptCacheTTL:   compactProvider.PromptCacheTTL,
		Manual:           true,
	}
	if compactModel.Config.ContextWindow != nil && *compactModel.Config.ContextWindow > 0 {
		cfg.MaxCompactTokens = *compactModel.Config.ContextWindow * 90 / 100
	}
	return cfg, nil
}
