package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

func (s *Service) doCompaction(ctx context.Context, botUUID pgtype.UUID, sessionUUID pgtype.UUID, cfg TriggerConfig) (res Result, err error) {
	measure, err := s.queries.MeasureUncompactedMessagesBySession(ctx, sessionUUID)
	if err != nil {
		return Result{}, err
	}
	if measure.CandidateCount == 0 {
		return Result{Status: StatusNoop, Reason: ReasonNothingToCompact}, nil
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
	fusing := shouldFuseFrontier(cfg, frontier.Artifacts, maxCompactTokens) && !s.fusionBackedOff(cfg.SessionID)
	if fusing && !frontierHasPersistedCoverage(frontier.Artifacts) {
		s.logger.WarnContext(ctx, "compaction: frontier fusion skipped",
			slog.String("reason", "legacy_parent_missing_coverage"),
			slog.String("session_id", cfg.SessionID),
		)
		fusing = false
	}
	if fusing {
		defer func() {
			if err != nil || res.Status == StatusOK {
				s.noteFusion(cfg.SessionID, err != nil)
			}
		}()
	}
	selectedSystemPrompt := systemPrompt
	// Every span the floor admits must fit the smallest entries budget.
	minSpanTokens := min(minCompactionSpanTokens, maxCompactTokens/2)
	if fusing {
		selectedSystemPrompt = fusionSystemPrompt
		maxCompactTokens, err = boundedCompactionInputTokens(cfg, baseMaxCompactTokens, maxOutputTokens, selectedSystemPrompt)
		if err != nil {
			return Result{}, err
		}
		// A rollup also replaces the frontier summaries it absorbs, so even a
		// small span shrinks the replayed context.
		minSpanTokens = 0
	}

	// Both entries budgets below are floored at half of maxCompactTokens, so a
	// span claimable within that is claimable within the actual one.
	read, reason, err := s.readCompactionSpan(ctx, sessionUUID, cfg, measure, minSpanTokens, maxCompactTokens/2, false)
	if err == nil && reason != "" && cfg.Manual && read.stats.HeldGaps > 0 {
		// With nothing else to claim, a manual request retries rows held
		// after an unusable summary: the user may just have fixed the model.
		read, reason, err = s.readCompactionSpan(ctx, sessionUUID, cfg, measure, minSpanTokens, maxCompactTokens/2, true)
	}
	if err != nil {
		return Result{}, err
	}
	if reason != "" {
		s.logger.LogAttrs(ctx, slog.LevelInfo, "compaction: no span to claim", read.attrs(cfg, reason)...)
		return Result{Status: StatusNoop, Reason: reason}, nil
	}
	rows, toCompact := read.rows, read.span

	var priorSummaries []string
	var absorbedSegments []absorbedSegment
	var priorTokens, absorbTokens, entriesBudget int
	if fusing {
		absorbedSegments, absorbTokens, err = buildAbsorbedContext(ctx, frontier.Artifacts, maxCompactTokens/2, s.loadAbsorbedRows)
		if err != nil {
			return Result{}, err
		}
		entriesBudget = maxCompactTokens - absorbTokens
		if entriesBudget < maxCompactTokens/2 {
			entriesBudget = maxCompactTokens / 2
		}
	} else {
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
	}

	s.logger.InfoContext(ctx, "compaction: before trim",
		slog.String("session_id", cfg.SessionID),
		slog.Int("messages", len(toCompact)),
		slog.Int("span_tokens", read.stats.SpanTokens),
		slog.Int("max_compact_tokens", maxCompactTokens),
		slog.Int("prior_context_tokens", priorTokens),
		slog.Int("absorbed_context_tokens", absorbTokens),
	)
	floor := min(minCompactionSpanTokens, maxCompactTokens/2)
	toCompact = retrySpan(trimSpan(toCompact, entriesBudget, minSpanTokens), floor)
	var retry attempt
	for _, item := range toCompact {
		retry.unusable = max(retry.unusable, item.UnusableAttempts)
		retry.failedBefore = retry.failedBefore || item.RetryRows > 0
	}
	retry.halves = halfOf(toCompact, floor) > 0
	// The progress guarantee may keep one oversized markable group past the
	// entries budget; the prior context is reference-only, so shrink it (down
	// to nothing) before letting the combined prompt exceed MaxCompactTokens.
	if entriesCost := markableCompactCost(toCompact); !fusing && entriesCost+priorTokens > maxCompactTokens {
		priorSummaries = capPriorSummaries(priorSummaries, maxCompactTokens-entriesCost)
		priorTokens = priorContextTokens(priorSummaries)
	}

	entries, compactedMessageIDs := buildEntriesAndIDs(toCompact)
	if len(entries) == 0 || len(compactedMessageIDs) == 0 {
		return Result{Status: StatusNoop, Reason: ReasonNoBeneficialSpan}, nil
	}
	retry.rows = len(compactedMessageIDs)
	s.logger.InfoContext(ctx, "compaction: after trim",
		slog.String("session_id", cfg.SessionID),
		slog.Int("selected_entry_count", len(entries)),
		slog.Int("claimed_row_count", len(compactedMessageIDs)),
		slog.String("first_message_id", formatUUID(compactedMessageIDs[0])),
		slog.String("last_message_id", formatUUID(compactedMessageIDs[len(compactedMessageIDs)-1])),
		slog.Int("entry_tokens", entriesPromptCost(entries)),
		slog.Int("prior_summaries", len(priorSummaries)),
		slog.Int("absorbed_segments", len(absorbedSegments)),
	)
	// Cap the rendered entries and verify the final prompt cost before any
	// row is claimed: a selection that cannot fit (an unsplittable tool
	// exchange larger than the budget) must fail closed with zero claims and
	// zero provider calls instead of overflowing the summarizer window.
	contextTokens := priorTokens + absorbTokens
	entries = capEntriesToBudget(entries, maxCompactTokens-contextTokens)
	if cost := entriesPromptCost(entries); cost+contextTokens > maxCompactTokens {
		return Result{}, fmt.Errorf("%w: entries=%d entry_tokens=%d max_compact_tokens=%d",
			errCompactionInputOverflow, len(entries), cost, maxCompactTokens)
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
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}
	if marked != int64(len(compactedMessageIDs)) {
		err = fmt.Errorf("marked %d of %d compaction source rows", marked, len(compactedMessageIDs))
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}

	assetRows, err := s.queries.ListMessageAssetsBatch(persistCtx, compactedMessageIDs)
	if err != nil {
		err = fmt.Errorf("load compaction message assets: %w", err)
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}
	toCompact, err = candidatesWithAssets(toCompact, rows, assetRows)
	if err != nil {
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}
	artifact, err := artifactMetadataFor(toCompact, compactedMessageIDs)
	if err != nil {
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}
	if fusing {
		artifact, err = rollupArtifactMetadata(frontier.Artifacts, artifact)
		if err != nil {
			s.failLog(persistCtx, logID, err, retry)
			return Result{}, err
		}
	}

	userPrompt := buildUserPrompt(priorSummaries, entries)
	if fusing {
		userPrompt = buildFusionUserPrompt(priorSummaries, absorbedSegments, entries)
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

	request := sdk.Request{
		System:    systemPromptDecorated,
		Messages:  sdkMessages,
		MaxTokens: &maxOutputTokens,
	}
	result, err := modelretry.Do(models.WithModelSession(ctx, cfg.SessionID), s.logger, "agent.compaction", modelretry.Config{}, modelretry.Retryable,
		func(ctx context.Context) (sdk.ModelResult, error) { return model.Generate(ctx, request) })
	if err != nil {
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}

	summary := strings.TrimSpace(result.Text)
	replacementTokens := entriesPromptCost(entries)
	if fusing {
		replacementTokens = summaryReplacementTokens(entries, frontier.Artifacts)
	}
	summaryTokens := estimateSummaryReplayTokens(summary)
	switch {
	case result.FinishReason == sdk.FinishReasonLength:
		err = fmt.Errorf("%w: %w", errIncompleteSummary, errSummaryCutOff)
	case summary == "":
		err = errEmptySummary
	case result.FinishReason != sdk.FinishReasonStop:
		err = fmt.Errorf("%w: finish_reason=%s", errIncompleteSummary, result.FinishReason)
	case summaryTokens >= replacementTokens && fusing:
		err = fmt.Errorf("%w: summary_tokens=%d raw_tokens=%d", errIneffectiveRollup, summaryTokens, replacementTokens)
	case summaryTokens >= replacementTokens:
		err = fmt.Errorf("summary does not reduce replay tokens: summary_tokens=%d raw_tokens=%d", summaryTokens, replacementTokens)
	}
	if err != nil {
		if fusing {
			err = fmt.Errorf("%w: %w", errRollupFailed, err)
		}
		err = fmt.Errorf("%w: %w", ErrIneffectiveSummary, err)
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}

	usageJSON, _ := json.Marshal(result.Usage)

	modelUUID := db.ParseUUIDOrEmpty(cfg.ModelRecordID)
	if fusing {
		err = s.completeRollupLog(persistCtx, logID, summary, len(compactedMessageIDs), usageJSON, modelUUID, artifact, frontier.Artifacts)
	} else {
		err = s.completeLog(persistCtx, logID, "ok", summary, "", len(compactedMessageIDs), usageJSON, modelUUID, &artifact, "", 0)
	}
	if err != nil {
		// The rows are already marked, but the log never reached status=ok, so
		// the reclaim SQL keeps them eligible for a later pass. Reporting ok
		// here would claim a summary that was never persisted.
		s.failLog(persistCtx, logID, err, retry)
		return Result{}, err
	}
	s.logger.InfoContext(ctx, "compaction: summary committed",
		slog.String("session_id", cfg.SessionID),
		slog.Int("claimed_row_count", len(compactedMessageIDs)),
		slog.Int("replaced_tokens", replacementTokens),
		slog.Int("summary_tokens", summaryTokens),
	)
	return Result{Status: StatusOK, Summary: summary, MessageCount: len(compactedMessageIDs)}, nil
}

const (
	minCompactionReadBytes = 512 << 10
	maxCompactionReadBytes = 8 << 20
)

func compactionReadMaxBytes(cfg TriggerConfig) int64 {
	tokens := cfg.MaxCompactTokens
	if tokens <= 0 {
		tokens = 30000
	}
	bytes := int64(tokens) * 8
	if bytes < minCompactionReadBytes {
		return minCompactionReadBytes
	}
	if bytes > maxCompactionReadBytes {
		return maxCompactionReadBytes
	}
	return bytes
}

func uncompactedRowsFromBounded(rows []sqlc.ListUncompactedMessagesBySessionWithinBytesRow) []sqlc.ListUncompactedMessagesBySessionRow {
	converted := make([]sqlc.ListUncompactedMessagesBySessionRow, len(rows))
	for i, row := range rows {
		converted[i] = sqlc.ListUncompactedMessagesBySessionRow{
			ID: row.ID, BotID: row.BotID, SessionID: row.SessionID,
			SenderChannelIdentityID: row.SenderChannelIdentityID, SenderUserID: row.SenderUserID,
			ExternalMessageID: row.ExternalMessageID, SourceReplyToMessageID: row.SourceReplyToMessageID,
			Role: row.Role, Content: row.Content, Metadata: row.Metadata, Usage: row.Usage,
			EventID: row.EventID, DisplayText: row.DisplayText, CompactID: row.CompactID, CreatedAt: row.CreatedAt,
			SenderDisplayName: row.SenderDisplayName, SenderAvatarUrl: row.SenderAvatarUrl,
			Platform: row.Platform, CompactionEpoch: row.CompactionEpoch,
			ConversationType: row.ConversationType, ConversationName: row.ConversationName, ReplyTarget: row.ReplyTarget,
		}
	}
	return converted
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

// attempt is what a failed claim records for its rows: the unusable attempts
// they had in a row before it, whether they had already failed on their own,
// how many rows it claimed, and whether it could still be halved.
type attempt struct {
	unusable     int
	failedBefore bool
	rows         int
	halves       bool
}

// failLog completes a claimed attempt as failed. A summary that cannot
// replace its rows, or a request the provider rejected for what it holds, is
// recorded by reason so later passes select past those rows: for the epoch
// when the summary was not shorter; not at all when the next pass can retry
// half of the rows; otherwise for a while that grows with each such attempt
// in a row. A failed rollup records nothing against rows that never failed on
// their own, which an ordinary pass tries next; rows that did keep failing.
// Any other failure leaves the rows eligible for a retry.
func (s *Service) failLog(ctx context.Context, logID pgtype.UUID, cause error, retry attempt) {
	reason, attempts, rows := "", 0, 0
	rejected := requestRejected(cause)
	switch {
	case !errors.Is(cause, ErrIneffectiveSummary) && !rejected, errors.Is(cause, errRollupFailed) && !retry.failedBefore:
	case errors.Is(cause, errRollupFailed):
		reason, attempts, rows = failureReasonUnusableSummary, retry.unusable+1, retry.rows
	case (rejected || errors.Is(cause, errSummaryCutOff)) && retry.halves:
		reason, attempts, rows = failureReasonRetryHalf, retry.unusable, retry.rows
	case rejected, errors.Is(cause, errEmptySummary), errors.Is(cause, errIncompleteSummary):
		reason, attempts, rows = failureReasonUnusableSummary, retry.unusable+1, retry.rows
	default:
		reason = failureReasonIneffectiveSummary
	}
	_ = s.completeLog(ctx, logID, "error", "", cause.Error(), rows, nil, pgtype.UUID{}, nil, reason, attempts)
}

// requestRejected reports a provider that refused the request itself, for
// what it holds: too long a prompt, or content it will not take.
func requestRejected(err error) bool {
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func (s *Service) completeLog(ctx context.Context, logID pgtype.UUID, status, summary, errMsg string, messageCount int, usage []byte, modelID pgtype.UUID, artifact *artifactMetadata, failureReason string, failureAttempts int) error {
	coverage := []byte("[]")
	var anchorStartMs, anchorEndMs int64
	if artifact != nil {
		coverage = artifact.Coverage
		anchorStartMs = artifact.AnchorStartMs
		anchorEndMs = artifact.AnchorEndMs
	}
	_, err := s.queries.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{
		ID:              logID,
		Status:          status,
		Summary:         summary,
		MessageCount:    int32(messageCount), //nolint:gosec // count always small
		ErrorMessage:    errMsg,
		Usage:           usage,
		ModelID:         modelID,
		Coverage:        coverage,
		AnchorStartMs:   anchorStartMs,
		AnchorEndMs:     anchorEndMs,
		FailureReason:   failureReason,
		FailureAttempts: int32(failureAttempts), //nolint:gosec // attempts on one span stay small
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to complete compaction log", slog.String("error", err.Error()))
		return err
	}
	return nil
}

func (s *Service) completeRollupLog(
	ctx context.Context,
	logID pgtype.UUID,
	summary string,
	messageCount int,
	usage []byte,
	modelID pgtype.UUID,
	artifact artifactMetadata,
	parents []Artifact,
) error {
	parentIDs := make([]pgtype.UUID, 0, len(parents))
	for _, parent := range parents {
		parentID, err := db.ParseUUID(parent.ID)
		if err != nil {
			return err
		}
		parentIDs = append(parentIDs, parentID)
	}
	_, err := s.queries.CompleteCompactionRollup(ctx, sqlc.CompleteCompactionRollupParams{
		ID:            logID,
		Status:        "ok",
		Summary:       summary,
		MessageCount:  int32(messageCount), //nolint:gosec // count always small
		Usage:         usage,
		ModelID:       modelID,
		Coverage:      artifact.Coverage,
		AnchorStartMs: artifact.AnchorStartMs,
		AnchorEndMs:   artifact.AnchorEndMs,
		Level:         int32(rollupArtifactLevel(parents)), //nolint:gosec // lineage levels are bounded by pass count
		Parents:       parentIDs,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to complete compaction rollup", slog.String("error", err.Error()))
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
