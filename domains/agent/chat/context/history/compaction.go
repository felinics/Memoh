package history

import (
	"strings"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
)

const NamespaceCompactionLog = "compaction_log"

func SummaryRecord(compactID string, summary string, coveredRefs []fragment.ContextRef, scope fragment.Scope) HistoryRecord {
	rec := summaryRecordBase(compactID, summary, scope)
	rec.Kind = fragment.KindConversationSummary
	rec.Lifecycle = LifecycleActiveSummary
	coverage := fragment.NewSummaryCoverage(rec.Ref, coveredRefs)
	rec.Coverage = &coverage
	return rec
}

func summaryRecordBase(compactID string, summary string, scope fragment.Scope) HistoryRecord {
	compactID = strings.TrimSpace(compactID)
	return HistoryRecord{
		Ref: fragment.ContextRef{
			Namespace:  NamespaceCompactionLog,
			ID:         compactID,
			Version:    1,
			Schema:     fragment.SchemaContextRef,
			Durability: fragment.RefDurable,
		},
		SourceKind: SourceCompactionLog,
		ModelMessage: agentdomain.ModelMessage{
			Role:    "user",
			Content: agentdomain.NewTextContent("<summary>\n" + summary + "\n</summary>"),
		},
		Scope: scope,
		Provenance: fragment.Provenance{
			Source:    string(SourceCompactionLog),
			SourceID:  compactID,
			Collector: CollectorHistoryRecords,
		},
		CompactID: compactID,
	}
}
