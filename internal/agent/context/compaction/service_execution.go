package compaction

import (
	"context"
	"encoding/json"
	"errors"
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
	if cfg.Rolling && cfg.SummaryTargetTokens <= 0 {
		return Result{}, errors.New("compaction: rolling summary target must be positive")
	}
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

	frontier, err := NewArtifactProjection(s.queries).LoadActiveSession(ctx, ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		return Result{}, err
	}
	for _, issue := range frontier.Issues {
		s.logger.Warn("compaction: ignored invalid artifact lineage", slog.String("issue", issue.Error()))
	}
	if cfg.ObservedArtifactsKnown && frontierAdvancedSinceObservation(frontier.Artifacts, cfg.ObservedArtifactIDs) {
		s.logger.Info("compaction: skipped stale context pressure after frontier advanced",
			slog.String("bot_id", cfg.BotID),
			slog.String("session_id", cfg.SessionID),
			slog.Int("observed_artifacts", len(cfg.ObservedArtifactIDs)),
			slog.Int("active_artifacts", len(frontier.Artifacts)),
		)
		return Result{Status: StatusNoop}, nil
	}

	var toCompact []CompactionCandidate
	if cfg.Rolling {
		// A rolling pass folds every safely renderable raw row into one
		// replacement summary. The ratio controls output size, not selection.
		toCompact = messages
	} else if cfg.TargetTokens > 0 {
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
	if cfg.Rolling && cfg.ModelContextTokens > 0 {
		promptReserve := cfg.ModelContextTokens / 100
		if promptReserve < 1024 {
			promptReserve = 1024
		}
		inputBudget := cfg.ModelContextTokens - cfg.SummaryTargetTokens - promptReserve
		if inputBudget < maxCompactTokens {
			maxCompactTokens = inputBudget
		}
	}
	if maxCompactTokens <= 0 {
		return Result{}, fmt.Errorf(
			"compaction: summary target %d leaves no input capacity in model context %d",
			cfg.SummaryTargetTokens,
			cfg.ModelContextTokens,
		)
	}

	var priorSummaries []string
	for _, artifact := range frontier.Artifacts {
		if strings.TrimSpace(artifact.Summary) != "" {
			priorSummaries = append(priorSummaries, artifact.Summary)
		}
	}
	if !cfg.Rolling {
		priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens/4)
	}
	priorTokens := priorContextTokens(priorSummaries)
	entriesBudget := maxCompactTokens - priorTokens
	if !cfg.Rolling && entriesBudget < maxCompactTokens/2 {
		// capPriorSummaries always keeps the newest summary, so a single
		// oversized one can exceed its allowance; floor the entries budget at
		// half the total so legacy segmented compaction keeps making progress.
		entriesBudget = maxCompactTokens / 2
	}

	s.logger.Info("compaction: before trim",
		slog.Int("messages", len(toCompact)),
		slog.Int("total_uncompacted", len(messages)),
		slog.Int("max_compact_tokens", maxCompactTokens),
		slog.Int("prior_context_tokens", priorTokens),
	)
	if !cfg.Rolling {
		toCompact = trimCompactMessages(toCompact, entriesBudget)
		// The progress guarantee may keep one oversized markable group past
		// the entries budget; legacy prior context is reference-only, so shrink
		// it before letting the combined prompt exceed MaxCompactTokens.
		if entriesCost := markableCompactCost(toCompact); entriesCost+priorTokens > maxCompactTokens {
			priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens-entriesCost)
			priorTokens = priorContextTokens(priorSummaries)
		}
	}
	s.logger.Info("compaction: after trim",
		slog.Int("messages", len(toCompact)),
		slog.Int("prior_summaries", len(priorSummaries)),
	)

	var entries []messageEntry
	var compactedMessageIDs []pgtype.UUID
	if cfg.Rolling {
		entries, compactedMessageIDs, err = buildRollingEntriesAndIDs(toCompact)
		if err != nil {
			return Result{}, err
		}
	} else {
		entries, compactedMessageIDs = buildEntriesAndIDs(toCompact)
	}
	if len(entries) == 0 || len(compactedMessageIDs) == 0 {
		// No complete group survived: every selected group had a row that rendered
		// empty (a reasoning-only message, or a tool exchange whose result renders
		// empty). buildEntriesAndIDs withholds such a group from both entries and
		// ids, so summarizing here would either destroy rows for a junk summary or
		// mark rows we cannot faithfully summarize. Leave them in raw history.
		return Result{Status: StatusNoop}, nil
	}
	if cfg.Rolling {
		inputCost := entriesPromptCost(entries) + priorTokens
		if inputCost > maxCompactTokens {
			return Result{}, fmt.Errorf(
				"compaction: rolling input %d exceeds model input budget %d; configure a larger compaction model",
				inputCost,
				maxCompactTokens,
			)
		}
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
	if cfg.Rolling {
		artifact, err = rollingArtifactMetadata(artifact, frontier.Artifacts)
		if err != nil {
			_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
			return Result{}, err
		}
	}

	// A single markable group larger than the whole budget survives trim by
	// design (progress guarantee); truncate its rendered entries rather than
	// send a prompt the model rejects on every pass. Entry floors can still
	// exceed the budget when that group holds enough rows, so recheck and
	// surface the overshoot instead of claiming an unconditional cap.
	if !cfg.Rolling {
		entries = capEntriesToBudget(entries, maxCompactTokens-priorTokens)
		if cost := entriesPromptCost(entries); cost+priorTokens > maxCompactTokens {
			s.logger.Warn("compaction: entry floors exceed the budget, prompt may overflow the compaction window",
				slog.Int("entries", len(entries)),
				slog.Int("entry_tokens", cost),
				slog.Int("max_compact_tokens", maxCompactTokens),
				slog.String("session_id", cfg.SessionID),
			)
		}
	}

	selectedSystemPrompt := systemPrompt
	userPrompt := buildUserPrompt(priorSummaries, entries)
	if cfg.Rolling {
		selectedSystemPrompt = rollingSystemPrompt(cfg.SummaryTargetTokens)
		userPrompt = buildRollingUserPrompt(priorSummaries, entries)
	}

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
		selectedSystemPrompt, []sdk.Message{sdk.UserMessage(userPrompt)}, nil,
	)

	generateOptions := []sdk.GenerateOption{
		sdk.WithModel(model),
		sdk.WithSystem(systemPromptDecorated),
		sdk.WithMessages(sdkMessages),
	}
	if cfg.SummaryTargetTokens > 0 {
		generateOptions = append(generateOptions, sdk.WithMaxTokens(cfg.SummaryTargetTokens))
	}
	result, err := sdk.GenerateTextResult(ctx, generateOptions...)
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
	completeErr := error(nil)
	if cfg.Rolling {
		completeErr = s.completeRollingLog(persistCtx, logID, result.Text, len(compactedMessageIDs), usageJSON, modelUUID, artifact)
	} else {
		completeErr = s.completeLog(persistCtx, logID, "ok", result.Text, "", len(compactedMessageIDs), usageJSON, modelUUID, &artifact)
	}
	if completeErr != nil {
		// The rows are already marked, but the log never reached status=ok, so
		// the reclaim SQL keeps them eligible for a later pass. Reporting ok
		// here would claim a summary that was never persisted.
		_ = s.completeLog(persistCtx, logID, "error", "", completeErr.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, completeErr
	}
	if cfg.Rolling {
		s.logger.Info("compaction: rolling summary persisted",
			slog.String("session_id", cfg.SessionID),
			slog.Int("summary_tokens", estimateBytesAsTokens(result.Text)),
			slog.Int("summary_target_tokens", cfg.SummaryTargetTokens),
			slog.Int("raw_messages_compacted", len(compactedMessageIDs)),
			slog.Int("superseded_summaries", len(artifact.ParentIDs)),
		)
	}
	return Result{Status: StatusOK, Summary: result.Text, MessageCount: len(compactedMessageIDs)}, nil
}

