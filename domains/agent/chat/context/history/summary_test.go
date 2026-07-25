package history

import (
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
)

func TestSummaryRecordIsTypedCoveredAndRenderingEquivalent(t *testing.T) {
	t.Parallel()

	covered := []fragment.ContextRef{
		{Namespace: "bot_history_message", ID: "row-1", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable},
		{Namespace: "bot_history_message", ID: "row-2", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable},
	}

	rec := SummaryRecord("compact-1", "condensed text", covered, fragment.Scope{BotID: "bot-1"})

	if rec.Kind != fragment.KindConversationSummary {
		t.Fatalf("kind = %q, want conversation_summary", rec.Kind)
	}
	if rec.Lifecycle != LifecycleActiveSummary || rec.SourceKind != SourceCompactionLog {
		t.Fatalf("lifecycle/source = %s/%s", rec.Lifecycle, rec.SourceKind)
	}
	if rec.Ref.Namespace != NamespaceCompactionLog || rec.Ref.ID != "compact-1" {
		t.Fatalf("summary ref = %#v", rec.Ref)
	}
	if rec.Coverage == nil {
		t.Fatal("summary record must carry coverage")
	}
	if len(rec.Coverage.CoveredRefs) != 2 || !rec.Coverage.SummaryRef.EqualIdentity(rec.Ref) {
		t.Fatalf("coverage mismatch: %#v", rec.Coverage)
	}

	want := agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("<summary>\ncondensed text\n</summary>")}
	if rec.ModelMessage.Role != want.Role || string(rec.ModelMessage.Content) != string(want.Content) {
		t.Fatalf("summary model message changed: %#v", rec.ModelMessage)
	}
}

func TestSummaryRecordToFragCarriesKindAndCoverage(t *testing.T) {
	t.Parallel()

	covered := []fragment.ContextRef{{Namespace: "bot_history_message", ID: "row-1", Schema: fragment.SchemaContextRef, Durability: fragment.RefDurable}}
	rec := SummaryRecord("compact-1", "condensed", covered, fragment.Scope{BotID: "bot-1"})

	frag := ToFrag(rec)
	if frag.Kind != fragment.KindConversationSummary || frag.Slot != fragment.SlotHistory {
		t.Fatalf("frag kind/slot = %s/%s", frag.Kind, frag.Slot)
	}
	if frag.Coverage == nil || len(frag.Coverage.CoveredRefs) != 1 || frag.Coverage.CoveredRefs[0].ID != "row-1" {
		t.Fatalf("frag coverage mismatch: %#v", frag.Coverage)
	}
	if manifest := fragment.BuildManifest([]fragment.ContextFrag{frag}); len(manifest.CoverageTrace) != 1 {
		t.Fatalf("coverage trace not built: %#v", manifest.CoverageTrace)
	}
}
