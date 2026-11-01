package historyfrag

import (
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/turn"
)

func TestSummaryRecordIsTypedCoveredAndRenderingEquivalent(t *testing.T) {
	t.Parallel()

	rec := SummaryRecord("compact-1", "condensed text", contextfrag.Scope{BotID: "bot-1"})

	if rec.Kind != contextfrag.KindConversationSummary {
		t.Fatalf("kind = %q, want conversation_summary", rec.Kind)
	}
	if rec.Lifecycle != LifecycleActiveSummary || rec.SourceKind != SourceCompactionLog {
		t.Fatalf("lifecycle/source = %s/%s", rec.Lifecycle, rec.SourceKind)
	}
	if rec.Ref.Namespace != NamespaceCompactionLog || rec.Ref.ID != "compact-1" {
		t.Fatalf("summary ref = %#v", rec.Ref)
	}

	want := turn.ModelMessage{Role: "user", Content: turn.NewTextContent(turn.SummaryText("condensed text"))}
	if rec.ModelMessage.Role != want.Role || string(rec.ModelMessage.Content) != string(want.Content) {
		t.Fatalf("summary model message changed: %#v", rec.ModelMessage)
	}
}

func TestSummaryRecordToFragCarriesKindAndCoverage(t *testing.T) {
	t.Parallel()

	rec := SummaryRecord("compact-1", "condensed", contextfrag.Scope{BotID: "bot-1"})

	frag := ToFrag(rec)
	if frag.Kind != contextfrag.KindConversationSummary || frag.Slot != contextfrag.SlotHistory {
		t.Fatalf("frag kind/slot = %s/%s", frag.Kind, frag.Slot)
	}
	if frag.Ref.ID != "compact-1" {
		t.Fatalf("frag ref = %#v, want the summary's", frag.Ref)
	}
}
