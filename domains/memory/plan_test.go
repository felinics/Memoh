package memory

import (
	"testing"
	"time"
)

func TestPlanFromItemsClassifiesLayersAndEdges(t *testing.T) {
	items := []Item{
		{
			ID:        "bot-1:mem_1",
			Memory:    "User prefers oolong tea",
			Hash:      "h1",
			CreatedAt: "2026-06-01T10:00:00Z",
			Metadata:  map[string]any{"layer": "preference", "profile_ref": "user:42", "topic": "drinks", "confidence": 0.9},
		},
		{
			ID:        "bot-1:mem_2",
			Memory:    "User lives in Berlin",
			Hash:      "h2",
			CreatedAt: "2026-06-01T12:00:00Z",
			Metadata:  map[string]any{"profile_ref": "user:42", "topic": "location"},
		},
		{
			ID:        "bot-1:mem_3",
			Memory:    "Met about the API project",
			Hash:      "h3",
			CreatedAt: "2026-06-02T09:00:00Z",
			Metadata:  map[string]any{"topic": "work"},
		},
		{
			ID:        "bot-1:mem_4",
			Memory:    "Unrelated note",
			Hash:      "h4",
			CreatedAt: "2026-06-03T09:00:00Z",
		},
	}

	nodes, edges := PlanFromItems("bot-1", items)

	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}

	wantLayer := map[string]Layer{
		"bot-1:mem_1": LayerPreference,
		"bot-1:mem_2": LayerNote,
		"bot-1:mem_3": LayerNote,
		"bot-1:mem_4": LayerNote,
	}
	for _, n := range nodes {
		if got, want := n.Layer, wantLayer[n.ID]; got != want {
			t.Fatalf("node %s layer = %q, want %q", n.ID, got, want)
		}
	}

	conf := map[string]float32{}
	for _, n := range nodes {
		conf[n.ID] = n.Confidence
	}
	if conf["bot-1:mem_1"] != 0.9 {
		t.Fatalf("mem_1 confidence = %v, want 0.9", conf["bot-1:mem_1"])
	}
	if conf["bot-1:mem_2"] != 0.5 {
		t.Fatalf("mem_2 confidence = %v, want 0.5 (default)", conf["bot-1:mem_2"])
	}

	got := edgeSet(edges)
	expectEdge(t, got, "bot-1:mem_1", "bot-1:mem_2", EdgeSameProfile)
	expectEdge(t, got, "bot-1:mem_1", "bot-1:mem_2", EdgeSameDay)
	if _, ok := lookupEdge(got, "bot-1:mem_1", "bot-1:mem_2", EdgeSameTopic); ok {
		t.Fatal("did not expect a same_topic edge between mem_1 and mem_2 (different topics)")
	}
	for _, rel := range []EdgeRel{EdgeSameProfile, EdgeSameTopic, EdgeSameDay} {
		for _, other := range []string{"bot-1:mem_1", "bot-1:mem_2", "bot-1:mem_4"} {
			if _, ok := lookupEdge(got, "bot-1:mem_3", other, rel); ok {
				t.Fatalf("did not expect edge mem_3 <-> %s (%s)", other, rel)
			}
		}
	}
}

func TestPlanFromItemsRejectsInvalidLayerAndConfidence(t *testing.T) {
	items := []Item{
		{
			ID:        "bot-1:bad-layer",
			Memory:    "x",
			CreatedAt: "2026-06-01T00:00:00Z",
			Metadata:  map[string]any{"layer": "nonsense"},
		},
		{
			ID:        "bot-1:bad-conf",
			Memory:    "y",
			CreatedAt: "2026-06-01T00:00:00Z",
			Metadata:  map[string]any{"confidence": 2.5},
		},
	}
	nodes, _ := PlanFromItems("bot-1", items)
	byID := map[string]NodeSpec{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	if byID["bot-1:bad-layer"].Layer != LayerNote {
		t.Fatalf("invalid layer should fall back to note, got %q", byID["bot-1:bad-layer"].Layer)
	}
	if byID["bot-1:bad-conf"].Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should fall back to 0.5, got %v", byID["bot-1:bad-conf"].Confidence)
	}
}

