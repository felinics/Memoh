package contextfrag_test

import (
	"encoding/json"
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestLifecycleSnapshotFromMetadata(t *testing.T) {
	t.Parallel()

	snapshot := contextfrag.LifecycleSnapshot{
		Version:         1,
		Counts:          contextfrag.ManifestCounts{Fragments: 3, TokenEstimate: 1200},
		Breakdown:       []contextfrag.KindBreakdown{{Kind: contextfrag.KindConversationEvent, Fragments: 2, TokenEstimate: 900}},
		ToolDefs:        []contextfrag.ToolDefAccounting{{Provider: "mcp", Name: "jira_search", Bytes: 400, TokenEstimate: 100}},
		CacheComparison: &contextfrag.CacheComparison{Outcome: contextfrag.CacheOutcomeHit, PrevAgeMs: 1200, FirstStepCacheReadTokens: 800},
	}
	raw, err := json.Marshal(map[string]any{contextfrag.MetadataContextLifecycleKey: snapshot})
	if err != nil {
		t.Fatal(err)
	}

	got, ok := contextfrag.LifecycleSnapshotFromMetadata(raw)
	if !ok {
		t.Fatal("expected snapshot to parse")
	}
	if got.Counts.TokenEstimate != 1200 || len(got.Breakdown) != 1 || len(got.ToolDefs) != 1 {
		t.Fatalf("parsed snapshot = %+v, want original composition fields", got)
	}
	if got.CacheComparison == nil || got.CacheComparison.Outcome != contextfrag.CacheOutcomeHit ||
		got.CacheComparison.PrevAgeMs != 1200 || got.CacheComparison.FirstStepCacheReadTokens != 800 {
		t.Fatalf("parsed cache comparison = %+v, want legacy-compatible carrier", got.CacheComparison)
	}
	if got.Steps != nil {
		t.Fatalf("parsed omitted steps = %#v, want nil preserved", got.Steps)
	}
}

func TestLifecycleSnapshotFromMetadataAbsent(t *testing.T) {
	t.Parallel()

	if _, ok := contextfrag.LifecycleSnapshotFromMetadata(nil); ok {
		t.Fatal("nil metadata must not parse")
	}
	if _, ok := contextfrag.LifecycleSnapshotFromMetadata([]byte(`{"other":1}`)); ok {
		t.Fatal("metadata without the lifecycle key must not parse")
	}
	if _, ok := contextfrag.LifecycleSnapshotFromMetadata([]byte(`not-json`)); ok {
		t.Fatal("invalid JSON must not parse")
	}
}
