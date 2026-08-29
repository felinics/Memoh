package application

import (
	"context"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

// Automatic compaction derives its soft trigger, hard backstop, and target
// from the chat model's context window. A configured absolute threshold only
// overrides the async trigger and is capped at the hard backstop.
const (
	compactionSoftThresholdPercent = 50
	compactionHardThresholdPercent = 75
	defaultCompactionTargetPercent = 40
	// maxAsyncCompactionPasses bounds how many consecutive summarizer calls
	// one background trigger may spend draining a backlog.
	maxAsyncCompactionPasses = 3
)

// autoCompactionThreshold is the async trigger level. A zero return leaves
// automatic compaction off when no usable model window is available.
func autoCompactionThreshold(userThreshold, contextTokenBudget int) int {
	if contextTokenBudget <= 0 {
		return 0
	}
	if userThreshold <= 0 {
		return max(1, contextTokenBudget*compactionSoftThresholdPercent/100)
	}
	return min(userThreshold, hardCompactionThreshold(contextTokenBudget))
}

func hardCompactionThreshold(contextTokenBudget int) int {
	if contextTokenBudget <= 0 {
		return 0
	}
	return max(1, contextTokenBudget*compactionHardThresholdPercent/100)
}

func compactionTargetTokens(targetPercent *int, contextTokenBudget int) int {
	if contextTokenBudget <= 0 {
		return 0
	}
	percent := defaultCompactionTargetPercent
	if targetPercent != nil && *targetPercent >= 1 && *targetPercent <= 99 {
		percent = *targetPercent
	}
	return max(1, contextTokenBudget*percent/100)
}

// syncBackstopTargetTokens caps the hard-share backstop at the soft share so it
// always makes progress and lands below the soft trigger, restoring hysteresis.
func syncBackstopTargetTokens(targetPercent *int, contextTokenBudget int) int {
	softTarget := max(1, contextTokenBudget*compactionSoftThresholdPercent/100)
	return min(compactionTargetTokens(targetPercent, contextTokenBudget), softTarget)
}

func syncCompactionShouldRun(pressure, contextTokenBudget int) bool {
	threshold := hardCompactionThreshold(contextTokenBudget)
	return threshold > 0 && pressure >= threshold
}

func asyncCompactionInputTokens(rc resolvedContext, providerInputTokens int) int {
	if rc.compactableTokensKnown {
		return rc.compactableTokens
	}
	return providerInputTokens
}

func (s *Service) maybeCompact(ctx context.Context, req ChatRequest, rc resolvedContext, inputTokens int) {
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
	if !botSettings.CompactionEnabled {
		s.logger.Info("compaction: skipped, disabled")
		return
	}
	threshold := autoCompactionThreshold(botSettings.CompactionThreshold, rc.contextTokenBudget)
	if threshold <= 0 {
		s.logger.Info("compaction: skipped, no usable threshold",
			slog.Int("configured_threshold", botSettings.CompactionThreshold),
			slog.Int("context_token_budget", rc.contextTokenBudget),
		)
		return
	}
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
	)

	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens, rc.model.ID)
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
	cfg.TargetTokens = compactionTargetTokens(botSettings.CompactionTargetPercent, rc.contextTokenBudget)
	cfg.AllowFrontierFusion = true
	cfg.ContextWindowTokens = rc.contextTokenBudget
	if err := s.drainCompactionBacklog(ctx, cfg); err != nil {
		s.logger.Error("compaction failed", slog.String("bot_id", cfg.BotID), slog.String("session_id", cfg.SessionID), slog.Any("error", err))
	}
}

// drainCompactionBacklog runs bounded consecutive passes until the backlog is
// drained (noop), a pass fails, or the pass cap is reached, so a small
// summarizer window cannot strand a large backlog for future turns. The
// session barrier is re-acquired per pass: a turn that arrives between
// passes runs before the next summarizer call instead of waiting out the
// whole drain.
func (s *Service) drainCompactionBacklog(ctx context.Context, cfg compaction.TriggerConfig) error {
	for pass := 0; pass < maxAsyncCompactionPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := s.runCompactionPass(ctx, cfg)
		if err != nil {
			return err
		}
		if res.Status != compaction.StatusOK {
			return nil
		}
	}
	return nil
}

func (s *Service) runCompactionPass(ctx context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	done := s.enterSessionCompaction(cfg.BotID, cfg.SessionID)
	defer done()
	return s.compactionService.RunCompactionSync(ctx, cfg)
}

// runCompactionSync runs compaction synchronously when context reaches the
// blocking share of the model's context window and reports the session-scoped
// result.
// A noop (failure cooldown, another compaction in flight, or nothing to
// compact) leaves this turn's context untouched: the request proceeds as-is,
// possibly still above the threshold, and the next turn re-evaluates.
func (s *Service) runCompactionSync(ctx context.Context, req ChatRequest, inputTokens, contextTokenBudget int, turnModelID string) compaction.Result {
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
	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens, turnModelID)
	if err != nil {
		s.logger.Warn("compaction sync: failed to build config", slog.Any("error", err))
		return compaction.Result{}
	}
	if cfg.ModelID == "" {
		// Same skip path as the async trigger above — no model or model
		// disabled means there is nothing to compact.
		return compaction.Result{}
	}
	cfg.TargetTokens = syncBackstopTargetTokens(botSettings.CompactionTargetPercent, contextTokenBudget)
	cfg.ContextWindowTokens = contextTokenBudget

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

// buildCompactionConfig resolves the summarizer through the shared model
// policy — explicit override first, then the turn's actually-resolved model,
// then the bot chat default, then the session's latest model — and attaches
// credentials. Unavailable-model conditions stand the automatic trigger down
// silently; infrastructure errors propagate.
func (s *Service) buildCompactionConfig(ctx context.Context, req ChatRequest, botSettings settings.Settings, inputTokens int, turnModelID string) (compaction.TriggerConfig, error) {
	sessionModelID := ""
	if strings.TrimSpace(botSettings.CompactionModelID) == "" && strings.TrimSpace(turnModelID) == "" && strings.TrimSpace(botSettings.ChatModelID) == "" {
		sessionModelID = models.LatestSessionModelID(ctx, s.queries, req.ThreadID)
	}
	resolution, err := models.ResolveCompactionModel(
		ctx,
		s.modelsService,
		s.queries,
		botSettings.CompactionModelID,
		turnModelID,
		botSettings.ChatModelID,
		sessionModelID,
	)
	if models.IsCompactionModelUnavailable(err) {
		s.logger.Info("compaction: skipped",
			slog.String("bot_id", req.BotID),
			slog.String("session_id", req.ThreadID),
			slog.Any("reason", err),
		)
		return compaction.TriggerConfig{}, nil
	}
	if err != nil {
		return compaction.TriggerConfig{}, err
	}
	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, req.UserID)
	creds, err := authService.ResolveModelCredentials(authCtx, resolution.Provider)
	if err != nil {
		return compaction.TriggerConfig{}, err
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
	cfg.BotID = req.BotID
	cfg.SessionID = req.ThreadID
	cfg.TotalInputTokens = inputTokens
	cfg.HTTPClient = s.nonStreamingHTTPClient
	return cfg, nil
}
