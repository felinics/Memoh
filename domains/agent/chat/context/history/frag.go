package history

import (
	"strings"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/convert"
)

// ToFrag renders the history record for context-frag manifests. Consumers that
// need provider-continuity details should classify from HistoryRecord.ModelMessage.
func ToFrag(record HistoryRecord) fragment.ContextFrag {
	msg := convert.ModelMessageToSDKMessage(record.ModelMessage)
	kind := record.Kind
	if kind == "" {
		kind = fragment.KindConversationEvent
	}
	provenance := record.Provenance
	if strings.TrimSpace(provenance.Source) == "" {
		provenance.Source = string(record.SourceKind)
	}
	if strings.TrimSpace(provenance.SourceID) == "" {
		provenance.SourceID = strings.TrimSpace(record.Ref.ID)
	}
	if strings.TrimSpace(provenance.Collector) == "" {
		provenance.Collector = CollectorHistoryRecords
	}

	frag := fragment.MessageFrag(fragment.MessageFragInput{
		ID:         fragmentID(record),
		Message:    msg,
		Kind:       kind,
		Slot:       fragment.SlotHistory,
		Priority:   fragment.PriorityForMessage(msg),
		CacheClass: fragment.CacheNever,
		Trust:      trustForHistoryRecord(record),
		Scope:      record.Scope,
		Source:     provenance.Source,
		SourceID:   provenance.SourceID,
		Collector:  provenance.Collector,
		Index:      provenance.Index,
	})
	frag = fragment.WithContextRef(frag, record.Ref)
	frag.Coverage = record.Coverage
	return frag
}

func ToModelMessages(records []HistoryRecord) []agentdomain.ModelMessage {
	out := make([]agentdomain.ModelMessage, 0, len(records))
	for _, record := range records {
		out = append(out, record.ModelMessage)
	}
	return out
}

func ToSDKMessages(records []HistoryRecord) []sdk.Message {
	out := make([]sdk.Message, 0, len(records))
	for _, record := range records {
		out = append(out, convert.ModelMessageToSDKMessage(record.ModelMessage))
	}
	return out
}

func fragmentID(record HistoryRecord) string {
	source := strings.TrimSpace(string(record.SourceKind))
	if source == "" {
		source = "history"
	}
	id := strings.TrimSpace(record.Ref.ID)
	if id == "" {
		id = strings.TrimSpace(record.DBMessageID)
	}
	if id == "" {
		return "history." + source
	}
	return "history." + source + "." + id
}

func trustForHistoryRecord(HistoryRecord) fragment.TrustLevel {
	return fragment.TrustExternal
}
