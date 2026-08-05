package handlers

import (
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestLatestContextCompositionUsesNewestTurn(t *testing.T) {
	t.Parallel()

	latest := ContextLifecycleTurn{RunID: "r2", Snapshot: contextfrag.LifecycleSnapshot{
		Breakdown: []contextfrag.KindBreakdown{
			{Kind: contextfrag.KindConversationEvent, Fragments: 4, TokenEstimate: 900},
			{Kind: contextfrag.KindSystemPrompt, Fragments: 2, TokenEstimate: 300},
		},
		ToolDefs: []contextfrag.ToolDefAccounting{
			{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
			{Provider: "mcp", Name: "jira_search", Bytes: 1600, TokenEstimate: 400},
			{Provider: "native", Name: "exec", Bytes: 800, TokenEstimate: 200},
		},
	}}
	older := ContextLifecycleTurn{RunID: "r1", Snapshot: contextfrag.LifecycleSnapshot{
		Breakdown: []contextfrag.KindBreakdown{{Kind: contextfrag.KindSystemPrompt, Fragments: 1, TokenEstimate: 1}},
	}}

	breakdown, buckets := latestContextComposition([]ContextLifecycleTurn{latest, older})
	if len(breakdown) != 2 || breakdown[0].Kind != contextfrag.KindConversationEvent {
		t.Fatalf("breakdown = %+v, want latest turn's rows", breakdown)
	}
	want := []ToolDefBucket{
		{Provider: "mcp", Tools: 1, TokenEstimate: 400},
		{Provider: "native", Tools: 2, TokenEstimate: 300},
	}
	if len(buckets) != len(want) {
		t.Fatalf("buckets = %+v, want %+v", buckets, want)
	}
	for i := range want {
		if buckets[i] != want[i] {
			t.Fatalf("buckets[%d] = %+v, want %+v", i, buckets[i], want[i])
		}
	}
}

func TestLatestContextCompositionEmpty(t *testing.T) {
	t.Parallel()

	breakdown, buckets := latestContextComposition(nil)
	if breakdown != nil || buckets != nil {
		t.Fatalf("empty turns must produce nil composition, got %+v %+v", breakdown, buckets)
	}
}
