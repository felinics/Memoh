package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/chat/contextepoch"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// defaultSegmentCap is the spec's suggested K: the summary-segment chain may
// hold at most this many segments before the oldest ones merge.
const defaultSegmentCap = 8

func segmentCap(cfg TriggerConfig) int {
	if cfg.SegmentCap > 1 {
		return cfg.SegmentCap
	}
	return defaultSegmentCap
}

// maybeMergeSegments merges the oldest cap summary segments into one when the
// session's segment chain exceeds the cap (Context Epoch spec: 摘要段数超过 K
// 时最旧的几段合并成一段,epoch 加一记 segment-merge). Merging the oldest K
// drops the chain from K+1 back to 2, so full stable-part invalidation stays
// amortized at roughly once per K appended segments.
//
// The merge is bookkeeping-critical: the rollup completion and the epoch
// advance commit in ONE SQL statement (CompleteCompactionRollup). Failures
// only log — the next compaction trigger retries the merge.
func (s *Service) maybeMergeSegments(ctx context.Context, botUUID, sessionUUID pgtype.UUID, cfg TriggerConfig) {
	limit := segmentCap(cfg)
	frontier, err := NewArtifactProjection(s.queries).LoadActiveSession(ctx, ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true})
	if err != nil {
		s.logger.WarnContext(ctx, "segment merge: frontier load failed", slog.String("session_id", cfg.SessionID), slog.Any("error", err))
		return
	}
	if len(frontier.Artifacts) <= limit {
		return
	}
	if s.contextEpochs == nil {
		s.logger.WarnContext(ctx, "segment merge: skipped, no context epoch store (bookkeeping would be lost)",
			slog.String("session_id", cfg.SessionID),
			slog.Int("segments", len(frontier.Artifacts)),
			slog.Int("cap", limit))
		return
	}
	// frontier.Artifacts is ordered oldest first; merge the oldest cap.
	parents := append([]Artifact(nil), frontier.Artifacts[:limit]...)
	if !frontierHasPersistedCoverage(parents) {
		s.logger.WarnContext(ctx, "segment merge: skipped, legacy segment without persisted coverage",
			slog.String("session_id", cfg.SessionID))
		return
	}
	if err := s.mergeSegments(ctx, botUUID, sessionUUID, cfg, parents, coveredMessageIDs(frontier.Artifacts)); err != nil {
		s.logger.WarnContext(ctx, "segment merge: failed",
			slog.String("session_id", cfg.SessionID),
			slog.Int("parents", len(parents)),
			slog.Any("error", err))
	}
}

