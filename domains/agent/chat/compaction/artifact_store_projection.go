package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ArtifactProjection struct {
	store ArtifactStore
}

func NewArtifactProjection(store ArtifactStore) ArtifactProjection {
	return ArtifactProjection{store: store}
}

func (p ArtifactProjection) LoadActiveSession(ctx context.Context, owner ArtifactOwner) (ArtifactFrontier, error) {
	if p.store == nil {
		return ArtifactFrontier{}, nil
	}
	sessionID, err := parseUUID(owner.SessionID)
	if err != nil {
		return ArtifactFrontier{}, err
	}
	rows, err := p.store.ListArtifactsBySession(ctx, sessionID)
	if err != nil {
		return ArtifactFrontier{}, err
	}
	artifacts := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		artifact, err := artifactFromRecord(row)
		if err != nil {
			return ArtifactFrontier{}, err
		}
		artifacts = append(artifacts, artifact)
	}
	return buildArtifactFrontierForOwner(artifacts, owner), nil
}

func (p ArtifactProjection) LoadActiveByID(ctx context.Context, id string, owner ArtifactOwner) (Artifact, error) {
	if p.store == nil {
		return Artifact{}, errors.New("compaction artifact projection: store is required")
	}
	startID := strings.TrimSpace(id)
	if _, err := parseUUID(startID); err != nil {
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
		artifactID, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		row, err := p.store.GetArtifact(ctx, artifactID)
		if err != nil {
			if errors.Is(err, ErrArtifactNotFound) && id != startID {
				continue
			}
			return nil, fmt.Errorf("load compaction artifact %s: %w", id, err)
		}
		artifact, err := artifactFromRecord(row)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
		incoming, err := p.store.ListParentIDs(ctx, ArtifactParentsInput{
			SuccessorID: artifact.ID,
			BotID:       artifact.BotID,
			SessionID:   artifact.SessionID,
		})
		if err != nil {
			return nil, fmt.Errorf("load parents for compaction artifact %s: %w", id, err)
		}
		queue = append(queue, incoming...)
		if artifact.SupersededBy != "" {
			queue = append(queue, artifact.SupersededBy)
		}
		queue = append(queue, artifact.ParentIDs...)
	}
	return artifacts, nil
}

func artifactFromRecord(row ArtifactRecord) (Artifact, error) {
	if row.ID == "" {
		return Artifact{}, errors.New("compaction artifact: id is required")
	}
	coverage, coverageErr := DecodeArtifactCoverage(row.Coverage)
	coverageMalformed := coverageErr != nil
	if !coverageMalformed && len(coverage) > 0 {
		coverageMalformed = row.AnchorStartMs != coverage[0].CreatedAtMs ||
			row.AnchorEndMs != coverage[len(coverage)-1].CreatedAtMs
	}
	version := row.ArtifactVersion
	if version == 0 {
		version = ArtifactVersion
	}
	return Artifact{
		ID:                row.ID,
		BotID:             row.BotID,
		SessionID:         row.SessionID,
		Status:            strings.TrimSpace(row.Status),
		Summary:           row.Summary,
		Version:           version,
		Coverage:          coverage,
		AnchorStartMs:     row.AnchorStartMs,
		AnchorEndMs:       row.AnchorEndMs,
		Level:             row.ArtifactLevel,
		ParentIDs:         append([]string(nil), row.ParentIDs...),
		SupersededBy:      row.SupersededBy,
		SupersededAt:      row.SupersededAt.UTC(),
		StartedAt:         row.StartedAt.UTC(),
		CoverageMalformed: coverageMalformed,
	}, nil
}
