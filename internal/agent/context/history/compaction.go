package historyfrag

import (
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

const NamespaceCompactionLog = "compaction_log"

func SummaryRecord(compactID string, summary string, coveredRefs []contextfrag.ContextRef, scope contextfrag.Scope) HistoryRecord {
	rec := summaryRecordBase(compactID, summary, scope)
	rec.Kind = contextfrag.KindConversationSummary
	rec.Lifecycle = LifecycleActiveSummary
	coverage := contextfrag.NewSummaryCoverage(rec.Ref, coveredRefs)
	rec.Coverage = &coverage
	return rec
}

func summaryRecordBase(compactID string, summary string, scope contextfrag.Scope) HistoryRecord {
	compactID = strings.TrimSpace(compactID)
	return HistoryRecord{
		Ref: contextfrag.ContextRef{
			Namespace:  NamespaceCompactionLog,
			ID:         compactID,
			Version:    1,
			Schema:     contextfrag.SchemaContextRef,
			Durability: contextfrag.RefDurable,
		},
		SourceKind: SourceCompactionLog,
		ModelMessage: turn.ModelMessage{
			Role:    "user",
			Content: turn.NewTextContent("<summary>\n" + summary + "\n</summary>"),
		},
		Scope: scope,
		Provenance: contextfrag.Provenance{
			Source:    string(SourceCompactionLog),
			SourceID:  compactID,
			Collector: CollectorHistoryRecords,
		},
		CompactID: compactID,
	}
}
