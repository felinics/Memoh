package application

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/turn"
)

func mergeMissingCompactionSummaries(messages []historyfrag.HistoryRecord, summaries []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(summaries) == 0 {
		return messages
	}
	seen := make(map[string]struct{}, len(messages))
	for _, record := range messages {
		if id := strings.TrimSpace(record.CompactID); id != "" {
			seen[id] = struct{}{}
		}
		if record.SourceKind == historyfrag.SourceCompactionLog {
			if id := strings.TrimSpace(record.Ref.ID); id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	missing := make([]historyfrag.HistoryRecord, 0, len(summaries))
	for _, summary := range summaries {
		id := strings.TrimSpace(summary.CompactID)
		if id == "" {
			id = strings.TrimSpace(summary.Ref.ID)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		missing = append(missing, summary)
	}
	if len(missing) == 0 {
		return messages
	}
	// Merge each aged-out summary at its anchor position instead of batching
	// the whole set at the front, mirroring the pipeline path's anchor-ordered
	// composition. A summary never lands directly before a tool-result record:
	// results follow their call contiguously, so flushing there would split
	// the exchange.
	out := make([]historyfrag.HistoryRecord, 0, len(missing)+len(messages))
	next := 0
	for i, record := range messages {
		if !strings.EqualFold(strings.TrimSpace(record.ModelMessage.Role), "tool") {
			for next < len(missing) && !missing[next].CreatedAt.After(record.CreatedAt) {
				out = append(out, missing[next])
				next++
			}
		}
		out = append(out, messages[i])
	}
	out = append(out, missing[next:]...)
	return out
}

func pgUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	parsed, err := uuid.FromBytes(value.Bytes[:])
	if err != nil {
		return ""
	}
	return parsed.String()
}

func replaceCompactedHistoryRecords(messages []historyfrag.HistoryRecord, summaries map[string]string, scope contextfrag.Scope) []historyfrag.HistoryRecord {
	artifacts := make(map[string]compaction.Artifact, len(summaries))
	for compactID, summary := range summaries {
		artifacts[compactID] = compaction.Artifact{ID: compactID, Summary: summary}
	}
	return replaceCompactedHistoryRecordsWithArtifacts(messages, artifacts, scope)
}

func replaceCompactedHistoryRecordsWithArtifacts(messages []historyfrag.HistoryRecord, artifacts map[string]compaction.Artifact, scope contextfrag.Scope) []historyfrag.HistoryRecord {
	return replaceCompactedHistoryRecordsWithService(messages, scope, func(record historyfrag.HistoryRecord) (compaction.Artifact, bool) {
		artifact, ok := artifacts[strings.TrimSpace(record.CompactID)]
		return artifact, ok
	})
}

// replaceCompactedHistoryRecordsWithService renders each resolved artifact in
// place of the records it covers, placed by turn.PlaceSummary. A required
// record stays raw beside its summary; an artifact that covers only required
// records renders right before the first of them.
func replaceCompactedHistoryRecordsWithService(
	messages []historyfrag.HistoryRecord,
	scope contextfrag.Scope,
	resolve func(historyfrag.HistoryRecord) (compaction.Artifact, bool),
) []historyfrag.HistoryRecord {
	type placedRecord struct {
		record     historyfrag.HistoryRecord
		artifactID string
		removed    bool
	}
	sourceGroupFor := func(record historyfrag.HistoryRecord, index int) string {
		if compactID := strings.TrimSpace(record.CompactID); compactID != "" {
			return compactID
		}
		return fmt.Sprintf("#%d", index)
	}
	requiredGroups := make(map[string]struct{})
	for i, record := range messages {
		if record.Required {
			requiredGroups[sourceGroupFor(record, i)] = struct{}{}
		}
	}

	items := make([]placedRecord, len(messages))
	artifacts := make(map[string]compaction.Artifact)
	var order []string
	for i, record := range messages {
		items[i].record = record
		artifact, ok := resolve(record)
		if !ok || artifact.ID == "" || strings.TrimSpace(artifact.Summary) == "" {
			continue
		}
		_, required := requiredGroups[sourceGroupFor(record, i)]
		items[i].artifactID, items[i].removed = artifact.ID, !required
		if _, seen := artifacts[artifact.ID]; !seen {
			order = append(order, artifact.ID)
			artifacts[artifact.ID] = artifact
		}
	}
	if len(order) == 0 {
		return messages
	}
	for _, id := range order {
		artifact := artifacts[id]
		if len(artifact.Coverage) == 0 {
			for _, item := range items {
				if item.artifactID == id {
					artifact.Coverage = append(artifact.Coverage, compaction.CoveredSource{Ref: item.record.Ref})
				}
			}
		}
		summary := placedRecord{record: artifact.HistoryRecord(scope)}
		placed := false
		items, placed = turn.PlaceSummary(items, summary, func(item placedRecord) bool { return item.removed && item.artifactID == id })
		if !placed {
			first := slices.IndexFunc(items, func(item placedRecord) bool { return item.artifactID == id })
			items = slices.Insert(items, first, summary)
		}
	}
	result := make([]historyfrag.HistoryRecord, len(items))
	for i, item := range items {
		result[i] = item.record
	}
	return result
}
