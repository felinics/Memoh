package contextfrag

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestNewSummaryCoverageCarriesRefsAndSchema(t *testing.T) {
	t.Parallel()

	summaryRef := ContextRef{Namespace: "compaction_log", ID: "log-1", Schema: SchemaContextRef, Durability: RefDurable}
	covered := []ContextRef{
		{Namespace: "bot_history_message", ID: "row-1", Schema: SchemaContextRef, Durability: RefDurable},
		{Namespace: "bot_history_message", ID: "row-2", Schema: SchemaContextRef, Durability: RefDurable},
	}

	cov := NewSummaryCoverage(summaryRef, covered)

	if cov.CoverageID != "coverage:"+summaryRef.StableKey() {
		t.Fatalf("coverage id = %q, want it derived from summary ref StableKey", cov.CoverageID)
	}
	if !cov.SummaryRef.EqualIdentity(summaryRef) {
		t.Fatalf("summary ref mismatch: %#v", cov.SummaryRef)
	}
	if len(cov.CoveredRefs) != 2 {
		t.Fatalf("covered refs = %d, want 2", len(cov.CoveredRefs))
	}
	if cov.Schema.Name != SchemaSummaryCoverage || cov.Schema.Version != CurrentSchemaVersion {
		t.Fatalf("coverage schema = %#v", cov.Schema)
	}
}

func TestBuildManifestCollectsSummaryCoverageTrace(t *testing.T) {
	t.Parallel()

	summaryRef := ContextRef{Namespace: "compaction_log", ID: "log-1", Schema: SchemaContextRef, Durability: RefDurable}
	covered := []ContextRef{{Namespace: "bot_history_message", ID: "row-1", Schema: SchemaContextRef, Durability: RefDurable}}
	cov := NewSummaryCoverage(summaryRef, covered)
	summaryFrag := MessageFrag(MessageFragInput{
		ID:      "history.compaction_log.log-1",
		Message: sdk.UserMessage("<summary>\ncondensed\n</summary>"),
		Kind:    KindConversationSummary,
		Slot:    SlotHistory,
		Scope:   Scope{BotID: "bot-1"},
		Source:  "compaction_log",
	})
	summaryFrag.Coverage = &cov

	manifest := BuildManifest([]ContextFrag{summaryFrag})

	if len(manifest.CoverageTrace) != 1 {
		t.Fatalf("coverage trace = %d, want 1", len(manifest.CoverageTrace))
	}
	got := manifest.CoverageTrace[0]
	if !got.SummaryRef.EqualIdentity(summaryRef) {
		t.Fatalf("coverage summary ref mismatch: %#v", got.SummaryRef)
	}
	if len(got.CoveredRefs) != 1 || got.CoveredRefs[0].ID != "row-1" {
		t.Fatalf("coverage covered refs mismatch: %#v", got.CoveredRefs)
	}
	if len(manifest.Items) != 1 || manifest.Items[0].Kind != KindConversationSummary {
		t.Fatalf("summary manifest item not retained: %#v", manifest.Items)
	}
}

func TestCompilePreservesExplicitSummaryMessageFragWithoutDuplication(t *testing.T) {
	t.Parallel()

	summaryRef := ContextRef{Namespace: "compaction_log", ID: "log-1", Schema: SchemaContextRef, Durability: RefDurable}
	covered := []ContextRef{{Namespace: "bot_history_message", ID: "row-1", Schema: SchemaContextRef, Durability: RefDurable}}
	cov := NewSummaryCoverage(summaryRef, covered)
	msg := sdk.UserMessage("<summary>\ncondensed\n</summary>")
	summaryFrag := MessageFrag(MessageFragInput{
		ID:      "message.000",
		Message: msg,
		Kind:    KindConversationSummary,
		Slot:    SlotHistory,
		Source:  "compaction_log",
	})
	summaryFrag.Ref = summaryRef
	summaryFrag.Coverage = &cov

	assembled := Compile(CompileInput{
		Source:   SourceRunConfig,
		Messages: []sdk.Message{msg},
		Existing: []ContextFrag{summaryFrag},
	})

	if len(assembled.Messages) != 1 {
		t.Fatalf("rendered messages = %d, want 1 summary message", len(assembled.Messages))
	}
	if len(assembled.Manifest.Items) != 1 {
		t.Fatalf("manifest items = %d, want 1 explicit summary item: %#v", len(assembled.Manifest.Items), assembled.Manifest.Items)
	}
	if assembled.Manifest.Items[0].Kind != KindConversationSummary {
		t.Fatalf("summary item kind = %q", assembled.Manifest.Items[0].Kind)
	}
	if len(assembled.Manifest.CoverageTrace) != 1 || len(assembled.Manifest.CoverageTrace[0].CoveredRefs) != 1 {
		t.Fatalf("coverage trace missing: %#v", assembled.Manifest.CoverageTrace)
	}
}
