package application

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

// compactForBudget folds the session history before the protected tail into
// one summary that fits historyBudget, in as many passes as the summarizer
// needs to read it all. It reports why when the history could not be folded;
// a failed pass leaves the history it did not commit as it was.
func (s *Service) compactForBudget(ctx context.Context, req ChatRequest, inputTokens, historyBudget int, turnModelID string) error {
	if s.compactionService == nil || s.settingsService == nil {
		return errors.New("compaction service unavailable")
	}
	botSettings, err := s.settingsService.GetBot(ctx, req.BotID)
	if err != nil {
		return fmt.Errorf("load compaction settings: %w", err)
	}
	cfg, err := s.buildCompactionConfig(ctx, req, botSettings, inputTokens, turnModelID)
	if err != nil {
		return err
	}
	cfg.ProtectedSources = compactionProtectedSources(req)
	if anchors := req.compactionAnchors(); len(anchors) > 0 {
		cfg.Anchors, cfg.ProtectedSources = anchors, req.continuationTail
	} else if len(req.discussCurrentSources) > 0 {
		// Discuss admission budgets the retained inputs separately. Keep each
		// input in place without pinning completed tool steps from a failed run
		// that follow an older, still-unanswered input.
		cfg.Anchors, cfg.ProtectedSources = cfg.ProtectedSources, nil
	}
	cfg.CurrentRequest = cmp.Or(req.ModelQuery, req.Query)
	// Every row of the thread the summaries have not replaced is a
	// candidate, however far a request's view reaches: the summary absorbs
	// the oldest of them and the anchor moves on. An edit or retry compacts
	// only the history before its cutoff.
	if cutoff := strings.TrimSpace(req.HistoryCutoffBeforeMessageID); cutoff != "" {
		message, err := s.messageService.GetByIDBySession(ctx, req.ThreadID, cutoff)
		if err != nil {
			return fmt.Errorf("load history cutoff: %w", err)
		}
		cfg.CandidatesBefore = message.CreatedAt
	}
	cfg.HistoryBudgetTokens = historyBudget

	start := time.Now()
	passes, err := s.compactionService.Compact(ctx, cfg)
	if err != nil {
		s.logger.WarnContext(ctx, "compaction failed",
			slog.String("bot_id", req.BotID), slog.String("session_id", req.ThreadID),
			slog.Int("passes", len(passes)), slog.Int64("duration_ms", time.Since(start).Milliseconds()), slog.Any("error", err))
		return err
	}
	s.logger.InfoContext(ctx, "compaction finished",
		slog.String("bot_id", req.BotID), slog.String("session_id", req.ThreadID), slog.Int("passes", len(passes)),
		slog.Int("input_tokens", inputTokens), slog.Int("history_budget_tokens", historyBudget),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()))
	if len(passes) == 0 {
		return errors.New("compaction folded nothing")
	}
	return nil
}

// buildCompactionConfig resolves the summarizer through the shared model
// policy — the turn's actually-resolved model, then the bot chat default, then
// the session's latest model — and attaches credentials.
func (s *Service) buildCompactionConfig(ctx context.Context, req ChatRequest, botSettings settings.Settings, inputTokens int, turnModelID string) (compaction.TriggerConfig, error) {
	sessionModelID := ""
	if strings.TrimSpace(turnModelID) == "" && strings.TrimSpace(botSettings.ChatModelID) == "" {
		sessionModelID = models.LatestSessionModelID(ctx, s.queries, req.ThreadID)
	}
	resolution, err := models.ResolveCompactionModel(
		ctx,
		s.modelsService,
		s.queries,
		turnModelID,
		botSettings.ChatModelID,
		sessionModelID,
	)
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
		MaxOutputTokens:       resolution.Model.Config.MaxOutputTokensLimit(),
	})
	cfg.BotID = req.BotID
	cfg.SessionID = req.ThreadID
	cfg.TotalInputTokens = inputTokens
	cfg.HTTPClient = s.compactionHTTPClient
	return cfg, nil
}

// compactionProtectedSources is the current input a compaction must keep raw.
// Discuss callers pass the protected suffix their admission chose, so older
// unconsumed input stays compactable.
func compactionProtectedSources(req ChatRequest) []turn.ContextMessageSource {
	sources := append([]turn.ContextMessageSource(nil), req.discussCurrentSources...)
	if req.ExternalMessageID != "" {
		sources = append(sources, turn.ContextMessageSource{Kind: "external", ID: req.ExternalMessageID, Current: !req.inputInHistory})
	}
	for _, id := range []string{req.RequiredHistoryMessageID, req.PersistedUserMessageID} {
		if id != "" {
			sources = append(sources, turn.ContextMessageSource{Kind: "history", ID: id, Current: !req.inputInHistory})
		}
	}
	return sources
}

// compactionAnchors are the sources a compaction keeps raw in place. A
// continuation that resumes after a deferred call keeps its input and steers
// where they are and folds the work around them; the caller prices them, the
// way an in-run fold does, outside the history budget it gives.
func (req ChatRequest) compactionAnchors() []turn.ContextMessageSource {
	if len(req.continuationTail) == 0 {
		return nil
	}
	return append(compactionProtectedSources(req), req.continuationAnchors...)
}
