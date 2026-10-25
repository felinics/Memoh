package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
	"github.com/felinics/memoh/internal/runtimefence"
)

// minSummaryTokens is the least room a summary must have to be worth
// writing: below it a pass fails before calling the summarizer.
const minSummaryTokens = 32

// doCompaction runs one pass: it folds the earlier summaries and the oldest
// candidates into one summary and commits it. It reports false when there was
// nothing to fold.
func (s *Service) doCompaction(ctx context.Context, botUUID pgtype.UUID, sessionUUID pgtype.UUID, cfg TriggerConfig) (Pass, bool, error) {
	if cfg.HistoryBudgetTokens <= 0 || cfg.SummaryWindowTokens <= 0 {
		return Pass{}, false, failed(ReasonProtectedOverflow, fmt.Errorf("%w: history_budget=%d window=%d", errCompactionUnsized, cfg.HistoryBudgetTokens, cfg.SummaryWindowTokens))
	}
	// The summarizer may write as much as the model declares it can answer;
	// a model that declares nothing gets the output its runs get.
	capability := cfg.MaxOutputTokens
	if capability <= 0 {
		capability = models.ResolveGenerationLimits(models.ClientType(cfg.ClientType), nil, cfg.SummaryWindowTokens, 0).MaxOutputTokens
	}
	promptWindow := cfg.SummaryWindowTokens - estimateBytesAsTokens(systemPrompt) - compactionPromptFramingTokens
	if promptWindow <= 0 {
		return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: window=%d", ErrSummaryWindowTooSmall, cfg.SummaryWindowTokens))
	}

	readMaxBytes := compactionReadMaxBytes(promptWindow)
	parents, err := s.compactionParents(ctx, cfg, readMaxBytes)
	if err != nil {
		return Pass{}, false, err
	}
	createdFrom := pgtype.Timestamptz{Time: cfg.CandidatesFrom, Valid: !cfg.CandidatesFrom.IsZero()}
	createdBefore := pgtype.Timestamptz{Time: cfg.CandidatesBefore, Valid: !cfg.CandidatesBefore.IsZero()}
	viewIDs, viewExternalIDs := viewFilter(cfg)
	measure, err := s.queries.MeasureUncompactedMessagesBySession(ctx, sqlc.MeasureUncompactedMessagesBySessionParams{
		SessionID:          sessionUUID,
		CreatedFrom:        createdFrom,
		CreatedBefore:      createdBefore,
		MessageIds:         viewIDs,
		ExternalMessageIds: viewExternalIDs,
	})
	if err != nil {
		return Pass{}, false, err
	}
	if measure.CandidateCount == 0 && len(parents) == 0 {
		return Pass{}, false, nil
	}
	boundedRows, err := s.queries.ListUncompactedMessagesBySessionWithinBytes(ctx, sqlc.ListUncompactedMessagesBySessionWithinBytesParams{
		SessionID:          sessionUUID,
		CreatedFrom:        createdFrom,
		CreatedBefore:      createdBefore,
		MessageIds:         viewIDs,
		ExternalMessageIds: viewExternalIDs,
		MaxBytes:           readMaxBytes,
	})
	if err != nil {
		return Pass{}, false, err
	}
	rows := uncompactedRowsFromBounded(boundedRows)
	// Use the count captured by the same SQL statement as the payload rows:
	// concurrent claims may change eligibility between the two statements.
	truncatedRead := false
	if len(boundedRows) > 0 {
		truncatedRead = boundedRows[0].CandidateCount > int64(len(rows))
		s.logger.InfoContext(ctx, "compaction: bounded candidate read",
			slog.String("session_id", cfg.SessionID),
			slog.Int("loaded_messages", len(rows)),
			slog.Int64("candidate_count", boundedRows[0].CandidateCount),
			slog.Int64("candidate_bytes", boundedRows[0].CandidateBytes),
			slog.Int64("loaded_bytes", boundedRows[len(boundedRows)-1].CumulativeBytes),
			slog.Int64("read_max_bytes", readMaxBytes),
			slog.Bool("truncated", truncatedRead))
	} else if measure.CandidateCount > 0 {
		return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: largest_candidate_bytes=%d read_max_bytes=%d", errCompactionInputOverflow, measure.LargestCandidateBytes, readMaxBytes))
	}

	messages, barrierCount := candidatesFromRows(rows)
	protectCurrentSources(messages, cfg.ProtectedSources)
	markAnchors(messages, cfg.Anchors)
	if barrierCount > 0 {
		s.logger.WarnContext(ctx, "compaction: kept unparseable history rows as span barriers",
			slog.Int("barrier_count", barrierCount),
			slog.String("session_id", cfg.SessionID),
		)
	}
	var backfills []sqlc.BackfillCompactionArtifactCoverageParams
	if len(parents) > 0 {
		if parents, backfills, err = s.backfillLegacyCoverage(ctx, parents); err != nil {
			return Pass{}, false, err
		}
	}
	if !cfg.CandidatesBefore.IsZero() {
		for _, parent := range parents {
			if parent.AnchorEndMs <= 0 || parent.AnchorEndMs >= cfg.CandidatesBefore.UnixMilli() {
				return Pass{}, false, fmt.Errorf("%w: artifact_id=%s", errSummaryPastCutoff, parent.ID)
			}
		}
	}
	earlier := make([]string, len(parents))
	earlierTokens := 0
	for i, parent := range parents {
		earlier[i] = parent.Summary
		earlierTokens += summaryReplayTokens(parent.Summary)
	}

	// The summary replaces the earlier summaries and the rows it folds; what
	// the request keeps beside it bounds how long it may be.
	room := summaryRoom{HistoryTokens: cfg.HistoryBudgetTokens, KeptTokens: keptReplayTokens(messages, cfg.ProtectedSources)}
	summaryLimit := summaryOutputLimit(room.HistoryTokens-room.KeptTokens, capability)
	if summaryLimit < minSummaryTokens {
		return Pass{}, false, failed(ReasonProtectedOverflow, fmt.Errorf("%w: history_budget=%d kept=%d summary_limit=%d", errNoSummaryRoom, room.HistoryTokens, room.KeptTokens, summaryLimit))
	}
	current := currentRequestEntries(messages, cfg.ProtectedSources, cfg.CurrentRequest)
	readWindow := promptWindow - entriesPromptCost(current)
	if readWindow <= 0 {
		return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: window=%d current_request_tokens=%d", ErrSummaryWindowTooSmall, cfg.SummaryWindowTokens, entriesPromptCost(current)))
	}
	foldable := foldableItems(messages)
	earlierCost := earlierSummariesCost(earlier)
	// The window holds what a pass reads and the summary written from it. A
	// summary is shorter than what it replaces, so reading r tokens may need
	// min(summaryLimit, r) beside it. A later group is read while at least
	// half of that is left, r + min(summaryLimit, r)/2 <= readWindow: a pass
	// gives up part of its summary room rather than leave a group to another
	// pass. No pass progresses without the earlier summaries and the first
	// group, so they need only some room left beside them.
	required := earlierCost + leadingGroupCost(foldable)
	inputTokens := readWindow - max(min(summaryLimit/2, readWindow/3, readWindow-required), 1)

	toCompact := trimCompactMessages(foldable, inputTokens-earlierCost)
	entries, compactedMessageIDs := buildEntriesAndIDs(toCompact)
	if len(compactedMessageIDs) == 0 {
		if _, pending := buildEntriesAndIDs(foldable); len(pending) > 0 {
			return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: earlier_summary_tokens=%d input_tokens=%d", errCompactionInputOverflow, earlierTokens, inputTokens))
		}
		// Nothing new to fold: the summaries are refolded only when their
		// replay is itself what no longer fits the room the request leaves.
		if len(parents) == 0 || earlierTokens <= room.HistoryTokens-room.KeptTokens {
			return Pass{}, false, nil
		}
	}
	entries, keptAfter := keptAfterSummary(entries)
	kept := append(keptInPlace(entries), keptAfter...)
	current = append(keptAfter, current...)
	readTokens := entriesPromptCost(entries) + earlierCost
	if readTokens > inputTokens {
		return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: entries=%d entry_tokens=%d input_tokens=%d", errCompactionInputOverflow, len(entries), readTokens, inputTokens))
	}
	_, remaining := buildEntriesAndIDs(withoutCompacted(foldable, compactedMessageIDs))
	// A summary must be shorter than what it replaces, so the bound the
	// prompt states keeps one that obeys it, as the request renders it,
	// under that too.
	replacedTokens := max(entriesPromptCost(coveredEntries(entries)), compactedReplayTokens(messages, compactedMessageIDs)) + earlierTokens
	maxTokens := min(capability, promptWindow-entriesPromptCost(current)-readTokens)
	room.SummaryTokens = min(summaryLimit, summaryOutputLimit(replacedTokens-1, capability), maxTokens)
	if room.SummaryTokens <= 0 {
		return Pass{}, false, failed(ReasonIneffective, fmt.Errorf("%w: replaced_tokens=%d", errIneffectiveSummary, replacedTokens))
	}
	userPrompt := buildUserPrompt(earlier, entries, current, room)
	summarizerTokens := contextfrag.ProviderEnvelopeTokens(systemPrompt, []sdk.Message{sdk.UserMessage(userPrompt)}, nil)
	if summarizerTokens+maxTokens > cfg.SummaryWindowTokens {
		return Pass{}, false, failed(ReasonInputOverflow, fmt.Errorf("%w: request_tokens=%d max_tokens=%d window=%d", errCompactionInputOverflow, summarizerTokens, maxTokens, cfg.SummaryWindowTokens))
	}
	// A pass that folds everything the request renders may ask for the
	// summary at the end of that request instead.
	var extended *sdk.Request
	if len(remaining) == 0 {
		extended = continuationRequest(cfg, buildContinuationPrompt(kept, toolResultRefs(toCompact), room), room.SummaryTokens, capability)
	}

	if err := ctx.Err(); err != nil {
		return Pass{}, false, err
	}
	var epoch int64
	if len(rows) > 0 {
		epoch = rows[0].CompactionEpoch
	} else {
		session, err := s.queries.GetSessionByID(ctx, sessionUUID)
		if err != nil {
			return Pass{}, false, err
		}
		epoch = session.CompactionEpoch
	}
	persistCtx := context.WithoutCancel(ctx)
	// Claim the exact selected row versions before loading assets. Asset
	// upserts lock the same message row, so either their mutation is visible
	// below or they invalidate this attempt's epoch before it can complete.
	logID, err := s.claim(persistCtx, botUUID, sessionUUID, cfg, epoch, compactedMessageIDs, rows)
	if err != nil {
		return Pass{}, false, err
	}
	fail := func(err error) (Pass, bool, error) {
		_ = s.completeLog(persistCtx, s.queries, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return Pass{}, false, err
	}
	claimed := toCompact
	if len(compactedMessageIDs) > 0 {
		assetRows, err := s.queries.ListMessageAssetsBatch(persistCtx, compactedMessageIDs)
		if err != nil {
			return fail(fmt.Errorf("load compaction message assets: %w", err))
		}
		if claimed, err = candidatesWithAssets(toCompact, rows, assetRows); err != nil {
			return fail(err)
		}
	}
	if err := s.captureSourceVersions(ctx, cfg, claimed); err != nil {
		return fail(err)
	}
	artifact, err := artifactMetadataFor(claimed, compactedMessageIDs)
	if err != nil {
		return fail(err)
	}
	if len(parents) > 0 {
		if artifact, err = rollupArtifactMetadata(parents, artifact); err != nil {
			return fail(err)
		}
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
		systemPrompt, []sdk.Message{sdk.UserMessage(userPrompt)}, nil,
	)

	request := sdk.Request{
		System:    systemPromptDecorated,
		Messages:  sdkMessages,
		MaxTokens: &maxTokens,
	}
	generate := func(request sdk.Request) (sdk.ModelResult, error) {
		// A transient failure gets one immediate retry, or the wait the
		// provider asks for; a rejected request is not made again.
		return modelretry.Do(models.WithModelSession(ctx, cfg.SessionID), s.logger, "agent.compaction",
			modelretry.Config{MaxAttempts: 1, FastAttempts: 1}, modelretry.Retryable,
			func(ctx context.Context) (sdk.ModelResult, error) { return model.Generate(ctx, request) })
	}
	var result sdk.ModelResult
	if extended != nil {
		s.logger.InfoContext(ctx, "compaction: summary asked at the end of the run's request",
			slog.String("session_id", cfg.SessionID),
			slog.Int("request_tokens", contextfrag.ProviderEnvelopeTokens(extended.System, extended.Messages, extended.Tools)),
			slog.Int("summarizer_request_tokens", summarizerTokens))
		result, err = generate(*extended)
		if err == nil && (len(result.ToolCalls) > 0 || result.FinishReason != sdk.FinishReasonStop || strings.TrimSpace(result.Text) == "") {
			err = fmt.Errorf("finish_reason=%s tool_calls=%d", result.FinishReason, len(result.ToolCalls))
		}
		if err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "compaction: extended request gave no summary, asking the summarizer",
				slog.String("session_id", cfg.SessionID), slog.Any("error", err))
			extended = nil
		}
	}
	if extended == nil {
		result, err = generate(request)
	}
	if err != nil {
		return fail(failed(ReasonSummarizerFailed, err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	summary := strings.TrimSpace(result.Text)
	if summary == "" {
		return fail(failed(ReasonEmpty, errEmptySummary))
	}
	if result.FinishReason != sdk.FinishReasonStop {
		return fail(failed(ReasonTruncated, fmt.Errorf("%w: finish_reason=%s", errIncompleteSummary, result.FinishReason)))
	}
	replay := summaryReplayTokens(summary)
	if replay >= replacedTokens {
		return fail(failed(ReasonIneffective, fmt.Errorf("%w: summary_tokens=%d replaced_tokens=%d", errIneffectiveSummary, replay, replacedTokens)))
	}
	if replay > room.HistoryTokens-room.KeptTokens {
		return fail(failed(ReasonStillOver, fmt.Errorf("%w: replay=%d room=%d", errNoSummaryRoom, replay, room.HistoryTokens-room.KeptTokens)))
	}

	usageJSON, _ := json.Marshal(result.Usage)
	modelUUID := db.ParseUUIDOrEmpty(cfg.ModelRecordID)
	if _, err := s.inSession(ctx, cfg, func(q dbstore.Queries) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyClaimedCoverage(ctx, q, logID, claimed, compactedMessageIDs, readMaxBytes); err != nil {
			return err
		}
		for _, backfill := range backfills {
			if err := q.BackfillCompactionArtifactCoverage(ctx, backfill); err != nil {
				return err
			}
		}
		if len(parents) > 0 {
			return s.completeRollupLog(ctx, q, logID, summary, len(compactedMessageIDs), usageJSON, modelUUID, artifact, parents)
		}
		return s.completeLog(ctx, q, logID, "ok", summary, "", len(compactedMessageIDs), usageJSON, modelUUID, &artifact)
	}); err != nil {
		// The rows are already marked, but the log never reached status=ok, so
		// the reclaim SQL keeps them eligible for a later pass. Reporting ok
		// here would claim a summary that was never persisted.
		return fail(staleSource(err))
	}
	pass := Pass{ArtifactID: formatUUID(logID), Summary: summary, More: truncatedRead || len(remaining) > 0}
	for _, item := range toCompact {
		if item.HasPolicy(CompactPolicyAnchor) {
			continue
		}
		pass.Covered = append(pass.Covered, turn.ContextMessageSource{Kind: "history", ID: formatUUID(item.ID)})
		if id := strings.TrimSpace(item.Record.ExternalMessageID); id != "" {
			pass.Covered = append(pass.Covered, turn.ContextMessageSource{Kind: "external", ID: id})
		}
	}
	for _, parent := range parents {
		pass.Absorbed = append(pass.Absorbed, parent.ID)
	}
	return pass, true, nil
}

// viewFilter restricts the candidate read to the rows the caller's request
// renders, or leaves it open when the caller names no view.
func viewFilter(cfg TriggerConfig) ([]pgtype.UUID, []string) {
	if cfg.View == nil {
		return nil, nil
	}
	ids, externalIDs := []pgtype.UUID{}, []string{}
	for _, source := range append(slices.Clone(cfg.View), cfg.Anchors...) {
		switch source.Kind {
		case "history":
			if id, err := db.ParseUUID(source.ID); err == nil {
				ids = append(ids, id)
			}
		case "external":
			externalIDs = append(externalIDs, source.ID)
		}
	}
	return ids, externalIDs
}

// failed names the reason a pass ends the event with.
func failed(reason Reason, err error) error {
	return &EventError{Reason: reason, Err: err}
}

// claim records the attempt and marks the rows it folds, in one fenced
// transaction when the caller holds runtime ownership. A claim another
// writer got first, or a session whose compaction epoch moved, is a stale
// source.
func (s *Service) claim(ctx context.Context, botUUID, sessionUUID pgtype.UUID, cfg TriggerConfig, epoch int64, ids []pgtype.UUID, rows []sqlc.ListUncompactedMessagesBySessionRow) (pgtype.UUID, error) {
	expected, err := expectedCompactionClaims(rows, ids)
	if err != nil {
		return pgtype.UUID{}, err
	}
	var logID pgtype.UUID
	fenced, err := s.inSession(ctx, cfg, func(q dbstore.Queries) error {
		logRow, err := q.CreateCompactionLog(ctx, sqlc.CreateCompactionLogParams{
			BotID:         botUUID,
			SessionID:     sessionUUID,
			ExpectedEpoch: epoch,
		})
		if err != nil {
			return err
		}
		logID = logRow.ID
		if len(ids) == 0 {
			return nil
		}
		marked, err := q.MarkMessagesCompacted(ctx, sqlc.MarkMessagesCompactedParams{
			CompactID:          logID,
			MessageIds:         ids,
			ExpectedCompactIds: expected,
		})
		if err != nil {
			return err
		}
		if marked != int64(len(ids)) {
			return fmt.Errorf("%w: marked %d of %d compaction source rows", errStaleSource, marked, len(ids))
		}
		return nil
	})
	if err != nil {
		if logID.Valid && !fenced {
			_ = s.completeLog(ctx, s.queries, logID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		}
		return pgtype.UUID{}, staleSource(err)
	}
	return logID, nil
}

// inSession runs fn in a transaction fenced by the caller's runtime
// ownership, so a run that lost it cannot claim or commit. A caller without
// ownership writes directly. It reports whether fn ran fenced.
func (s *Service) inSession(ctx context.Context, cfg TriggerConfig, fn func(dbstore.Queries) error) (bool, error) {
	if _, ok := runtimefence.FromContext(ctx); ok {
		return true, runtimefence.InTransaction(ctx, s.queries, cfg.BotID, cfg.SessionID, fn)
	}
	return false, fn(s.queries)
}

// verifyClaimedCoverage checks, at commit, that exactly the rows the attempt
// claimed still carry its claim and the content its summary was written from.
func verifyClaimedCoverage(ctx context.Context, q dbstore.Queries, logID pgtype.UUID, claimed []CompactionCandidate, ids []pgtype.UUID, maxBytes int64) error {
	current, err := q.ListMessagesByCompactIDWithinBytes(ctx, sqlc.ListMessagesByCompactIDWithinBytesParams{CompactID: logID, MaxBytes: maxBytes})
	if err != nil {
		return err
	}
	if len(current) != len(ids) {
		return fmt.Errorf("%w: read back %d of %d claimed rows", errStaleSource, len(current), len(ids))
	}
	if len(current) == 0 {
		return nil
	}
	rows := make([]sqlc.ListUncompactedMessagesBySessionRow, len(current))
	for i, row := range current {
		rows[i] = sqlc.ListUncompactedMessagesBySessionRow(row)
	}
	assetRows, err := q.ListMessageAssetsBatch(ctx, ids)
	if err != nil {
		return err
	}
	items, _ := candidatesFromRows(rows)
	if items, err = candidatesWithAssets(items, rows, assetRows); err != nil {
		return err
	}
	hashes := make(map[pgtype.UUID]string, len(items))
	for _, item := range items {
		hashes[item.ID] = item.Record.Ref.ContentHash
	}
	for _, item := range claimed {
		if item.HasPolicy(CompactPolicyAnchor) {
			continue
		}
		if hash, ok := hashes[item.ID]; !ok || hash != item.Record.Ref.ContentHash {
			return fmt.Errorf("%w: row %s changed since it was claimed", errStaleSource, formatUUID(item.ID))
		}
	}
	return nil
}

// staleSource classifies a write the database refused because the session
// or its rows moved on: lost runtime ownership, a changed compaction epoch,
// or an attempt that is no longer pending.
func staleSource(err error) error {
	if errors.Is(err, errStaleSource) || errors.Is(err, runtimefence.ErrStale) || errors.Is(err, pgx.ErrNoRows) {
		return failed(ReasonStaleSource, err)
	}
	return err
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

// backfillLegacyCoverage reads the coverage of legacy summaries back from
// their rows. The caller persists it only once a summary that supersedes them
// is ready to commit.
func (s *Service) backfillLegacyCoverage(ctx context.Context, parents []Artifact) ([]Artifact, []sqlc.BackfillCompactionArtifactCoverageParams, error) {
	filled := append([]Artifact(nil), parents...)
	var backfills []sqlc.BackfillCompactionArtifactCoverageParams
	for i := range filled {
		if len(filled[i].Coverage) > 0 {
			continue
		}
		rows, err := s.loadAbsorbedRows(ctx, filled[i])
		if err != nil {
			return nil, nil, err
		}
		items, _ := candidatesFromRows(rows)
		if len(items) == 0 {
			return nil, nil, fmt.Errorf("%w: artifact_id=%s", errIncompleteAbsorbedCoverage, filled[i].ID)
		}
		ids := make([]pgtype.UUID, 0, len(items))
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		metadata, err := artifactMetadataFor(items, ids)
		if err != nil {
			return nil, nil, err
		}
		if filled[i].Coverage, err = DecodeArtifactCoverage(metadata.Coverage); err != nil {
			return nil, nil, fmt.Errorf("backfill coverage of artifact %s: %w", filled[i].ID, err)
		}
		id, err := db.ParseUUID(filled[i].ID)
		if err != nil {
			return nil, nil, err
		}
		backfills = append(backfills, sqlc.BackfillCompactionArtifactCoverageParams{
			ID:            id,
			Coverage:      metadata.Coverage,
			AnchorStartMs: metadata.AnchorStartMs,
			AnchorEndMs:   metadata.AnchorEndMs,
		})
		filled[i].AnchorStartMs, filled[i].AnchorEndMs = metadata.AnchorStartMs, metadata.AnchorEndMs
	}
	return filled, backfills, nil
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

func (s *Service) completeLog(ctx context.Context, q dbstore.Queries, logID pgtype.UUID, status, summary, errMsg string, messageCount int, usage []byte, modelID pgtype.UUID, artifact *artifactMetadata) error {
	coverage := []byte("[]")
	var anchorStartMs, anchorEndMs int64
	if artifact != nil {
		coverage = artifact.Coverage
		anchorStartMs = artifact.AnchorStartMs
		anchorEndMs = artifact.AnchorEndMs
	}
	_, err := q.CompleteCompactionLog(ctx, sqlc.CompleteCompactionLogParams{
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

func (s *Service) completeRollupLog(
	ctx context.Context,
	q dbstore.Queries,
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
	_, err := q.CompleteCompactionRollup(ctx, sqlc.CompleteCompactionRollupParams{
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

// continuationRequest appends the summary instruction to the run's last
// request. It preserves the prefix when the model is
// unchanged and the request leaves enough room for the summary.
func continuationRequest(cfg TriggerConfig, prompt string, summaryTokens, capability int) *sdk.Request {
	c := cfg.Continuation
	if c == nil || c.Model != cfg.ModelID {
		return nil
	}
	base := contextfrag.ProviderEnvelopeTokens(c.System, c.Messages, c.Tools)
	ask := contextfrag.ProviderEnvelopeTokens("", []sdk.Message{sdk.UserMessage(prompt)}, nil)
	maxTokens := min(capability, cfg.SummaryWindowTokens-base-ask)
	if maxTokens < summaryTokens {
		return nil
	}
	request := *c
	request.Messages = append(slices.Clip(c.Messages), sdk.UserMessage(prompt))
	request.MaxTokens = &maxTokens
	return &request
}
