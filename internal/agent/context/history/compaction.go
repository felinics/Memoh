package historyfrag

import (
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

const NamespaceCompactionLog = "compaction_log"

// SummaryRecord is the record a summary of a history checkpoint renders as.
// Its coverage names the summary and no rows: what the summary stands for is
// its checkpoint's to say.
func SummaryRecord(compactID string, summary string, scope contextfrag.Scope) HistoryRecord {
	compactID = strings.TrimSpace(compactID)
	rec := HistoryRecord{
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
			Content: turn.NewTextContent(turn.SummaryText(summary)),
		},
		Scope: scope,
		Provenance: contextfrag.Provenance{
			Source:    string(SourceCompactionLog),
			SourceID:  compactID,
			Collector: CollectorHistoryRecords,
		},
		Kind:      contextfrag.KindConversationSummary,
		Lifecycle: LifecycleActiveSummary,
	}
	return rec
}
