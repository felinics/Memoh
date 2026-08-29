package handlers

import (
	"math"
	"reflect"
	"testing"

	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
)

func TestAggregateGraphEdgesMergesNodePairs(t *testing.T) {
	edges := aggregateGraphEdges([]migrate.EdgeSpec{
		{SrcNode: "b", DstNode: "a", Rel: migrate.EdgeSameTopic, Weight: 0.8},
		{SrcNode: "a", DstNode: "b", Rel: migrate.EdgeSameDay, Weight: 0.5},
		{SrcNode: "b", DstNode: "a", Rel: migrate.EdgeRefs, Weight: 1.0},
	})

	if len(edges) != 1 {
		t.Fatalf("expected one aggregated edge, got %#v", edges)
	}
	edge := edges[0]
	if edge.Source != "a" || edge.Target != "b" {
		t.Fatalf("expected canonical edge a -> b, got %s -> %s", edge.Source, edge.Target)
	}
	if edge.Rel != string(migrate.EdgeRefs) {
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

func TestDeduplicateMemoryItemsCanonicalizesBotScopedIDs(t *testing.T) {
	items := deduplicateMemoryItems("bot-1", []memprovider.MemoryItem{
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
	items := deduplicateMemoryItems("bot-1", []memprovider.MemoryItem{
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

	nodes, specs, sourceToConcept := buildGraphProjection("bot-1", items)
	if len(nodes) != 2 {
		t.Fatalf("concept nodes = %#v, want 2", nodes)
	}
	byID := map[string]graphNode{}
	for _, node := range nodes {
		byID[node.ID] = node
	}
	alice := byID["alice"]
	if alice.ID == "" {
		t.Fatalf("missing alice concept node: %#v", nodes)
	}
	if alice.Count != 2 {
		t.Fatalf("alice count = %d, want 2", alice.Count)
	}
	if !reflect.DeepEqual(alice.MemoryIDs, []string{"bot-1:mem_alice_1", "bot-1:mem_alice_2"}) {
		t.Fatalf("alice memory ids = %#v", alice.MemoryIDs)
	}
	if sourceToConcept["bot-1:mem_alice_1"] != "alice" || sourceToConcept["bot-1:mem_alice_2"] != "alice" {
		t.Fatalf("sourceToConcept = %#v", sourceToConcept)
	}

	edges := aggregateGraphEdges(projectGraphEdges(migrate.PlanFromNodes(specs), sourceToConcept))
	if len(edges) != 1 {
		t.Fatalf("projected edges = %#v, want one alice-bob edge", edges)
	}
	if edges[0].Source != "alice" || edges[0].Target != "bob" {
		t.Fatalf("edge = %s -> %s, want alice -> bob", edges[0].Source, edges[0].Target)
	}
	if edges[0].Count != 4 {
		t.Fatalf("edge count = %d, want 4 projected source edges", edges[0].Count)
	}
}
