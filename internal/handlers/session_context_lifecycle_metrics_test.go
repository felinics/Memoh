package handlers

import (
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func metricTurn(runID string, outcome string, readTokens, expected int, toolDefs []contextfrag.ToolDefAccounting) ContextLifecycleTurn {
	snapshot := contextfrag.LifecycleSnapshot{
		CacheReadTokens:           readTokens,
		StablePrefixTokenEstimate: expected,
		ToolDefs:                  toolDefs,
	}
	if outcome != "" {
		snapshot.CacheComparison = &contextfrag.CacheComparison{Outcome: outcome}
	}
	return ContextLifecycleTurn{RunID: runID, Snapshot: snapshot}
}

func TestAggregateContextLifecycleCacheEfficiency(t *testing.T) {
	t.Parallel()

	turns := []ContextLifecycleTurn{
		metricTurn("m3", contextfrag.CacheOutcomeHit, 8000, 40000, nil),
		metricTurn("m2", contextfrag.CacheOutcomeHit, 32000, 40000, nil),
		metricTurn("m1", contextfrag.CacheOutcomeFirstObservation, 0, 40000, nil),
		metricTurn("unknown", "", 9000, 90000, nil),
	}
	agg := aggregateContextLifecycle(turns)
	if agg.TotalExpectedStableTokens != 80000 {
		t.Fatalf("expected stable tokens = %d, want 80000 (first and unknown observations excluded)", agg.TotalExpectedStableTokens)
	}
	if agg.CacheReadEfficiency != 50 {
		t.Fatalf("cache read efficiency = %.1f, want 50.0", agg.CacheReadEfficiency)
	}
}

func TestAggregateContextLifecycleToolRosterChurn(t *testing.T) {
	t.Parallel()

	oldDefs := []contextfrag.ToolDefAccounting{
		{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
		{Provider: "mcp", Name: "jira_search", Bytes: 800, TokenEstimate: 200},
	}
	resized := []contextfrag.ToolDefAccounting{
		{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
		{Provider: "mcp", Name: "jira_search", Bytes: 804, TokenEstimate: 201},
	}
	swapped := []contextfrag.ToolDefAccounting{
		{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100},
		{Provider: "mcp", Name: "confluence_read", Bytes: 900, TokenEstimate: 225},
	}
	turns := []ContextLifecycleTurn{
		metricTurn("m3", "", 0, 0, swapped),
		metricTurn("m2", "", 0, 0, resized),
		metricTurn("m1", "", 0, 0, oldDefs),
	}
	agg := aggregateContextLifecycle(turns)
	if agg.ToolRosterChanges != 2 || len(agg.ToolRosterChangeDetails) != 2 {
		t.Fatalf("roster aggregate = %+v, want 2 changes", agg)
	}
	newest := agg.ToolRosterChangeDetails[0]
	if newest.RunID != "m3" || len(newest.Added) != 1 || newest.Added[0] != "mcp/confluence_read" || len(newest.Removed) != 1 {
		t.Fatalf("newest change = %+v, want jira_search swapped for confluence_read", newest)
	}
	older := agg.ToolRosterChangeDetails[1]
	if older.RunID != "m2" || len(older.Resized) != 1 || older.Resized[0] != "mcp/jira_search" {
		t.Fatalf("older change = %+v, want jira_search resized", older)
	}
}

func TestAggregateContextLifecycleRosterIgnoresMissingSides(t *testing.T) {
	t.Parallel()

	turns := []ContextLifecycleTurn{
		metricTurn("m2", "", 0, 0, []contextfrag.ToolDefAccounting{{Provider: "native", Name: "send_message", Bytes: 400, TokenEstimate: 100}}),
		metricTurn("m1", "", 0, 0, nil),
	}
	if agg := aggregateContextLifecycle(turns); agg.ToolRosterChanges != 0 {
		t.Fatalf("roster changes = %d, want 0 when a side has no accounting", agg.ToolRosterChanges)
	}
}