func frontierAdvancedSinceObservation(active []Artifact, observedIDs []string) bool {
	observed := make(map[string]struct{}, len(observedIDs))
	for _, id := range observedIDs {
		if id = strings.TrimSpace(id); id != "" {
			observed[id] = struct{}{}
		}
	}
	for _, artifact := range active {
		if _, ok := observed[strings.TrimSpace(artifact.ID)]; !ok {
			return true
		}
	}
	return false
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

func (s *Service) completeRollingLog(
	ctx context.Context,
	logID pgtype.UUID,
	summary string,
	messageCount int,
	usage []byte,
	modelID pgtype.UUID,
	artifact artifactMetadata,
) error {
	_, err := s.queries.CompleteRollingCompactionLog(ctx, sqlc.CompleteRollingCompactionLogParams{
		ID:            logID,
		ParentIds:     artifact.ParentIDs,
		MessageCount:  int32(messageCount), //nolint:gosec // count always small
		Summary:       summary,
		Usage:         usage,
		ModelID:       modelID,
		Coverage:      artifact.Coverage,
		AnchorStartMs: artifact.AnchorStartMs,
		AnchorEndMs:   artifact.AnchorEndMs,
		ArtifactLevel: artifact.Level,
	})
	if err != nil {
		s.logger.Error("failed to complete rolling compaction log", slog.String("error", err.Error()))
		return err
	}
	return nil
}
