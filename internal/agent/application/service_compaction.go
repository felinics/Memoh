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

// compactionBudgetThresholdPercent is the share of a chat model's context
// reserved for history before the ordinary pre-send trimming path intervenes.
const (
	compactionBudgetThresholdPercent = 70
	// maxDiscussMessageTokenBudget is the final guard against advertised model
	// windows that are larger than a compatibility endpoint accepts in one
	// request. It applies even when Bot compaction is disabled.
	maxDiscussMessageTokenBudget = 256000
)

// effectiveCompactionThreshold brings an invalid threshold forward just enough
// for the configured compaction model to accept the history plus its summary.
// Valid user thresholds remain unchanged.
func effectiveCompactionThreshold(threshold, modelContextTokens, ratio int) int {
	if threshold <= 0 || modelContextTokens <= 0 {
		return threshold
	}
	if ratio <= 0 || ratio > 100 {
		ratio = 80
	}
	summaryTarget := compaction.RollingSummaryTargetTokens(threshold, ratio, modelContextTokens)
	promptReserve := modelContextTokens / 100
	if promptReserve < 1024 {
		promptReserve = 1024
	}
	safeThreshold := modelContextTokens - summaryTarget - promptReserve
	if safeThreshold > 0 && safeThreshold < threshold {
		return safeThreshold
	}
	return threshold
}

func asyncCompactionInputTokens(rc resolvedContext, providerInputTokens int) int {
	if rc.compactionInputTokensKnown {
		return rc.compactionInputTokens
	}
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
	modelContextTokens := s.compactionModelContextTokens(ctx, botSettings.CompactionModelID)
	threshold := effectiveCompactionThreshold(botSettings.CompactionThreshold, modelContextTokens, botSettings.CompactionRatio)
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
	cfg.ObservedArtifactIDs = append([]string(nil), rc.compactionArtifactIDs...)
	cfg.ObservedArtifactsKnown = rc.compactionArtifactsKnown
	cfg.Rolling = true
	cfg.SummaryTargetTokens = compaction.RollingSummaryTargetTokens(threshold, cfg.Ratio, cfg.ModelContextTokens)
	if err := s.compactionService.RunCompaction(ctx, cfg); err != nil {
		s.logger.Error("compaction failed", slog.String("bot_id", cfg.BotID), slog.String("session_id", cfg.SessionID), slog.Any("error", err))
	}
}

func (s *Service) compactionModelContextTokens(ctx context.Context, modelID string) int {
	if s.modelsService == nil || strings.TrimSpace(modelID) == "" {
		return 0
	}
	model, err := s.modelsService.GetByID(ctx, modelID)
	if err != nil || model.Config.ContextWindow == nil || *model.Config.ContextWindow <= 0 {
		return 0
	}
	return *model.Config.ContextWindow
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
		cfg.ModelContextTokens = *compactModel.Config.ContextWindow
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
