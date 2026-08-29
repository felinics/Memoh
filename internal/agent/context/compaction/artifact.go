package compaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

const ArtifactVersion = 1

type CoveredSource struct {
	Ref                    contextfrag.ContextRef `json:"ref"`
	ExternalMessageID      string                 `json:"external_message_id,omitempty"`
	SourceReplyToMessageID string                 `json:"source_reply_to_message_id,omitempty"`
	CreatedAtMs            int64                  `json:"created_at_ms,omitempty"`
}

type Artifact struct {
	ID                string
	BotID             string
	SessionID         string
	Status            string
	Summary           string
	Version           int
	Coverage          []CoveredSource
	AnchorStartMs     int64
	AnchorEndMs       int64
	Level             int
	ParentIDs         []string
	SupersededBy      string
	SupersededAt      time.Time
	StartedAt         time.Time
	CoverageMalformed bool
}

func (a Artifact) HistoryRecord(scope contextfrag.Scope) historyfrag.HistoryRecord {
	if scope.BotID == "" {
		scope.BotID = a.BotID
	}
	if scope.SessionID == "" {
		scope.SessionID = a.SessionID
	}
	coveredRefs := make([]contextfrag.ContextRef, 0, len(a.Coverage))
	for _, source := range a.Coverage {
		coveredRefs = append(coveredRefs, source.Ref)
	}
	record := historyfrag.SummaryRecord(a.ID, a.Summary, coveredRefs, scope)
	record.BotID = a.BotID
	record.SessionID = a.SessionID
	record.SessionIDKnown = true
	if a.AnchorStartMs > 0 {
		record.CreatedAt = time.UnixMilli(a.AnchorStartMs).UTC()
	}
	return record
}

func (a Artifact) CoversRecord(record historyfrag.HistoryRecord) bool {
	createdAtMs := int64(0)
	if !record.CreatedAt.IsZero() {
		createdAtMs = record.CreatedAt.UnixMilli()
	}
	for _, source := range a.Coverage {
		if compatibleCoverageRef(record.Ref, source.Ref) &&
			strings.TrimSpace(source.ExternalMessageID) == strings.TrimSpace(record.ExternalMessageID) &&
			strings.TrimSpace(source.SourceReplyToMessageID) == strings.TrimSpace(record.SourceReplyToMessageID) &&
			source.CreatedAtMs == createdAtMs {
			return true
		}
	}
	return false
}

type artifactMetadata struct {
	Coverage      []byte
	AnchorStartMs int64
	AnchorEndMs   int64
}

func artifactMetadataFor(items []CompactionCandidate, ids []pgtype.UUID) (artifactMetadata, error) {
	byID := make(map[pgtype.UUID]CompactionCandidate, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	covered := make([]CoveredSource, 0, len(ids))
	for _, id := range ids {
		item, ok := byID[id]
		if !ok {
			return artifactMetadata{}, fmt.Errorf("compaction artifact: marked id %s missing from candidates", formatUUID(id))
		}
		createdAtMs := int64(0)
		if !item.Record.CreatedAt.IsZero() {
			createdAtMs = item.Record.CreatedAt.UnixMilli()
		}
		covered = append(covered, CoveredSource{
			Ref:                    item.Record.Ref,
			ExternalMessageID:      item.Record.ExternalMessageID,
			SourceReplyToMessageID: item.Record.SourceReplyToMessageID,
			CreatedAtMs:            createdAtMs,
		})
	}
	sort.SliceStable(covered, func(i, j int) bool {
		return covered[i].CreatedAtMs < covered[j].CreatedAtMs
	})
	encoded, err := json.Marshal(covered)
	if err != nil {
		return artifactMetadata{}, fmt.Errorf("encode compaction artifact coverage: %w", err)
	}
	metadata := artifactMetadata{Coverage: encoded}
	if len(covered) > 0 {
		metadata.AnchorStartMs = covered[0].CreatedAtMs
		metadata.AnchorEndMs = covered[len(covered)-1].CreatedAtMs
	}
	return metadata, nil
}

func rollupArtifactMetadata(parents []Artifact, current artifactMetadata) (artifactMetadata, error) {
	currentCoverage, err := DecodeArtifactCoverage(current.Coverage)
	if err != nil {
		return artifactMetadata{}, err
	}
	covered := make([]CoveredSource, 0, len(currentCoverage))
	for _, parent := range parents {
		if len(parent.Coverage) == 0 {
			return artifactMetadata{}, fmt.Errorf("%w: artifact_id=%s", errIncompleteAbsorbedCoverage, parent.ID)
		}
		covered = append(covered, parent.Coverage...)
	}
	covered = append(covered, currentCoverage...)
	sort.SliceStable(covered, func(i, j int) bool {
		return covered[i].CreatedAtMs < covered[j].CreatedAtMs
	})
	if err := validatePersistedArtifactCoverage(covered); err != nil {
		return artifactMetadata{}, fmt.Errorf("validate compaction rollup coverage: %w", err)
	}
	encoded, err := json.Marshal(covered)
	if err != nil {
		return artifactMetadata{}, fmt.Errorf("encode compaction rollup coverage: %w", err)
	}
	metadata := artifactMetadata{Coverage: encoded}
	if len(covered) > 0 {
		metadata.AnchorStartMs = covered[0].CreatedAtMs
		metadata.AnchorEndMs = covered[len(covered)-1].CreatedAtMs
	}
	return metadata, nil
}

func DecodeArtifactCoverage(raw []byte) ([]CoveredSource, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var covered []CoveredSource
	if err := json.Unmarshal(raw, &covered); err != nil {
		return nil, fmt.Errorf("decode compaction artifact coverage: %w", err)
	}
	if err := validatePersistedArtifactCoverage(covered); err != nil {
		return nil, fmt.Errorf("decode compaction artifact coverage: %w", err)
	}
	return covered, nil
}

func validatePersistedArtifactCoverage(covered []CoveredSource) error {
	seen := make(map[string]struct{}, len(covered))
	for i, source := range covered {
		if err := validatePersistedCoverageRef(source.Ref); err != nil {
			return fmt.Errorf("ref %d: %w", i, err)
		}
		key := source.Ref.StableKey()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("ref %d: duplicate stable key %q", i, key)
		}
		seen[key] = struct{}{}
		if i > 0 && source.CreatedAtMs < covered[i-1].CreatedAtMs {
			return fmt.Errorf(
				"ref %d: created_at_ms %d precedes ref %d created_at_ms %d",
				i,
				source.CreatedAtMs,
				i-1,
				covered[i-1].CreatedAtMs,
			)
		}
	}
	return nil
}

func validatePersistedCoverageRef(ref contextfrag.ContextRef) error {
	if err := contextfrag.ValidateContextRef(ref); err != nil {
		return err
	}
	if ref.HashAlgo != contextfrag.HashAlgoSHA256 {
		return fmt.Errorf("hash algo must be %q", contextfrag.HashAlgoSHA256)
	}
	if ref.HashScope != contextfrag.HashScopeSourcePayload {
		return fmt.Errorf("hash scope must be %q", contextfrag.HashScopeSourcePayload)
	}
	if strings.TrimSpace(ref.ContentHash) == "" {
		return errors.New("content hash is required")
	}
	return nil
}
