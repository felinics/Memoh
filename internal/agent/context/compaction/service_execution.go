package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// noopStatus reports a run that appended nothing: StatusOK when the merge
// pass still rewrote the summary chain (history changed, callers must
// reload), StatusNoop when nothing changed at all.
func noopStatus(merged bool) string {
	if merged {
		return StatusOK
	}
	return StatusNoop
}

func (s *Service) doCompaction(ctx context.Context, botUUID pgtype.UUID, sessionUUID pgtype.UUID, cfg TriggerConfig) (Result, error) {
	measure, err := s.queries.MeasureUncompactedMessagesBySession(ctx, sessionUUID)
	if err != nil {
		return Result{}, err
	}
	// New compactions only ever APPEND a segment to the summary chain (Context
	// Epoch spec, rule 3); merging old segments is the separate merge pass.
	// Run that pass first so an over-cap chain left by an earlier merge
	// failure retries on every trigger, even when this run has nothing new to
	// append and would otherwise noop below.
	merged := s.maybeMergeSegments(ctx, botUUID, sessionUUID, cfg)
	if measure.CandidateCount == 0 {
		return Result{Status: noopStatus(merged)}, nil
	}
	readMaxBytes := compactionReadMaxBytes(cfg)
	boundedRows, err := s.queries.ListUncompactedMessagesBySessionWithinBytes(ctx, sqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID: sessionUUID,
		MaxBytes:  readMaxBytes,
	})
	if err != nil {
		return Result{}, err
	}
	rows := uncompactedRowsFromBounded(boundedRows)
	if len(rows) == 0 {
		s.logger.WarnContext(ctx, "compaction: no candidate fits the database read budget",
			slog.String("session_id", cfg.SessionID),
			slog.Int64("candidate_count", measure.CandidateCount),
			slog.Int64("candidate_bytes", measure.CandidateBytes),
			slog.Int64("largest_candidate_bytes", measure.LargestCandidateBytes),
			slog.Int64("read_max_bytes", readMaxBytes))
		return Result{Status: noopStatus(merged)}, nil
	}
	// Use the count captured by the same SQL statement as the payload rows for
	// the selection decision. The metadata-only measure above is observability
	// and oversized-head handling; concurrent claims may legitimately change
	// eligibility between the two statements.
	truncatedRead := false
	if len(boundedRows) > 0 {
		candidateCount := boundedRows[0].CandidateCount
		candidateBytes := boundedRows[0].CandidateBytes
		truncatedRead = candidateCount > int64(len(rows))
		s.logger.InfoContext(ctx, "compaction: bounded candidate read",
			slog.String("session_id", cfg.SessionID),
			slog.Int("loaded_messages", len(rows)),
			slog.Int64("candidate_count", candidateCount),
			slog.Int64("candidate_bytes", candidateBytes),
			slog.Int64("loaded_bytes", boundedRows[len(boundedRows)-1].CumulativeBytes),
			slog.Int64("read_max_bytes", readMaxBytes),
			slog.Bool("truncated", truncatedRead))
	}

	messages, barrierCount := itemsFromRows(rows)
	protectCurrentSources(messages, cfg.ProtectedSources)
	if barrierCount > 0 {
		s.logger.WarnContext(ctx, "compaction: kept unparseable history rows as span barriers",
			slog.Int("barrier_count", barrierCount),
			slog.String("session_id", cfg.SessionID),
		)
	}
	if len(messages) == 0 {
		return Result{Status: noopStatus(merged)}, nil
	}

	// Cap the compaction input to avoid exceeding the compaction model's
	// context window. NewTriggerConfig sets MaxCompactTokens to 85% of the
	// model's window. If not set, use a conservative default of 30K tokens. Prior
	// summaries and message entries share this one budget — an additive prior
	// allowance would let the combined prompt exceed the window headroom.
	baseMaxCompactTokens := cfg.MaxCompactTokens
	if baseMaxCompactTokens <= 0 {
		baseMaxCompactTokens = 30000
	}

	// Bound the summary output and derive the hard input budget: window minus
	// output reserve minus the fixed system prompt and wrapper framing. A
	// window that cannot hold even that fails closed before claiming rows.
	maxOutputTokens := maxCompactionSummaryTokens
	if cfg.SummaryWindowTokens > 0 {
		maxOutputTokens = min(maxCompactionSummaryTokens, max(1, cfg.SummaryWindowTokens/10))
	}
	// The replay budget is a length the prompt states, never the request cap: a
	// reasoning model spends the cap before it writes any summary text.
	summaryLimit := maxOutputTokens
	if cfg.HistoryBudgetTokens > 0 {
		outputBudget := cfg.HistoryBudgetTokens
		if cfg.TargetTokens > 0 {
			outputBudget = min(outputBudget, cfg.TargetTokens)
		}
		summaryLimit = summaryOutputLimit(outputBudget, maxOutputTokens)
		if summaryLimit <= 0 {
			return Result{Status: noopStatus(merged)}, nil
		}
	}
	maxCompactTokens, err := boundedCompactionInputTokens(cfg, baseMaxCompactTokens, maxOutputTokens, systemPrompt)
	if err != nil {
		return Result{}, err
	}

	frontier, err := NewArtifactProjection(s.queries).LoadActiveSession(ctx, ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		return Result{}, err
	}
	for _, issue := range frontier.Issues {
		s.logger.WarnContext(ctx, "compaction: ignored invalid artifact lineage", slog.String("issue", issue.Error()))
	}

	rawTarget := cfg.TargetTokens
	if rawTarget > 0 {
		rawTarget = max(1, rawTarget-frontierSummaryTokens(frontier.Artifacts))
	}
	var toCompact []CompactionCandidate
	switch {
	case truncatedRead:
		// Rows are an oldest-first prefix. When more history exists outside the
		// database admission boundary, compact the admitted prefix and let the
		// existing prompt trim/closure rules choose the largest safe claim. Using
		// splitByTarget here could noop forever because the unseen newest tail is
		// precisely what should be kept.
		toCompact = messages
	case cfg.TargetTokens > 0:
		// Sync compaction: compress enough messages to bring context
		// down to TargetTokens. Calculate how many tokens to keep
		// (newest messages) and compact everything older.
		toCompact = splitByTarget(messages, rawTarget)
	default:
		toCompact = splitByRatio(messages, cfg.TotalInputTokens, cfg.Ratio)
	}
	if len(toCompact) == 0 {
		return Result{Status: noopStatus(merged)}, nil
	}

	var priorSummaries []string
	var priorTokens, entriesBudget int
	for _, artifact := range frontier.Artifacts {
		if strings.TrimSpace(artifact.Summary) != "" {
			priorSummaries = append(priorSummaries, artifact.Summary)
		}
	}
	priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens/4)
	priorTokens = priorContextTokens(priorSummaries)
	// capPriorSummaries always keeps the newest summary, so a single oversized
	// one can exceed its allowance; floor the entries budget at half the total
	// so compaction keeps making progress.
	entriesBudget = maxCompactTokens - priorTokens
	if entriesBudget < maxCompactTokens/2 {
		entriesBudget = maxCompactTokens / 2
	}

	s.logger.InfoContext(ctx, "compaction: before trim",
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
	s.logger.InfoContext(ctx, "compaction: after trim",
		slog.Int("messages", len(toCompact)),
		slog.Int("prior_summaries", len(priorSummaries)),
	)

	entries, compactedMessageIDs := buildEntriesAndIDs(toCompact)
	if len(entries) == 0 || len(compactedMessageIDs) == 0 {
		// Every selectable run renders nothing (reasoning-only messages, empty
		// tool results), so there is nothing to summarize. Leave them in raw
		// history.
		return Result{Status: noopStatus(merged)}, nil
	}
	// Cap the rendered entries and verify the final prompt cost before any
	// row is claimed: a selection that cannot fit (an unsplittable tool
	// exchange larger than the budget) must fail closed with zero claims and
	// zero provider calls instead of overflowing the summarizer window.
	contextTokens := priorTokens
	entries = capEntriesToBudget(entries, maxCompactTokens-contextTokens)
	if cost := entriesPromptCost(entries); cost+contextTokens > maxCompactTokens {
		return Result{}, fmt.Errorf("%w: entries=%d entry_tokens=%d max_compact_tokens=%d",
			errCompactionInputOverflow, len(entries), cost, maxCompactTokens)
	}

	replayBudget, retainedTokens := 0, 0
	if cfg.HistoryBudgetTokens > 0 {
		retainedTokens = retainedReplayTokens(messages, compactedMessageIDs, frontier.Artifacts, false, cfg.ProtectedSources)
		replayBudget = cfg.TargetTokens
		if replayBudget <= 0 {
			replayBudget = cfg.HistoryBudgetTokens
		}
		if remaining := cfg.HistoryBudgetTokens - retainedTokens; remaining > summaryProviderReplayTokens("") {
			replayBudget = min(replayBudget, remaining)
		}
		summaryLimit = summaryOutputLimit(replayBudget, maxOutputTokens)
		if summaryLimit <= 0 {
			return Result{Status: noopStatus(merged)}, nil
		}
	}

	expectedCompactIDs, err := expectedCompactionClaims(rows, compactedMessageIDs)
	if err != nil {
		return Result{}, err
	}
	// Claim the exact selected row versions before loading assets. Asset upserts
	// lock the same message row, so either their mutation is visible below or
	// they invalidate this attempt's epoch before it can complete.
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var epoch int64
	if len(rows) > 0 {
		epoch = rows[0].CompactionEpoch
	} else {
		session, err := s.queries.GetSessionByID(ctx, sessionUUID)
		if err != nil {
			return Result{}, err
		}
		epoch = session.CompactionEpoch
	}
	persistCtx := context.WithoutCancel(ctx)
	logRow, err := s.queries.CreateCompactionLog(persistCtx, sqlc.CreateCompactionLogParams{
		BotID:         botUUID,
		SessionID:     sessionUUID,
		ExpectedEpoch: epoch,
	})
	if err != nil {
		return Result{}, err
	}
	logID := logRow.ID
	var assetRows []sqlc.ListMessageAssetsBatchRow
	if len(compactedMessageIDs) > 0 {
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

		assetRows, err = s.queries.ListMessageAssetsBatch(persistCtx, compactedMessageIDs)
		if err != nil {
			err = fmt.Errorf("load compaction message assets: %w", err)
			_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
			return Result{}, err
		}
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

	userPrompt := buildUserPrompt(priorSummaries, entries)
	userPrompt += fmt.Sprintf("\nKeep the summary under %d tokens.\n", summaryLimit)

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

	request := sdk.Request{
		System:    systemPromptDecorated,
		Messages:  sdkMessages,
		MaxTokens: &maxOutputTokens,
	}
	result, err := modelretry.Do(models.WithModelSession(ctx, cfg.SessionID), s.logger, "agent.compaction", modelretry.Config{}, modelretry.Retryable,
		func(ctx context.Context) (sdk.ModelResult, error) { return model.Generate(ctx, request) })
	if err != nil {
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	summary := strings.TrimSpace(result.Text)
	if summary == "" {
		_ = s.completeLog(persistCtx, logID, "error", "", errEmptySummary.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, errEmptySummary
	}
	if result.FinishReason != sdk.FinishReasonStop {
		err = fmt.Errorf("%w: finish_reason=%s", errIncompleteSummary, result.FinishReason)
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	replacementTokens := max(entriesPromptCost(entries), compactedReplayTokens(messages, compactedMessageIDs))
	if summaryTokens := estimateSummaryReplayTokens(summary); summaryTokens >= replacementTokens {
		err = fmt.Errorf("%w: summary_tokens=%d raw_tokens=%d", errIneffectiveSummary, summaryTokens, replacementTokens)
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	if replayBudget > 0 && summaryProviderReplayTokens(summary) > replayBudget {
		err = fmt.Errorf("compaction: summary replay exceeds output budget: replay=%d budget=%d", summaryProviderReplayTokens(summary), replayBudget)
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}

	usageJSON, _ := json.Marshal(result.Usage)

	modelUUID := db.ParseUUIDOrEmpty(cfg.ModelRecordID)
	if err := s.completeLog(persistCtx, logID, "ok", summary, "", len(compactedMessageIDs), usageJSON, modelUUID, &artifact); err != nil {
		// The rows are already marked, but the log never reached status=ok, so
		// the reclaim SQL keeps them eligible for a later pass. Reporting ok
		// here would claim a summary that was never persisted.
		_ = s.completeLog(persistCtx, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Result{}, err
	}
	status := StatusOK
	if cfg.HistoryBudgetTokens > 0 && (truncatedRead || retainedTokens+summaryProviderReplayTokens(summary) > cfg.HistoryBudgetTokens) {
		status = StatusProgress
	}
	// The append succeeded; the chain may now exceed the segment cap, so run
	// the merge pass (spec: merging old segments advances the epoch with
	// reason segment-merge in the same write as the supersession). Keep the
	// caller's context so the merge's model call honors cancellation and
	// deadlines; the pass detaches only its bookkeeping writes internally,
	// and a cancelled merge retries on the next trigger.
	s.maybeMergeSegments(ctx, botUUID, sessionUUID, cfg)
	return Result{Status: status, Summary: summary, MessageCount: len(compactedMessageIDs)}, nil
}

func boundedCompactionInputTokens(cfg TriggerConfig, maxCompactTokens, maxOutputTokens int, prompt string) (int, error) {
	if cfg.SummaryWindowTokens <= 0 {
		return maxCompactTokens, nil
	}
	fixedPromptTokens := estimateBytesAsTokens(prompt) + compactionPromptFramingTokens
	inputBudget := cfg.SummaryWindowTokens - maxOutputTokens - fixedPromptTokens
	if inputBudget <= 0 {
		return 0, fmt.Errorf("%w: window=%d output_reserve=%d fixed_prompt=%d",
			ErrSummaryWindowTooSmall, cfg.SummaryWindowTokens, maxOutputTokens, fixedPromptTokens)
	}
	return min(maxCompactTokens, inputBudget), nil
}

func (s *Service) loadAbsorbedRows(ctx context.Context, artifact Artifact) ([]sqlc.ListUncompactedMessagesBySessionRow, error) {
	compactID, err := db.ParseUUID(artifact.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListMessagesByCompactID(ctx, compactID)
	if err != nil {
		return nil, err
	}
	converted := make([]sqlc.ListUncompactedMessagesBySessionRow, len(rows))
	for i, row := range rows {
		converted[i] = sqlc.ListUncompactedMessagesBySessionRow(row)
	}
	return converted, nil
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
		s.logger.ErrorContext(ctx, "failed to complete compaction log", slog.String("error", err.Error()))
		return err
	}
	return nil
}

// estimateSummaryReplayTokens meters the summary with the same byte-based
// estimator the selection path uses, so the ineffective-summary check
// compares like units.
func estimateSummaryReplayTokens(summary string) int {
	return estimateBytesAsTokens("<summary>\n" + strings.TrimSpace(summary) + "\n</summary>")
}

// compactionPromptFramingTokens is a fixed safety allowance for the user
// prompt wrapper, entry headers, and provider framing that the byte estimator
// does not attribute to any single entry.
const compactionPromptFramingTokens = 512
