package compaction

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// ForkHistoryCarrier carries a source session's summaries into its fork
// (thread.ForkHistoryCarrier).
type ForkHistoryCarrier struct{}

// CarryForkSummaries makes every summary that holds for a fork's copy an
// active root of the fork over the copies of its rows: the source's active
// summaries when the fork copied its whole visible history, else the ones
// that end before the cutoff, which memoh_rewind_compaction keeps once the
// history from there on is replaced. A copied row is named by its copy and
// the copy's hash, unless the source row changed after the summary covered
// it: then the stale hash stays, and the fork's summary covers the copy as
// little as the source's covers the row. A summary without persisted
// coverage, or with a row the fork did not copy, stays behind, and the
// copies of its rows read raw.
func (ForkHistoryCarrier) CarryForkSummaries(ctx context.Context, queries dbstore.Queries, fork thread.ForkedHistory) error {
	sourceID, err := db.ParseUUID(fork.SourceSessionID)
	if err != nil {
		return err
	}
	forkID, err := db.ParseUUID(fork.ForkSessionID)
	if err != nil {
		return err
	}
	owner := ArtifactOwner{BotID: fork.BotID, SessionID: fork.SourceSessionID, SessionIDKnown: true}
	projection := NewArtifactProjection(queries)
	var frontier ArtifactFrontier
	if fork.Cutoff.IsZero() {
		frontier, err = projection.LoadActiveSession(ctx, owner)
	} else {
		frontier, err = projection.LoadSessionBefore(ctx, owner, fork.Cutoff)
	}
	if err != nil {
		return fmt.Errorf("load source summaries: %w", err)
	}
	copies := make(map[pgtype.UUID]pgtype.UUID, len(fork.SourceMessageIDs))
	for i, id := range fork.SourceMessageIDs {
		if i < len(fork.ForkMessageIDs) {
			copies[id] = fork.ForkMessageIDs[i]
		}
	}
	var sourceIDs, copyIDs []pgtype.UUID
	for _, artifact := range frontier.Artifacts {
		for _, source := range artifact.Coverage {
			id, err := db.ParseUUID(source.Ref.ID)
			if err != nil {
				continue
			}
			sourceIDs = append(sourceIDs, id)
			if copyID, ok := copies[id]; ok {
				copyIDs = append(copyIDs, copyID)
			}
		}
	}
	if len(copyIDs) == 0 {
		return nil
	}
	sourceRecords, err := coverageRecords(ctx, queries, sourceID, sourceIDs)
	if err != nil {
		return err
	}
	copyRecords, err := coverageRecords(ctx, queries, forkID, copyIDs)
	if err != nil {
		return err
	}
	for _, artifact := range frontier.Artifacts {
		covered, claims, ok := forkCoverage(artifact, copies, sourceRecords, copyRecords)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(covered)
		if err != nil {
			return fmt.Errorf("encode fork coverage of summary %s: %w", artifact.ID, err)
		}
		sourceCompactID, err := db.ParseUUID(artifact.ID)
		if err != nil {
			return err
		}
		compactID, err := queries.CreateForkedCompactionArtifact(ctx, sqlc.CreateForkedCompactionArtifactParams{
			MessageCount:    int32(len(claims)), //nolint:gosec // bounded by the copied rows of one session
			Coverage:        encoded,
			AnchorStartMs:   covered[0].CreatedAtMs,
			AnchorEndMs:     covered[len(covered)-1].CreatedAtMs,
			SessionID:       forkID,
			SourceCompactID: sourceCompactID,
		})
		if err != nil {
			return fmt.Errorf("carry summary %s: %w", artifact.ID, err)
		}
		claimed, err := queries.ClaimForkedMessagesForCompact(ctx, sqlc.ClaimForkedMessagesForCompactParams{
			CompactID:  compactID,
			SessionID:  forkID,
			MessageIds: claims,
		})
		if err != nil {
			return fmt.Errorf("claim rows of carried summary %s: %w", artifact.ID, err)
		}
		if claimed != int64(len(claims)) {
			return fmt.Errorf("carried summary %s claimed %d of its %d rows", artifact.ID, claimed, len(claims))
		}
	}
	return nil
}

// forkCoverage is the coverage of artifact named by the fork's copies of its
// rows, and the copies it claims; false when the fork did not copy a row it
// covers.
func forkCoverage(artifact Artifact, copies map[pgtype.UUID]pgtype.UUID, sourceRecords, copyRecords map[pgtype.UUID]historyfrag.HistoryRecord) ([]CoveredSource, []pgtype.UUID, bool) {
	if len(artifact.Coverage) == 0 {
		return nil, nil, false
	}
	covered := make([]CoveredSource, 0, len(artifact.Coverage))
	claims := make([]pgtype.UUID, 0, len(artifact.Coverage))
	for _, source := range artifact.Coverage {
		sourceID, err := db.ParseUUID(source.Ref.ID)
		if err != nil {
			return nil, nil, false
		}
		copyID, ok := copies[sourceID]
		if !ok {
			return nil, nil, false
		}
		copied, ok := copyRecords[copyID]
		if !ok {
			return nil, nil, false
		}
		ref := copied.Ref
		if current, ok := sourceRecords[sourceID]; !ok || current.Ref.ContentHash != source.Ref.ContentHash {
			ref.ContentHash = source.Ref.ContentHash
		}
		source.Ref = ref
		covered = append(covered, source)
		claims = append(claims, copyID)
	}
	return covered, claims, true
}

// coverageRecords loads the session's rows by id as a compaction pass reads
// them, assets included, so their refs carry the hashes coverage compares.
// A row compaction cannot read is left out: no summary covers it.
func coverageRecords(ctx context.Context, queries dbstore.Queries, sessionID pgtype.UUID, ids []pgtype.UUID) (map[pgtype.UUID]historyfrag.HistoryRecord, error) {
	rows, err := queries.ListMessagesByIDsForCoverage(ctx, sqlc.ListMessagesByIDsForCoverageParams{SessionID: sessionID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("load covered rows: %w", err)
	}
	assetRows, err := queries.ListMessageAssetsBatch(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load covered row assets: %w", err)
	}
	assets := assetsByMessageID(assetRows)
	records := make(map[pgtype.UUID]historyfrag.HistoryRecord, len(rows))
	for _, row := range rows {
		candidate := sqlc.ListUncompactedMessagesBySessionRow(row)
		msg := rowToMessage(candidate)
		msg.Assets = assets[row.ID]
		record, err := historyfrag.FromDBMessage(msg, rowScopeFallback(candidate))
		if err != nil {
			continue
		}
		records[row.ID] = record
	}
	return records, nil
}
