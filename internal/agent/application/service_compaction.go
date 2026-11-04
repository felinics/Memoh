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
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/oauthctx"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/settings"
)

// compactForBudget folds the session history into one summary, in as many
// passes as the summarizer needs to read it all, so that what the history
// renders fits historyBudget: the summary and the rows it keeps raw. It
// reports why when the history could not be folded; a failed pass leaves the
// history it did not commit as it was.
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
	// The input and a continuation's steers are anchors, as in the run's own
	// folds; the deferred call and answer a continuation resumes from are its
	// newest call group.
	cfg.Anchors = append(compactionInputs(req), req.continuationAnchors...)
	if len(req.discussCurrentSources) > 0 && req.continuationToolCallID == "" {
		// A Discuss run keeps the inputs it answers where they are while they
		// leave the summary room, without pinning completed tool steps of a
		// failed run that follow an older, still-unanswered input.
		cfg.AnchorPolicy = compaction.AnchorsFirst
	}
	cfg.CurrentRequest = cmp.Or(req.ModelQuery, req.Query)
	// Every row of the thread before the request's own turn that the
	// checkpoint has not folded is a candidate, however far a request's view
	// reaches: the summary absorbs the oldest of them and the checkpoint moves
	// on. The request's rows land at its admitted turn when its first step
	// commits, after whatever other writers append meanwhile, so a fold that
	// reached past that turn would stop holding once they land. An edit or
	// retry compacts only the history before its cutoff, onto the checkpoint
	// its replacement keeps.
	cut, err := s.loadHistoryCut(ctx, req)
	if err != nil {
		return err
	}
	cfg.CandidatesBefore = cut.before
	if cfg.CandidatesBefore == nil && req.TurnPosition != nil {
		cfg.CandidatesBefore = &messagepkg.HistoryPosition{Turn: *req.TurnPosition}
	}
	cfg.ReplacedTurnID = cut.replacedTurnID
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
		WindowTokens:          resolution.WindowTokens,
		MaxOutputTokens:       resolution.Model.Config.MaxOutputTokensLimit(),
	})
	cfg.BotID = req.BotID
	cfg.SessionID = req.ThreadID
	cfg.TotalInputTokens = inputTokens
	cfg.HTTPClient = s.streamHTTPClient
	return cfg, nil
}

// compactionInputs is the current input, which a compaction keeps as an
// anchor. Discuss callers pass the suffix their admission chose, so older
// unconsumed input stays compactable.
func compactionInputs(req ChatRequest) []turn.ContextMessageSource {
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
