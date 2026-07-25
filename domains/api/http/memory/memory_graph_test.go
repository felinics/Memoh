package memory

import (
	"reflect"
	"testing"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

func TestAggregateGraphEdgesMergesNodePairs(t *testing.T) {
	// Characterization retained at the handler boundary; detailed aggregation
	// coverage lives in domains/memory/graph_test.go.
	graph := memorydomain.BuildGraph("bot-1", []memorydomain.Item{
		{ID: "bot-1:a", Memory: "node a", Metadata: map[string]any{"subject": "A", "topic": "shared"}, CreatedAt: "2026-06-01T00:00:00Z"},
		{ID: "bot-1:b", Memory: "node b", Metadata: map[string]any{"subject": "B", "topic": "shared"}, CreatedAt: "2026-06-01T00:00:00Z"},
	})
	if len(graph.Edges) != 1 {
		t.Fatalf("expected one aggregated edge, got %#v", graph.Edges)
	}
	if graph.Edges[0].Source != "a" || graph.Edges[0].Target != "b" {
		t.Fatalf("expected canonical edge a -> b, got %s -> %s", graph.Edges[0].Source, graph.Edges[0].Target)
	}
	if graph.Edges[0].Count < 2 {
		t.Fatalf("edge count = %d, want at least same_topic+same_day", graph.Edges[0].Count)
	}
}

func TestDeduplicateMemoryItemsCanonicalizesBotScopedIDs(t *testing.T) {
	items := deduplicateMemoryItems("bot-1", []memorydomain.Item{
		{
			ID:     "mem_1",
			Memory: "short",
			Metadata: map[string]any{
				"subject": "Alice",
			},
		},
		{
			ID:     "bot-1:mem_1",
			Memory: "longer canonical memory body",
			Metadata: map[string]any{
				"subject": "Alice",
				"topic":   "profile",
			},
		},
	})

	if len(items) != 1 {
		t.Fatalf("deduplicated items = %d, want 1", len(items))
	}
	if items[0].ID != "bot-1:mem_1" {
		t.Fatalf("id = %q, want bot-1:mem_1", items[0].ID)
	}
	if items[0].Memory != "longer canonical memory body" {
		t.Fatalf("memory = %q", items[0].Memory)
	}
}

func TestGraphProjectionMergesSameConceptNodes(t *testing.T) {
	items := deduplicateMemoryItems("bot-1", []memorydomain.Item{
		{
			ID:     "mem_alice_1",
			Memory: "Alice short duplicate",
			Metadata: map[string]any{
				"subject": "Alice",
				"topic":   "profile",
			},
		},
		{
			ID:     "bot-1:mem_alice_1",
			Memory: "Alice has a detailed profile",
			Metadata: map[string]any{
				"subject": "Alice",
				"topic":   "profile",
			},
		},
		{
			ID:     "bot-1:mem_alice_2",
			Memory: "Alice prefers structured output",
			Metadata: map[string]any{
				"subject": "Alice",
				"topic":   "profile",
			},
		},
		{
			ID:     "bot-1:mem_bob",
			Memory: "Bob shares the profile topic",
			Metadata: map[string]any{
				"subject": "Bob",
				"topic":   "profile",
			},
		},
	})

	graph := memorydomain.BuildGraph("bot-1", items)
	if len(graph.Nodes) != 2 {
		t.Fatalf("concept nodes = %#v, want 2", graph.Nodes)
	}
	byID := map[string]memorydomain.GraphNode{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	alice := byID["alice"]
	if alice.ID == "" {
		t.Fatalf("missing alice concept node: %#v", graph.Nodes)
	}
	if alice.Count != 2 {
		t.Fatalf("alice count = %d, want 2", alice.Count)
	}
	if !reflect.DeepEqual(alice.MemoryIDs, []string{"bot-1:mem_alice_1", "bot-1:mem_alice_2"}) {
		t.Fatalf("alice memory ids = %#v", alice.MemoryIDs)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("projected edges = %#v, want one alice-bob edge", graph.Edges)
	}
	if graph.Edges[0].Source != "alice" || graph.Edges[0].Target != "bob" {
		t.Fatalf("edge = %s -> %s, want alice -> bob", graph.Edges[0].Source, graph.Edges[0].Target)
	}
	if graph.Edges[0].Count != 4 {
		t.Fatalf("edge count = %d, want 4 projected source edges", graph.Edges[0].Count)
	}
}
