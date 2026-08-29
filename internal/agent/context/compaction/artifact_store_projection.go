package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type artifactProjectionQueries interface {
	GetCompactionLogByID(context.Context, pgtype.UUID) (sqlc.BotHistoryMessageCompact, error)
	ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error)
	ListCompactionArtifactParentIDsBySuccessor(context.Context, sqlc.ListCompactionArtifactParentIDsBySuccessorParams) ([]pgtype.UUID, error)
}

type ArtifactProjection struct {
	queries artifactProjectionQueries
}

func NewArtifactProjection(queries artifactProjectionQueries) ArtifactProjection {
	return ArtifactProjection{queries: queries}
}

func (p ArtifactProjection) LoadActiveSession(ctx context.Context, owner ArtifactOwner) (ArtifactFrontier, error) {
	if p.queries == nil {
		return ArtifactFrontier{}, nil
	}
	sessionUUID, err := db.ParseUUID(owner.SessionID)
	if err != nil {
		return ArtifactFrontier{}, err
	}
	rows, err := p.queries.ListCompactionArtifactLineageBySession(ctx, sessionUUID)
	if err != nil {
		return ArtifactFrontier{}, err
	}
	artifacts := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		artifact, err := artifactFromDBRow(row)
		if err != nil {
			return ArtifactFrontier{}, err
		}
		artifacts = append(artifacts, artifact)
	}
	return buildArtifactFrontierForOwner(artifacts, owner), nil
}

func (p ArtifactProjection) LoadActiveByID(ctx context.Context, id string, owner ArtifactOwner) (Artifact, error) {
	if p.queries == nil {
		return Artifact{}, errors.New("compaction artifact projection: queries are required")
	}
	startID := strings.TrimSpace(id)
	if _, err := db.ParseUUID(startID); err != nil {
		return Artifact{}, err
	}
	artifacts, err := p.loadConnectedLineage(ctx, startID)
	if err != nil {
		return Artifact{}, err
	}
	frontier := buildArtifactFrontierForOwner(artifacts, owner)
	if artifact, ok := frontier.Resolve(startID); ok {
		return artifact, nil
	}
	if len(frontier.Issues) > 0 {
		return Artifact{}, &LineageError{Issue: frontier.Issues[0]}
	}
	return Artifact{}, &LineageError{Issue: LineageIssue{Kind: LineageIssueInactiveSuccessor, ArtifactID: startID}}
}

func (p ArtifactProjection) loadConnectedLineage(ctx context.Context, startID string) ([]Artifact, error) {
	queue := []string{startID}
	requested := make(map[string]struct{})
	artifacts := make([]Artifact, 0, 2)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, seen := requested[id]; seen {
			continue
		}
		requested[id] = struct{}{}
		artifactID, err := db.ParseUUID(id)
		if err != nil {
			return nil, err
		}
		row, err := p.queries.GetCompactionLogByID(ctx, artifactID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) && id != startID {
				continue
			}
			return nil, fmt.Errorf("load compaction artifact %s: %w", id, err)
		}
		artifact, err := artifactFromDBRow(row)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
		incoming, err := p.queries.ListCompactionArtifactParentIDsBySuccessor(ctx, sqlc.ListCompactionArtifactParentIDsBySuccessorParams{
			SuccessorID: row.ID,
			BotID:       row.BotID,
			SessionID:   row.SessionID,
		})
		if err != nil {
			return nil, fmt.Errorf("load parents for compaction artifact %s: %w", id, err)
		}
		for _, parentID := range incoming {
			if parent := formatUUID(parentID); parent != "" {
				queue = append(queue, parent)
			}
		}
		if artifact.SupersededBy != "" {
			queue = append(queue, artifact.SupersededBy)
		}
		queue = append(queue, artifact.ParentIDs...)
	}
	return artifacts, nil
}

func artifactFromDBRow(row sqlc.BotHistoryMessageCompact) (Artifact, error) {
	id := formatUUID(row.ID)
	if id == "" {
		return Artifact{}, errors.New("compaction artifact: id is required")
	}
	coverage, coverageErr := DecodeArtifactCoverage(row.Coverage)
	coverageMalformed := coverageErr != nil
	if !coverageMalformed && len(coverage) > 0 {
		coverageMalformed = row.AnchorStartMs != coverage[0].CreatedAtMs ||
			row.AnchorEndMs != coverage[len(coverage)-1].CreatedAtMs
	}
	version := int(row.ArtifactVersion)
	if version == 0 {
		version = ArtifactVersion
	}
	parentIDs := make([]string, 0, len(row.ParentIds))
	for _, parentID := range row.ParentIds {
		if value := formatUUID(parentID); value != "" {
			parentIDs = append(parentIDs, value)
		}
	}
	return Artifact{
		ID:                id,
		BotID:             formatUUID(row.BotID),
		SessionID:         formatUUID(row.SessionID),
		Status:            strings.TrimSpace(row.Status),
		Summary:           row.Summary,
		Version:           version,
		Coverage:          coverage,
		AnchorStartMs:     row.AnchorStartMs,
		AnchorEndMs:       row.AnchorEndMs,
		Level:             int(row.ArtifactLevel),
		ParentIDs:         parentIDs,
		SupersededBy:      formatUUID(row.SupersededBy),
		SupersededAt:      pgTime(row.SupersededAt.Time, row.SupersededAt.Valid),
		StartedAt:         pgTime(row.StartedAt.Time, row.StartedAt.Valid),
		CoverageMalformed: coverageMalformed,
	}, nil
}

func pgTime(value time.Time, valid bool) time.Time {
	if !valid {
		return time.Time{}
	}
	return value.UTC()
}
