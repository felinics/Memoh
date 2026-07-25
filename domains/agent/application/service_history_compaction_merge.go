package application

import (
	"strings"

	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func mergeMissingCompactionSummaries(messages []history.HistoryRecord, summaries []history.HistoryRecord) []history.HistoryRecord {
	if len(summaries) == 0 {
		return messages
	}
	seen := make(map[string]struct{}, len(messages))
	for _, record := range messages {
		if id := strings.TrimSpace(record.CompactID); id != "" {
			seen[id] = struct{}{}
		}
		if record.SourceKind == history.SourceCompactionLog {
			if id := strings.TrimSpace(record.Ref.ID); id != "" {
				seen[id] = struct{}{}
			}
		}
	}
	missing := make([]history.HistoryRecord, 0, len(summaries))
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
	out := make([]history.HistoryRecord, 0, len(missing)+len(messages))
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

func replaceCompactedHistoryRecords(messages []history.HistoryRecord, summaries map[string]string, scope fragment.Scope) []history.HistoryRecord {
	artifacts := make(map[string]compaction.Artifact, len(summaries))
	for compactID, summary := range summaries {
		artifacts[compactID] = compaction.Artifact{ID: compactID, Summary: summary}
	}
	return replaceCompactedHistoryRecordsWithArtifacts(messages, artifacts, scope)
}

func replaceCompactedHistoryRecordsWithArtifacts(messages []history.HistoryRecord, artifacts map[string]compaction.Artifact, scope fragment.Scope) []history.HistoryRecord {
	return replaceCompactedHistoryRecordsWithService(messages, scope, func(record history.HistoryRecord) (compaction.Artifact, bool) {
		artifact, ok := artifacts[strings.TrimSpace(record.CompactID)]
		return artifact, ok
	})
}

func replaceCompactedHistoryRecordsWithService(
	messages []history.HistoryRecord,
	scope fragment.Scope,
	resolve func(history.HistoryRecord) (compaction.Artifact, bool),
) []history.HistoryRecord {
	type sourceGroupKey struct {
		compactID string
		index     int
	}
	type recordAssignment struct {
		artifactID string
		source     sourceGroupKey
	}
	type artifactGroup struct {
		artifact compaction.Artifact
		indices  []int
	}
	sourceGroupFor := func(record history.HistoryRecord, index int) sourceGroupKey {
		if compactID := strings.TrimSpace(record.CompactID); compactID != "" {
			return sourceGroupKey{compactID: compactID, index: -1}
		}
		return sourceGroupKey{index: index}
	}
	requiredGroups := make(map[sourceGroupKey]struct{})
	for i, record := range messages {
		if record.Required {
			requiredGroups[sourceGroupFor(record, i)] = struct{}{}
		}
	}

	assignments := make([]recordAssignment, len(messages))
	groups := make(map[string]*artifactGroup)
	for i, record := range messages {
		artifact, ok := resolve(record)
		if !ok || artifact.ID == "" || strings.TrimSpace(artifact.Summary) == "" {
			continue
		}
		assignments[i] = recordAssignment{artifactID: artifact.ID, source: sourceGroupFor(record, i)}
		group := groups[artifact.ID]
		if group == nil {
			group = &artifactGroup{artifact: artifact}
			groups[artifact.ID] = group
		}
		group.indices = append(group.indices, i)
	}
	if len(groups) == 0 {
		return messages
	}

	result := make([]history.HistoryRecord, 0, len(messages))
	emitted := make(map[string]struct{}, len(groups))
	for i, record := range messages {
		assignment := assignments[i]
		artifactID := assignment.artifactID
		if artifactID == "" {
			result = append(result, record)
			continue
		}
		group := groups[artifactID]
		if _, seen := emitted[artifactID]; !seen {
			emitted[artifactID] = struct{}{}
			artifact := group.artifact
			if len(artifact.Coverage) == 0 {
				artifact.Coverage = make([]compaction.CoveredSource, 0, len(group.indices))
				for _, index := range group.indices {
					artifact.Coverage = append(artifact.Coverage, compaction.CoveredSource{Ref: messages[index].Ref})
				}
			}
			result = append(result, artifact.HistoryRecord(scope))
		}
		if _, required := requiredGroups[assignment.source]; required {
			result = append(result, record)
		}
	}
	return result
}