// mergeSegments runs one merge pass over parents: a summarizer call folds the
// segments into a single replacement segment, then the rollup completion
// supersedes the parents and advances the context epoch atomically.
func (s *Service) mergeSegments(ctx context.Context, botUUID, sessionUUID pgtype.UUID, cfg TriggerConfig, parents []Artifact, coveredIDs map[string]struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.ModelID == "" {
		return errors.New("segment merge: no summarizer model configured")
	}

	baseMaxCompactTokens := cfg.MaxCompactTokens
	if baseMaxCompactTokens <= 0 {
		baseMaxCompactTokens = 30000
	}
	maxOutputTokens := maxCompactionSummaryTokens
	if cfg.SummaryWindowTokens > 0 {
		maxOutputTokens = min(maxCompactionSummaryTokens, max(1, cfg.SummaryWindowTokens/10))
	}
	maxCompactTokens, err := boundedCompactionInputTokens(cfg, baseMaxCompactTokens, maxOutputTokens, fusionSystemPrompt)
	if err != nil {
		return err
	}
	absorbedSegments, _, err := buildAbsorbedContext(ctx, parents, maxCompactTokens, s.loadAbsorbedRows)
	if err != nil {
		return err
	}
	userPrompt := buildFusionUserPrompt(nil, absorbedSegments, nil)
	userPrompt += fmt.Sprintf("\nKeep the summary under %d tokens.\n", maxOutputTokens)

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
		fusionSystemPrompt, []sdk.Message{sdk.UserMessage(userPrompt)}, nil,
	)
	result, err := modelretry.Do(models.WithModelSession(ctx, cfg.SessionID), s.logger, "agent.segment_merge", modelretry.Config{}, modelretry.Retryable,
		func(ctx context.Context) (sdk.ModelResult, error) {
			return model.Generate(ctx, sdk.Request{
				System:    systemPromptDecorated,
				Messages:  sdkMessages,
				MaxTokens: &maxOutputTokens,
			})
		})
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(result.Text)
	if summary == "" {
		return errEmptySummary
	}
	if result.FinishReason != sdk.FinishReasonStop {
		return fmt.Errorf("%w: finish_reason=%s", errIncompleteSummary, result.FinishReason)
	}
	if summaryTokens := estimateSummaryReplayTokens(summary); summaryTokens >= summaryReplacementTokens(0, parents) {
		return fmt.Errorf("%w: summary_tokens=%d", errIneffectiveSummary, summaryTokens)
	}

	artifact, err := rollupArtifactMetadata(parents, artifactMetadata{})
	if err != nil {
		return err
	}

	// Book the merge: epoch+1 with reason segment-merge. The bookmark moves to
	// the merged coverage end when that lies ahead of it; otherwise the epoch
	// advances in place.
	state, err := s.contextEpochs.Load(ctx, cfg.SessionID)
	if err != nil {
		return fmt.Errorf("segment merge: load context epoch: %w", err)
	}
	if state.IsZero() {
		state = contextepoch.Initial(s.nowFn())
	}
	anchorAt := state.AnchorTime()
	anchorID := state.AnchorMessageID
	if artifact.AnchorEndMs > state.AnchorCreatedAtMs {
		anchorAt = time.UnixMilli(artifact.AnchorEndMs).UTC()
		anchorID = lastCoverageMessageID(parents)
		// Never slide the anchor past a visible message no active summary
		// covers: the selector may retain an early raw message (e.g. the
		// window's first user turn) while newer rows compact, and sliding past
		// it would drop it from every later anchored load with no summary to
		// stand in. Clamp the anchor to the earliest such message instead; the
		// anchored window query is inclusive at the anchor timestamp.
		earliest, found, err := s.earliestUncoveredActiveMessage(ctx, sessionUUID, state.AnchorTime(), anchorAt, coveredIDs)
		if err != nil {
			return fmt.Errorf("segment merge: scan uncovered messages: %w", err)
		}
		if found {
			anchorAt = earliest.createdAt
			anchorID = earliest.id
		}
	}
	next, err := state.AdvancedBy(anchorAt, anchorID, contextepoch.ReasonSegmentMerge, s.nowFn())
	if err != nil {
		return fmt.Errorf("segment merge: derive epoch advance: %w", err)
	}
	encoded, err := next.Encode()
	if err != nil {
		return err
	}

	session, err := s.queries.GetSessionByID(ctx, sessionUUID)
	if err != nil {
		return err
	}
	persistCtx := context.WithoutCancel(ctx)
	logRow, err := s.queries.CreateCompactionLog(persistCtx, sqlc.CreateCompactionLogParams{
		BotID:         botUUID,
		SessionID:     sessionUUID,
		ExpectedEpoch: session.CompactionEpoch,
	})
	if err != nil {
		return err
	}
	if err := s.completeMergeLog(persistCtx, logRow.ID, summary, result, cfg, artifact, parents, encoded, next); err != nil {
		_ = s.completeLog(persistCtx, logRow.ID, "error", "", err.Error(), 0, nil, pgtype.UUID{}, nil)
		return err
	}
	s.logger.InfoContext(ctx, "segment merge: completed",
		slog.String("session_id", cfg.SessionID),
		slog.Int("merged_segments", len(parents)),
		slog.Int64("to_epoch", next.Epoch))
	return nil
}