func TestPlanFromItemsDerivesRefsFromWikiLinks(t *testing.T) {
	items := []Item{
		{
			ID:        "bot-1:mem_1",
			Memory:    "Alice likes local-first tools and references [[tech-stack]].",
			CreatedAt: "2026-06-01T00:00:00Z",
			Metadata:  map[string]any{"subject": "alice-profile"},
		},
		{
			ID:        "bot-1:mem_2",
			Memory:    "The stack links back to [Alice](alice-profile), but ignores [web](https://example.com).",
			CreatedAt: "2026-06-02T00:00:00Z",
			Metadata:  map[string]any{"subject": "Tech Stack"},
		},
	}
	_, edges := PlanFromItems("bot-1", items)
	got := edgeSet(edges)

	expectDirectedEdge(t, got, "bot-1:mem_1", "bot-1:mem_2", EdgeRefs)
	expectDirectedEdge(t, got, "bot-1:mem_2", "bot-1:mem_1", EdgeRefs)
	if links := parseMemoryLinks("[web](https://example.com) and [[Tech Stack]]"); len(links) != 1 || links[0] != "tech-stack" {
		t.Fatalf("parseMemoryLinks normalized links = %#v, want [tech-stack]", links)
	}
}

func TestPlanFromItemsEmptyAndSingleton(t *testing.T) {
	if nodes, edges := PlanFromItems("bot-1", nil); len(nodes) != 0 || len(edges) != 0 {
		t.Fatalf("empty input should yield empty plan, got %d nodes %d edges", len(nodes), len(edges))
	}
	nodes, edges := PlanFromItems("bot-1", []Item{{ID: "bot-1:mem_1", Memory: "x", CreatedAt: "2026-06-01T00:00:00Z"}})
	if len(nodes) != 1 || len(edges) != 0 {
		t.Fatalf("singleton should yield 1 node 0 edges, got %d nodes %d edges", len(nodes), len(edges))
	}
}

func TestPlanFromItemsFallsBackToUpdatedAtThenNow(t *testing.T) {
	noCreated := Item{ID: "bot-1:mem_1", Memory: "x", UpdatedAt: "2026-05-01T00:00:00Z"}
	nodes, _ := PlanFromItems("bot-1", []Item{noCreated})
	if !nodes[0].CapturedAt.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("captured_at should fall back to updated_at, got %v", nodes[0].CapturedAt)
	}

	neither := Item{ID: "bot-1:mem_1", Memory: "x"}
	nodes2, _ := PlanFromItems("bot-1", []Item{neither})
	if nodes2[0].CapturedAt.IsZero() {
		t.Fatal("captured_at should fall back to now when both created_at and updated_at are absent")
	}
}

func TestSummariseCountsLayers(t *testing.T) {
	items := []Item{
		{ID: "bot-1:mem_1", Memory: "a", CreatedAt: "2026-06-01T00:00:00Z", Metadata: map[string]any{"layer": "preference"}},
		{ID: "bot-1:mem_2", Memory: "b", CreatedAt: "2026-06-02T00:00:00Z", Metadata: map[string]any{"layer": "preference"}},
		{ID: "bot-1:mem_3", Memory: "c", CreatedAt: "2026-06-03T00:00:00Z"},
	}
	nodes, edges := PlanFromItems("bot-1", items)
	r := Summarise("bot-1", nodes, edges)
	if r.NodeCount != 3 {
		t.Fatalf("node count = %d, want 3", r.NodeCount)
	}
	if r.LayerBreak[LayerPreference] != 2 {
		t.Fatalf("preference count = %d, want 2", r.LayerBreak[LayerPreference])
	}
	if r.LayerBreak[LayerNote] != 1 {
		t.Fatalf("note count = %d, want 1", r.LayerBreak[LayerNote])
	}
}

func edgeSet(edges []EdgeSpec) map[string]struct{} {
	out := map[string]struct{}{}
	for _, e := range edges {
		out[e.SrcNode+"\x00"+e.DstNode+"\x00"+string(e.Rel)] = struct{}{}
	}
	return out
}

func lookupEdge(set map[string]struct{}, a, b string, rel EdgeRel) (struct{}, bool) {
	src, dst := a, b
	if dst < src {
		src, dst = dst, src
	}
	_, ok := set[src+"\x00"+dst+"\x00"+string(rel)]
	return struct{}{}, ok
}

func expectEdge(t *testing.T, set map[string]struct{}, a, b string, rel EdgeRel) {
	t.Helper()
	if _, ok := lookupEdge(set, a, b, rel); !ok {
		t.Fatalf("expected edge %s <-> %s (%s) not found", a, b, rel)
	}
}

func expectDirectedEdge(t *testing.T, set map[string]struct{}, src, dst string, rel EdgeRel) {
	t.Helper()
	if _, ok := set[src+"\x00"+dst+"\x00"+string(rel)]; !ok {
		t.Fatalf("expected directed edge %s -> %s (%s) not found", src, dst, rel)
	}
}
