package application

import (
	"context"
	"log/slog"
	"strings"

	"github.com/memohai/memoh/internal/agent/context/compaction"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/oauthctx"
	"github.com/memohai/memoh/internal/providers"
	"github.com/memohai/memoh/internal/settings"
)

// compactionBudgetThresholdPercent is the shared budget share at which
// compaction triggers: the pre-send synchronous backstop fires when
// compactable history reaches it, and async triggers clamp the user
// threshold to it so they fire before the blocking backstop does.
const compactionBudgetThresholdPercent = 70

// effectiveCompactionThreshold clamps the user-configured absolute threshold
// to the budget share, so an absolute default (e.g. 100000) still fires on
// models whose context window never reaches it. A non-positive threshold
// keeps async compaction disabled.
func effectiveCompactionThreshold(threshold, contextTokenBudget int) int {
	if threshold <= 0 || contextTokenBudget <= 0 {
		return threshold
	}
	budgetThreshold := contextTokenBudget * compactionBudgetThresholdPercent / 100
	if budgetThreshold > 0 && budgetThreshold < threshold {
		return budgetThreshold
	}
	return threshold
}

func asyncCompactionInputTokens(rc resolvedContext, providerInputTokens int) int {
	if rc.compactableTokensKnown {
		return rc.compactableTokens
	}
	return providerInputTokens
}

func (s *Service) maybeCompact(ctx context.Context, req ChatRequest, rc resolvedContext, inputTokens int) {
	done := s.enterSessionCompaction(req.BotID, req.ThreadID)
	defer done()
	inputTokens = asyncCompactionInputTokens(rc, inputTokens)
	if s.compactionService == nil || s.settingsService == nil {
		s.logger.Info("compaction: skipped, service or settings nil")
		return
	}
	botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
	if err != nil {
		s.logger.Warn("compaction: failed to load settings", slog.Any("error", err))
		return
	}
	if !botSettings.CompactionEnabled || botSettings.CompactionThreshold <= 0 {
		s.logger.Info("compaction: skipped, disabled or no threshold",
			slog.Bool("enabled", botSettings.CompactionEnabled),
			slog.Int("threshold", botSettings.CompactionThreshold),
		)
		return
	}
	threshold := effectiveCompactionThreshold(botSettings.CompactionThreshold, rc.contextTokenBudget)
	if !compaction.ShouldCompact(inputTokens, threshold) {
		s.logger.Info("compaction: skipped, below threshold",
			slog.Int("input_tokens", inputTokens),
			slog.Int("threshold", threshold),
		)
		return
	}

	s.logger.Info("compaction: triggering",
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.Int("input_tokens", inputTokens),
		slog.Int("threshold", threshold),
		slog.Int("ratio", botSettings.CompactionRatio),
	)

	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens)
	if err != nil {
		s.logger.Warn("compaction: failed to build config", slog.Any("error", err))
		return
	}
	if cfg.ModelID == "" {
		// buildCompactionConfig returns an empty cfg when no compaction model
		// is configured or the configured one is disabled. Skip the trigger
		// so the compaction service doesn't run hooks + fail on empty UUIDs.
		return
	}
	if err := s.compactionService.RunCompaction(ctx, cfg); err != nil {
		s.logger.Error("compaction failed", slog.String("bot_id", cfg.BotID), slog.String("session_id", cfg.SessionID), slog.Any("error", err))
	}
}