// completeMergeLog completes the merge's compaction log as a rollup of
// parents, carrying the epoch advance into the same statement.
func (s *Service) completeMergeLog(
	ctx context.Context,
	logID pgtype.UUID,
	summary string,
	result sdk.ModelResult,
	cfg TriggerConfig,
	artifact artifactMetadata,
	parents []Artifact,
	epochState []byte,
	next contextepoch.State,
) error {
	parentIDs := make([]pgtype.UUID, 0, len(parents))
	for _, parent := range parents {
		parentID, err := db.ParseUUID(parent.ID)
		if err != nil {
			return err
		}
		parentIDs = append(parentIDs, parentID)
	}
	usageJSON, _ := json.Marshal(result.Usage)
	_, err := s.queries.CompleteCompactionRollup(ctx, sqlc.CompleteCompactionRollupParams{
		ID:                   logID,
		Status:               "ok",
		Summary:              summary,
		MessageCount:         0,
		Usage:                usageJSON,
		ModelID:              db.ParseUUIDOrEmpty(cfg.ModelRecordID),
		Coverage:             artifact.Coverage,
		AnchorStartMs:        artifact.AnchorStartMs,
		AnchorEndMs:          artifact.AnchorEndMs,
		Level:                int32(rollupArtifactLevel(parents)), //nolint:gosec // lineage levels are bounded by pass count
		Parents:              parentIDs,
		ContextEpochState:    epochState,
		ContextEpoch:         next.Epoch,
		ContextEpochAnchorMs: next.AnchorCreatedAtMs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("segment merge: fenced out (concurrent advance or compaction epoch change): %w", contextepoch.ErrAdvanceStale)
		}
		s.logger.ErrorContext(ctx, "segment merge: rollup completion failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}

// lastCoverageMessageID names the newest covered message across parents, for
// the epoch anchor's audit trail.
func lastCoverageMessageID(parents []Artifact) string {
	best := ""
	var bestMs int64
	for _, parent := range parents {
		for _, source := range parent.Coverage {
			if source.CreatedAtMs > bestMs && strings.TrimSpace(source.Ref.ID) != "" {
				bestMs = source.CreatedAtMs
				best = source.Ref.ID
			}
		}
	}
	return best
}

// coveredMessageIDs indexes every persisted history message an active artifact
// covers. The merge's rollup absorbs exactly the parents' coverage, so the
// union over the active frontier is unchanged by the merge and stays the
// authoritative "replaced by a summary" set for the anchor clamp.
func coveredMessageIDs(artifacts []Artifact) map[string]struct{} {
	covered := make(map[string]struct{})
	for _, artifact := range artifacts {
		for _, source := range artifact.Coverage {
			if historyfrag.IsDBMessageRef(source.Ref) && source.Ref.ID != "" {
				covered[source.Ref.ID] = struct{}{}
			}
		}
	}
	return covered
}

type uncoveredActiveMessage struct {
	id        string
	createdAt time.Time
}

// earliestUncoveredActiveMessage finds the oldest active history message in
// (after, until] that no active summary covers. The SQL pre-filters rows a
// compaction claimed (compact_id); the coverage set guards the gap where a
// covered row was never marked.
func (s *Service) earliestUncoveredActiveMessage(
	ctx context.Context,
	sessionUUID pgtype.UUID,
	after, until time.Time,
	coveredIDs map[string]struct{},
) (uncoveredActiveMessage, bool, error) {
	rows, err := s.queries.ListActiveSessionMessageIDsInRange(ctx, sqlc.ListActiveSessionMessageIDsInRangeParams{
		SessionID: sessionUUID,
		After:     pgtype.Timestamptz{Time: after, Valid: true},
		Until:     pgtype.Timestamptz{Time: until, Valid: true},
	})
	if err != nil {
		return uncoveredActiveMessage{}, false, err
	}
	for _, row := range rows {
		if !row.ID.Valid || !row.CreatedAt.Valid {
			continue
		}
		id := formatUUID(row.ID)
		if _, covered := coveredIDs[id]; covered {
			continue
		}
		return uncoveredActiveMessage{id: id, createdAt: row.CreatedAt.Time}, true, nil
	}
	return uncoveredActiveMessage{}, false, nil
}
