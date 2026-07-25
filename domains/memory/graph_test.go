package memory

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestAggregateGraphEdgesMergesNodePairs(t *testing.T) {
	edges := aggregateGraphEdges([]EdgeSpec{
		{SrcNode: "b", DstNode: "a", Rel: EdgeSameTopic, Weight: 0.8},
		{SrcNode: "a", DstNode: "b", Rel: EdgeSameDay, Weight: 0.5},
		{SrcNode: "b", DstNode: "a", Rel: EdgeRefs, Weight: 1.0},
	})

	if len(edges) != 1 {
		t.Fatalf("expected one aggregated edge, got %#v", edges)
	}
	edge := edges[0]
	if edge.Source != "a" || edge.Target != "b" {
		t.Fatalf("expected canonical edge a -> b, got %s -> %s", edge.Source, edge.Target)
	}
	if edge.Rel != string(EdgeRefs) {
		t.Fatalf("primary rel = %q, want refs", edge.Rel)
	}
	if !reflect.DeepEqual(edge.Rels, []string{"refs", "same_topic", "same_day"}) {
		t.Fatalf("rels = %#v", edge.Rels)
	}
	if edge.Count != 3 {
		t.Fatalf("count = %d, want 3", edge.Count)
	}
	if math.Abs(edge.Weight-2.3) > 0.0001 {
		t.Fatalf("weight = %f, want 2.3", edge.Weight)
	}
}

func TestBuildGraphMergesSameConceptNodes(t *testing.T) {
	// Caller is expected to dedupe bot-scoped ID aliases before BuildGraph
	// (handlers.deduplicateMemoryItems); this fixture uses canonical IDs.
	items := []Item{
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
	}

	graph := BuildGraph("bot-1", items)
	if len(graph.Nodes) != 2 {
		t.Fatalf("concept nodes = %#v, want 2", graph.Nodes)
	}
	byID := map[string]GraphNode{}
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

func TestItemJSONTagsStable(t *testing.T) {
	// The unique Item DTO must keep the wire field names used by
	// handlers/adapters/storefs. Assert the marshalled keys, not just that the
	// struct can be populated: a test that only reads two fields back would
	// pass even after every json tag was renamed.
	item := Item{
		ID: "bot-1:mem_1", Memory: "x", Hash: "h", CreatedAt: "t1", UpdatedAt: "t2",
		Score: 1.5, BotID: "bot-1", AgentID: "a", RunID: "r",
		Metadata: map[string]any{"k": "v"},
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal item: %v", err)
	}
	want := map[string]any{
		"id":         "bot-1:mem_1",
		"memory":     "x",
		"hash":       "h",
		"created_at": "t1",
		"updated_at": "t2",
		"score":      1.5,
		"metadata":   map[string]any{"k": "v"},
		"bot_id":     "bot-1",
		"agent_id":   "a",
		"run_id":     "r",
	}
	if !reflect.DeepEqual(wire, want) {
		t.Fatalf("item wire shape = %#v, want %#v", wire, want)
	}

	// Every optional field is omitempty, so a zero Item must carry only the
	// two required keys.
	raw, err = json.Marshal(Item{})
	if err != nil {
		t.Fatalf("marshal zero item: %v", err)
	}
	if got := string(raw); got != `{"id":"","memory":""}` {
		t.Fatalf("zero item wire shape = %s, want {\"id\":\"\",\"memory\":\"\"}", got)
	}
}
