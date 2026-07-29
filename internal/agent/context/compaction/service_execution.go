package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/models"
)

func (s *Service) doCompaction(ctx context.Context, botUUID pgtype.UUID, sessionUUID pgtype.UUID, cfg TriggerConfig) (Result, error) {
	rows, err := s.queries.ListUncompactedMessagesBySession(ctx, sessionUUID)
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return Result{Status: StatusNoop}, nil
	}

	messages, barrierCount := itemsFromRows(rows)
	if barrierCount > 0 {
		s.logger.Warn("compaction: kept unparseable history rows as span barriers",
			slog.Int("barrier_count", barrierCount),
			slog.String("session_id", cfg.SessionID),
		)
	}
	if len(messages) == 0 {
		return Result{Status: StatusNoop}, nil
	}

	var toCompact []CompactionCandidate
	if cfg.TargetTokens > 0 {
		// Sync compaction: compress enough messages to bring context
		// down to TargetTokens. Calculate how many tokens to keep
		// (newest messages) and compact everything older.
		toCompact = splitByTarget(messages, cfg.TargetTokens)
	} else {
		toCompact = splitByRatio(messages, cfg.TotalInputTokens, cfg.Ratio)
	}
	if len(toCompact) == 0 {
		return Result{Status: StatusNoop}, nil
	}

	// Cap the compaction input to avoid exceeding the compaction model's
	// context window. MaxCompactTokens is typically set to 90% of the model's
	// window. If not set, use a conservative default of 30K tokens. Prior
	// summaries and message entries share this one budget — an additive prior
	// allowance would let the combined prompt exceed the window headroom.
	maxCompactTokens := cfg.MaxCompactTokens
	if maxCompactTokens <= 0 {
		maxCompactTokens = 30000
	}

	frontier, err := NewArtifactProjection(s.queries).LoadActiveSession(ctx, ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		return Result{}, err
	}
	for _, issue := range frontier.Issues {
		s.logger.Warn("compaction: ignored invalid artifact lineage", slog.String("issue", issue.Error()))
	}
	var priorSummaries []string
	for _, artifact := range frontier.Artifacts {
		if strings.TrimSpace(artifact.Summary) != "" {
			priorSummaries = append(priorSummaries, artifact.Summary)
		}
	}
	priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens/4)
	priorTokens := priorContextTokens(priorSummaries)
	// capPriorSummaries always keeps the newest summary, so a single oversized
	// one can exceed its allowance; floor the entries budget at half the total
	// so compaction keeps making progress.
	entriesBudget := maxCompactTokens - priorTokens
	if entriesBudget < maxCompactTokens/2 {
		entriesBudget = maxCompactTokens / 2
	}

	s.logger.Info("compaction: before trim",
		slog.Int("messages", len(toCompact)),
		slog.Int("total_uncompacted", len(messages)),
		slog.Int("max_compact_tokens", maxCompactTokens),
		slog.Int("prior_context_tokens", priorTokens),
	)
	toCompact = trimCompactMessages(toCompact, entriesBudget)
	// The progress guarantee may keep one oversized markable group past the
	// entries budget; the prior context is reference-only, so shrink it (down
	// to nothing) before letting the combined prompt exceed MaxCompactTokens.
	if entriesCost := markableCompactCost(toCompact); entriesCost+priorTokens > maxCompactTokens {
		priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens-entriesCost)
		priorTokens = priorContextTokens(priorSummaries)
	}
	s.logger.Info("compaction: after trim",
		slog.Int("messages", len(toCompact)),
		slog.Int("prior_summaries", len(priorSummaries)),
	)

	entries, compactedMessageIDs := buildEntriesAndIDs(toCompact)
	if len(entries) == 0 || len(compactedMessageIDs) == 0 {
		// No complete group survived: every selected group had a row that rendered
		// empty (a reasoning-only message, or a tool exchange whose result renders
		// empty). buildEntriesAndIDs withholds such a group from both entries and
		// ids, so summarizing here would either destroy rows for a junk summary or
		// mark rows we cannot faithfully summarize. Leave them in raw history.
		return Result{Status: StatusNoop}, nil
	}
	expectedCompactIDs, err := expectedCompactionClaims(rows, compactedMessageIDs)
	if err != nil {
		return Result{}, err
	}
	// Claim the exact selected row versions before loading assets. Asset upserts
	// lock the same message row, so either their mutation is visible below or
	// they invalidate this attempt's epoch before it can complete.
	persistCtx := context.WithoutCancel(ctx)
	logRow, err := s.queries.CreateCompactionLog(persistCtx, sqlc.CreateCompactionLogParams{
		BotID:         botUUID,
		SessionID:     sessionUUID,
		ExpectedEpoch: rows[0].CompactionEpoch,
	})
	if err != nil {
		return Result{}, err
	}
	logID := logRow.ID
	marked, err := s.queries.MarkMessagesCompacted(persistCtx, sqlc.MarkMessagesCompactedParams{
		CompactID:          logID,
		MessageIds:         compactedMessageIDs,
		ExpectedCompactIds: expectedCompactIDs,
	})
	if err != nil {
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	if marked != int64(len(compactedMessageIDs)) {
		err = fmt.Errorf("marked %d of %d compaction source rows", marked, len(compactedMessageIDs))
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	assetRows, err := s.queries.ListMessageAssetsBatch(persistCtx, compactedMessageIDs)
	if err != nil {
		err = fmt.Errorf("load compaction message assets: %w", err)
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	toCompact, err = candidatesWithAssets(toCompact, rows, assetRows)
	if err != nil {
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	artifact, err := artifactMetadataFor(toCompact, compactedMessageIDs)
	if err != nil {
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	// A single markable group larger than the whole budget survives trim by
	// design (progress guarantee); truncate its rendered entries rather than
	// send a prompt the model rejects on every pass. Entry floors can still
	// exceed the budget when that group holds enough rows, so recheck and
	// surface the overshoot instead of claiming an unconditional cap.
	entries = capEntriesToBudget(entries, maxCompactTokens-priorTokens)
	if cost := entriesPromptCost(entries); cost+priorTokens > maxCompactTokens {
		s.logger.Warn("compaction: entry floors exceed the budget, prompt may overflow the compaction window",
			slog.Int("entries", len(entries)),
			slog.Int("entry_tokens", cost),
			slog.Int("max_compact_tokens", maxCompactTokens),
			slog.String("session_id", cfg.SessionID),
		)
	}

	userPrompt := buildUserPrompt(priorSummaries, entries)

	model := models.NewSDKChatModel(models.SDKModelConfig{
		ClientType:            cfg.ClientType,
		BaseURL:               cfg.BaseURL,
		APIKey:                cfg.APIKey,
		CodexAccountID:        cfg.CodexAccountID,
		ModelID:               cfg.ModelID,
		ChatCompletionsCompat: cfg.ChatCompletionsCompat,
		HTTPClient:            cfg.HTTPClient,
	})

	systemPromptDecorated, sdkMessages, _ := models.ApplyPromptCache(
		model, cfg.PromptCacheTTL,
		systemPrompt, []sdk.Message{sdk.UserMessage(userPrompt)}, nil,
	)

	result, err := sdk.GenerateTextResult(ctx,
		sdk.WithModel(model),
		sdk.WithSystem(systemPromptDecorated),
		sdk.WithMessages(sdkMessages),
	)
	if err != nil {
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	if strings.TrimSpace(result.Text) == "" {
		_ = s.completeLog(persistCtx, logID, "error", "", errEmptySummary.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, errEmptySummary
	}

	usageJSON, _ := json.Marshal(result.Usage)

	modelUUID := db.ParseUUIDOrEmpty(cfg.ModelID)
	if err := s.completeLog(persistCtx, logID, "ok", result.Text, "", len(compactedMessageIDs), usageJSON, modelUUID, &artifact); err != nil {
		// The rows are already marked, but the log never reached status=ok, so
		// the reclaim SQL keeps them eligible for a later pass. Reporting ok
		// here would claim a summary that was never persisted.
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	return Result{Status: StatusOK, Summary: result.Text, MessageCount: len(compactedMessageIDs)}, nil
}

func expectedCompactionClaims(rows []sqlc.ListUncompactedMessagesBySessionRow, messageIDs []pgtype.UUID) ([]pgtype.UUID, error) {
	byID := make(map[pgtype.UUID]pgtype.UUID, len(rows))
	for _, row := range rows {
		byID[row.ID] = row.CompactID
	}
	expected := make([]pgtype.UUID, 0, len(messageIDs))
	for _, id := range messageIDs {
		claim, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("compaction source %s missing from selected rows", formatUUID(id))
		}
		expected = append(expected, claim)
	}
	return expected, nil
}

func (s *Service) completeLog(ctx context.Context, logID pgtype.UUID, status, summary, errMsg string, messageCount int, usage []byte, modelID pgtype.UUID, artifact *artifactMetadata) error {
	coverage := []byte("[]")
	var anchorStartMs, anchorEndMs int64
	if artifact != nil {
		coverage = artifact.Coverage
		anchorStartMs = artifact.AnchorStartMs
		anchorEndMs = artifact.AnchorEndMs
	}
	_, err := s.queries.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{
		ID:            logID,
		Status:        status,
		Summary:       summary,
		MessageCount:  int32(messageCount), //nolint:gosec // count always small
		ErrorMessage:  errMsg,
		Usage:         usage,
		ModelID:       modelID,
		Coverage:      coverage,
		AnchorStartMs: anchorStartMs,
		AnchorEndMs:   anchorEndMs,
	})
	if err != nil {
		s.logger.Error("failed to complete compaction log", slog.String("error", err.Error()))
		return err
	}
	return nil
}