// runCompactionSync runs compaction synchronously when context reaches
// 70% of the model's context window and reports the session-scoped result.
// A noop (failure cooldown, another compaction in flight, or nothing to
// compact) leaves this turn's context untouched: the request proceeds as-is,
// possibly still above the threshold, and the next turn re-evaluates.
func (s *Service) runCompactionSync(ctx context.Context, req ChatRequest, inputTokens, contextTokenBudget int) compaction.Result {
	if s.compactionService == nil || s.settingsService == nil {
		s.logger.Warn("compaction sync: skipped, service or settings nil")
		return compaction.Result{}
	}
	botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
	if err != nil {
		s.logger.Warn("compaction sync: failed to load settings", slog.Any("error", err))
		return compaction.Result{}
	}
	if !botSettings.CompactionEnabled {
		s.logger.Warn("compaction sync: compaction disabled, skipping")
		return compaction.Result{}
	}

	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens)
	if err != nil {
		s.logger.Warn("compaction sync: failed to build config", slog.Any("error", err))
		return compaction.Result{}
	}
	if cfg.ModelID == "" {
		// Same skip path as the async trigger above — no model or model
		// disabled means there is nothing to compact.
		return compaction.Result{}
	}
	cfg.TargetTokens = syncCompactionTargetTokens(contextTokenBudget, cfg.Ratio)

	s.logger.Info("compaction sync: running synchronously",
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.Int("input_tokens", inputTokens),
		slog.String("model_id", cfg.ModelID),
	)

	done := s.enterSessionCompactionForRun(req.BotID, req.ThreadID, strings.TrimSpace(req.RunID))
	defer done()
	res, err := s.compactionService.RunCompactionSync(ctx, cfg)
	if err != nil {
		s.logger.Warn("compaction sync: failed", slog.Any("error", err))
		return compaction.Result{}
	}
	s.logger.Info("compaction sync: finished",
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.String("status", res.Status),
	)
	return res
}

// buildCompactionConfig resolves the compaction model, provider credentials,
// and sets MaxCompactTokens to 90% of the compaction model's context window.
func (s *Service) buildCompactionConfig(ctx context.Context, req ChatRequest, botSettings settings.Settings, inputTokens int) (compaction.TriggerConfig, error) {
	modelID := botSettings.CompactionModelID
	if modelID == "" {
		return compaction.TriggerConfig{}, nil
	}

	ratio := botSettings.CompactionRatio
	if ratio <= 0 || ratio > 100 {
		ratio = 80
	}

	compactModel, err := s.modelsService.GetByID(ctx, modelID)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	if !compactModel.Enable {
		// Silently skip auto-compaction when the configured model is
		// disabled — matches the existing "no model configured" path so the
		// bot keeps running without spending tokens on a model the user
		// explicitly turned off.
		return compaction.TriggerConfig{}, nil
	}

	compactProvider, err := models.FetchProviderByID(ctx, s.queries, compactModel.ProviderID)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, req.UserID)
	creds, err := authService.ResolveModelCredentials(authCtx, compactProvider)
	if err != nil {
		return compaction.TriggerConfig{}, err
	}

	cfg := compaction.TriggerConfig{
		BotID:                 req.BotID,
		SessionID:             req.ThreadID,
		ModelID:               compactModel.ModelID,
		ClientType:            compactProvider.ClientType,
		APIKey:                creds.APIKey,
		CodexAccountID:        creds.CodexAccountID,
		BaseURL:               providers.ProviderConfigString(compactProvider, "base_url"),
		ChatCompletionsCompat: providers.ProviderConfigString(compactProvider, models.ChatCompletionsCompatConfigKey),
		Ratio:                 ratio,
		TotalInputTokens:      inputTokens,
		HTTPClient:            s.streamHTTPClient,
		PromptCacheTTL:        providers.ProviderConfigString(compactProvider, "prompt_cache_ttl"),
	}

	// Cap compaction input to 90% of the compaction model's context window.
	if compactModel.Config.ContextWindow != nil && *compactModel.Config.ContextWindow > 0 {
		cfg.MaxCompactTokens = *compactModel.Config.ContextWindow * 90 / 100
	}

	return cfg, nil
}

// syncCompactionTargetTokens derives the synchronous-compaction goal from the
// context budget: after compaction the kept tail should be the (100-ratio)%
// share the user asked to preserve, instead of a fixed absolute size.
func syncCompactionTargetTokens(contextTokenBudget, ratio int) int {
	if contextTokenBudget <= 0 || ratio >= 100 {
		return 0
	}
	return contextTokenBudget * (100 - ratio) / 100
}
