package compaction

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func (s *Service) captureSourceVersions(ctx context.Context, cfg TriggerConfig, items []CompactionCandidate) error {
	var ids []pgtype.UUID
	for _, item := range items {
		if item.Record.ExternalMessageID != "" && item.Record.EventID != "" {
			ids = append(ids, db.ParseUUIDOrEmpty(item.Record.EventID))
		}
	}
	cursors := make(map[string]int64)
	if len(ids) > 0 {
		rows, err := s.queries.ListSessionEventCursors(ctx, sqlc.ListSessionEventCursorsParams{SessionID: db.ParseUUIDOrEmpty(cfg.SessionID), Ids: ids})
		if err != nil {
			return err
		}
		for _, row := range rows {
			cursors[row.ID.String()] = row.EventCursor
		}
	}
	for i, item := range items {
		items[i].EventCursor = cursors[item.Record.EventID]
		for _, source := range cfg.View {
			changed := source.Kind == "history" && source.ID == formatUUID(item.ID) && source.ContentHash != "" && source.ContentHash != historyfrag.MessageContentHash(item.Record.ModelMessage)
			changed = changed || source.Kind == "external" && source.ID == item.Record.ExternalMessageID && (source.EventCursor <= 0 || source.EventCursor != items[i].EventCursor)
			if changed {
				return failed(ReasonStaleSource, fmt.Errorf("%w: source %s differs from the request view", errStaleSource, source.ID))
			}
		}
	}
	return nil
}

func (s *Service) compactionParents(ctx context.Context, cfg TriggerConfig, maxBytes int64) ([]Artifact, error) {
	owner := ArtifactOwner{BotID: cfg.BotID, SessionID: cfg.SessionID, SessionIDKnown: true}
	if cfg.View == nil {
		frontier, err := NewArtifactProjection(s.queries).LoadActiveSession(ctx, owner)
		for _, issue := range frontier.Issues {
			s.logger.WarnContext(ctx, "compaction: ignored invalid artifact lineage", slog.String("issue", issue.Error()))
		}
		return frontier.Artifacts, err
	}
	var parents []Artifact
	seen := make(map[string]bool)
	for _, source := range cfg.View {
		if source.Kind != "summary" || seen[source.ID] {
			continue
		}
		seen[source.ID] = true
		id, err := db.ParseUUID(source.ID)
		if err != nil {
			return nil, failed(ReasonStaleSource, err)
		}
		row, err := s.queries.GetActiveCompactionArtifactWithinBytes(ctx, sqlc.GetActiveCompactionArtifactWithinBytesParams{
			ID: id, BotID: db.ParseUUIDOrEmpty(cfg.BotID), SessionID: db.ParseUUIDOrEmpty(cfg.SessionID), MaxBytes: maxBytes,
		})
		if err != nil {
			return nil, failed(ReasonStaleSource, fmt.Errorf("load rendered artifact %s within %d bytes: %w", source.ID, maxBytes, err))
		}
		artifact, err := artifactFromDBRow(row)
		if err != nil || artifact.CoverageMalformed || !artifactMatchesOwner(artifact, owner) {
			return nil, failed(ReasonStaleSource, fmt.Errorf("invalid rendered artifact %s: %w", source.ID, errStaleSource))
		}
		if len(artifact.Coverage) == 0 {
			return nil, failed(ReasonInputOverflow, fmt.Errorf("%w: artifact_id=%s", errIncompleteAbsorbedCoverage, source.ID))
		}
		// The loader already validated this immutable artifact's lineage when
		// it put it in the request. Re-read its active version, not its ancestors.
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		maxBytes -= int64(len(encoded))
		parents = append(parents, artifact)
	}
	return parents, nil
}
