package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/job"
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

// AutoCompactionThreshold is the async trigger level, exported so read-side
// surfaces label the same level the turn path acts on. A zero return leaves
// automatic compaction off when no usable model window is available. The
// synchronous backstop is deliberately not exposed: it only runs on the history
// path, which a reader cannot observe.
func AutoCompactionThreshold(userThreshold, contextTokenBudget int) int {
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

// maybeCompact decides on the caller's goroutine whether this turn's pressure
// calls for compaction, so a turn below the threshold starts no unit, and runs
// the compaction as a background unit when it does.
func (s *Service) maybeCompact(ctx context.Context, req ChatRequest, rc resolvedContext, inputTokens int) {
	plan, ok := s.planCompaction(ctx, req, rc, inputTokens)
	if !ok {
		return
	}
	job.Go(ctx, s.logger, "agent.compaction", job.Options{}, func(ctx context.Context) error {
		return s.runCompaction(ctx, plan)
	},
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.Int("input_tokens", plan.inputTokens),
		slog.Int("threshold", plan.threshold),
	)
}

// compactionPlan is what an automatic compaction trigger runs with.
type compactionPlan struct {
	req         ChatRequest
	rc          resolvedContext
	settings    settings.Settings
	inputTokens int
	threshold   int
}

// planCompaction reports whether the pressure crosses the automatic trigger.
func (s *Service) planCompaction(ctx context.Context, req ChatRequest, rc resolvedContext, inputTokens int) (compactionPlan, bool) {
	inputTokens = asyncCompactionInputTokens(rc, inputTokens)
	if s.compactionService == nil || s.settingsService == nil {
		s.logger.InfoContext(ctx, "compaction: skipped, service or settings nil")
		return compactionPlan{}, false
	}
	botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
	if err != nil {
		result := errlog.Event(ctx, "agent.compaction", errs.Wrap(err, "load compaction settings", slog.String("bot_id", req.BotID)), errlog.Options{})
		s.logger.LogAttrs(ctx, result.Level, "compaction: failed to load settings", result.Attrs()...)
		return compactionPlan{}, false
	}
	if !botSettings.CompactionEnabled {
		s.logger.InfoContext(ctx, "compaction: skipped, disabled")
		return compactionPlan{}, false
	}
	threshold := AutoCompactionThreshold(botSettings.CompactionThreshold, rc.contextTokenBudget)
	if threshold <= 0 {
		s.logger.InfoContext(ctx, "compaction: skipped, no usable threshold",
			slog.Int("configured_threshold", botSettings.CompactionThreshold),
			slog.Int("context_token_budget", rc.contextTokenBudget),
		)
		return compactionPlan{}, false
	}
	if !compaction.ShouldCompact(inputTokens, threshold) {
		s.logger.InfoContext(ctx, "compaction: skipped, below threshold",
			slog.Int("input_tokens", inputTokens),
			slog.Int("threshold", threshold),
		)
		return compactionPlan{}, false
	}
	return compactionPlan{req: req, rc: rc, settings: botSettings, inputTokens: inputTokens, threshold: threshold}, true
}

// runCompaction is the body of one automatic compaction unit.
func (s *Service) runCompaction(ctx context.Context, plan compactionPlan) error {
	cfg, err := s.buildCompactionConfig(ctx, plan.req, plan.settings, plan.inputTokens, plan.rc.model.ID)
	if err != nil {
		return errs.Wrap(err, "build compaction config")
	}
	if cfg.ModelID == "" {
		// buildCompactionConfig returns an empty cfg when no compaction model
		// is configured or the configured one is disabled. Skip the trigger
		// so the compaction service doesn't run hooks + fail on empty UUIDs.
		job.Annotate(ctx, slog.String("skipped", "no_compaction_model"))
		return nil
	}
	cfg.TargetTokens = compactionTargetTokens(plan.settings.CompactionTargetPercent, plan.rc.contextTokenBudget)
	cfg.AllowFrontierFusion = true
	cfg.ContextWindowTokens = plan.rc.contextTokenBudget
	cfg.HardPressure = syncCompactionShouldRun(plan.inputTokens, plan.rc.contextTokenBudget)
	return s.drainCompactionBacklog(ctx, cfg)
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
		if errors.Is(err, compaction.ErrIneffectiveSummary) {
			// The next pass selects past the rows that did not shrink.
			continue
		}
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
		s.logger.WarnContext(ctx, "compaction sync: skipped, service or settings nil")
		return compaction.Result{}
	}
	botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
	if err != nil {
		s.logger.WarnContext(ctx, "compaction sync: failed to load settings", slog.Any("error", err))
		return compaction.Result{}
	}
	if !botSettings.CompactionEnabled {
		s.logger.WarnContext(ctx, "compaction sync: compaction disabled, skipping")
		return compaction.Result{}
	}
	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens, turnModelID)
	if err != nil {
		s.logger.WarnContext(ctx, "compaction sync: failed to build config", slog.Any("error", err))
		return compaction.Result{}
	}
	if cfg.ModelID == "" {
		// Same skip path as the async trigger above — no model or model
		// disabled means there is nothing to compact.
		return compaction.Result{}
	}
	cfg.TargetTokens = syncBackstopTargetTokens(botSettings.CompactionTargetPercent, contextTokenBudget)
	cfg.ContextWindowTokens = contextTokenBudget
	cfg.HardPressure = syncCompactionShouldRun(inputTokens, contextTokenBudget)

	s.logger.InfoContext(ctx, "compaction sync: running synchronously",
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.Int("input_tokens", inputTokens),
		slog.String("model_id", cfg.ModelID),
	)

	done := s.enterSessionCompactionForRun(req.BotID, req.ThreadID, strings.TrimSpace(req.RunID))
	defer done()
	res, err := s.compactionService.RunCompactionSync(ctx, cfg)
	if err != nil {
		s.logger.WarnContext(ctx, "compaction sync: failed", slog.Any("error", err))
		return compaction.Result{}
	}
	s.logger.InfoContext(ctx, "compaction sync: finished",
		slog.String("bot_id", req.BotID),
		slog.String("session_id", req.ThreadID),
		slog.String("status", res.Status),
		slog.String("reason", res.Reason),
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
		s.logger.InfoContext(ctx, "compaction: skipped",
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
	cfg.HTTPClient = s.compactionHTTPClient
	return cfg, nil
}
