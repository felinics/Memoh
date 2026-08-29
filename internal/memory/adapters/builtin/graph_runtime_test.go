package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/memory/migrate"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
	"github.com/felinics/memoh/internal/memory/wikistore"
)

var errForced = errors.New("forced store error for test")

// fakeWikiStore is an in-memory wikistore.Store for graphRuntime tests.
type fakeWikiStore struct {
	nodes map[string]migrate.NodeSpec // keyed by node ID
	edges map[string]migrate.EdgeSpec // keyed by src\0dst\0rel
}

func newFakeWikiStore() *fakeWikiStore {
	return &fakeWikiStore{nodes: map[string]migrate.NodeSpec{}, edges: map[string]migrate.EdgeSpec{}}
}

func (s *fakeWikiStore) UpsertNode(_ context.Context, node migrate.NodeSpec) (migrate.NodeSpec, error) {
	s.nodes[node.ID] = node
	return node, nil
}

func (s *fakeWikiStore) GetNode(_ context.Context, _, nodeID string) (migrate.NodeSpec, error) {
	if n, ok := s.nodes[nodeID]; ok {
		return n, nil
	}
	return migrate.NodeSpec{}, wikistore.ErrNodeNotFound
}

func (s *fakeWikiStore) ListNodes(_ context.Context, _ string) ([]migrate.NodeSpec, error) {
	out := make([]migrate.NodeSpec, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	return out, nil
}

func (s *fakeWikiStore) ListNodesByLayer(_ context.Context, _ string, layer migrate.MemoryLayer) ([]migrate.NodeSpec, error) {
	out := []migrate.NodeSpec{}
	for _, n := range s.nodes {
		if n.Layer == layer {
			out = append(out, n)
		}
	}
	return out, nil
}

func (s *fakeWikiStore) DeleteNode(_ context.Context, _, nodeID string) error {
	delete(s.nodes, nodeID)
	return nil
}

func (s *fakeWikiStore) DeleteAllNodes(_ context.Context, _ string) error {
	s.nodes = map[string]migrate.NodeSpec{}
	s.edges = map[string]migrate.EdgeSpec{}
	return nil
}

func (s *fakeWikiStore) CountNodes(_ context.Context, _ string) (int, error) {
	return len(s.nodes), nil
}

func (s *fakeWikiStore) UpsertEdges(_ context.Context, edges []migrate.EdgeSpec) error {
	for _, e := range edges {
		s.edges[e.SrcNode+"\x00"+e.DstNode+"\x00"+string(e.Rel)] = e
	}
	return nil
}

func (s *fakeWikiStore) ListEdges(_ context.Context, _ string) ([]migrate.EdgeSpec, error) {
	out := make([]migrate.EdgeSpec, 0, len(s.edges))
	for _, e := range s.edges {
		out = append(out, e)
	}
	return out, nil
}
func (*fakeWikiStore) DeleteEdgesForNode(_ context.Context, _, _ string) error { return nil }
func (s *fakeWikiStore) DeleteAllEdges(_ context.Context, _ string) error {
	s.edges = map[string]migrate.EdgeSpec{}
	return nil
}

func (s *fakeWikiStore) CountEdges(_ context.Context, _ string) (int, error) {
	return len(s.edges), nil
}

func (s *fakeWikiStore) RebuildDerivedEdges(_ context.Context, _ string) (int, error) {
	nodes := []migrate.NodeSpec{}
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	for key, edge := range s.edges {
		for _, rel := range migrate.DerivedEdgeRels {
			if edge.Rel == rel {
				delete(s.edges, key)
				break
			}
		}
	}
	derived := migrate.PlanFromNodes(nodes)
	for _, e := range derived {
		s.edges[e.SrcNode+"\x00"+e.DstNode+"\x00"+string(e.Rel)] = e
	}
	return len(derived), nil
}

func TestGraphRuntimeAddSearchDelete(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())

	botID := "graph-bot-1"
	ctx := context.Background()

	// Add two memories sharing a profile_ref -> implicit same_profile edge.
	if _, err := rt.Add(ctx, adapters.AddRequest{
		BotID:    botID,
		Message:  "I prefer oolong tea",
		Metadata: map[string]any{"profile_ref": "user:1", "topic": "drinks"},
	}); err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	if _, err := rt.Add(ctx, adapters.AddRequest{
		BotID:    botID,
		Message:  "I live in Berlin",
		Metadata: map[string]any{"profile_ref": "user:1", "topic": "location"},
	}); err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	// GetAll reflects 2 nodes.
	all, err := rt.GetAll(ctx, adapters.GetAllRequest{BotID: botID})
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(all.Results) != 2 {
		t.Fatalf("GetAll = %d, want 2", len(all.Results))
	}

	// Search "tea" seeds the oolong node; expansion reaches the Berlin node via
	// the shared-profile edge, so both surface and Relations is non-empty.
	resp, err := rt.Search(ctx, adapters.SearchRequest{BotID: botID, Query: "tea", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("Search returned no results")
	}
	if len(resp.Relations) == 0 {
		t.Fatal("Search Relations empty; expected the same_profile edge")
	}
	foundOolong := false
	for _, r := range resp.Results {
		if strings.Contains(r.Memory, "oolong") {
			foundOolong = true
		}
	}
	if !foundOolong {
		t.Fatal("Search did not surface the oolong memory")
	}

	// Status reports mode graph + node count.
	st, err := rt.Status(ctx, botID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.MemoryMode != "graph" {
		t.Fatalf("Status mode = %q, want graph", st.MemoryMode)
	}
	if st.SourceCount != 2 {
		t.Fatalf("Status node count = %d, want 2", st.SourceCount)
	}

	// Delete one node; count drops to 1.
	firstID := all.Results[0].ID
	if _, err := rt.Delete(ctx, firstID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	remaining, _ := rt.GetAll(ctx, adapters.GetAllRequest{BotID: botID})
	if len(remaining.Results) != 1 {
		t.Fatalf("after Delete, GetAll = %d, want 1", len(remaining.Results))
	}

	// DeleteAll clears everything.
	if _, err := rt.DeleteAll(ctx, adapters.DeleteAllRequest{BotID: botID}); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if n, _ := store.CountNodes(ctx, botID); n != 0 {
		t.Fatalf("after DeleteAll, node count = %d, want 0", n)
	}
}

func TestGraphRuntimeSearchExpandsRefs(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())

	botID := "graph-bot-refs"
	ctx := context.Background()

	if _, err := rt.Add(ctx, adapters.AddRequest{
		BotID:    botID,
		Message:  "Alice's favorite editor is Helix and she links [[berlin-home]].",
		Metadata: map[string]any{"subject": "alice-profile"},
	}); err != nil {
		t.Fatalf("Add ref source: %v", err)
	}
	if _, err := rt.Add(ctx, adapters.AddRequest{
		BotID:    botID,
		Message:  "Berlin home details are kept here.",
		Metadata: map[string]any{"subject": "Berlin Home"},
	}); err != nil {
		t.Fatalf("Add ref target: %v", err)
	}

	resp, err := rt.Search(ctx, adapters.SearchRequest{BotID: botID, Query: "Helix", Limit: 5})
	if err != nil {
		t.Fatalf("Search refs: %v", err)
	}
	foundBerlin := false
	foundRefs := false
	for _, r := range resp.Results {
		if strings.Contains(r.Memory, "Berlin home") {
			foundBerlin = true
		}
	}
	for _, rel := range resp.Relations {
		if relation, ok := rel.(map[string]any); ok && relation["rel"] == string(migrate.EdgeRefs) {
			foundRefs = true
		}
	}
	if !foundBerlin {
		t.Fatal("Search did not expand across refs edge to the Berlin memory")
	}
	if !foundRefs {
		t.Fatal("Search did not report the refs relation")
	}
}

func TestGraphRuntimeUpdateCanonicalIDMigratesLegacyBareNode(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())
	ctx := context.Background()
	botID := "bot-1"
	store.nodes["mem_legacy"] = migrate.NodeSpec{
		ID:    "mem_legacy",
		BotID: botID,
		Body:  "old body",
		Layer: migrate.LayerNote,
	}

	item, err := rt.Update(ctx, adapters.UpdateRequest{
		MemoryID: botID + ":mem_legacy",
		Memory:   "new body",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if item.ID != botID+":mem_legacy" {
		t.Fatalf("updated id = %q", item.ID)
	}
	if _, ok := store.nodes["mem_legacy"]; ok {
		t.Fatal("legacy bare node still exists after canonical update")
	}
	saved, ok := store.nodes[botID+":mem_legacy"]
	if !ok {
		t.Fatalf("canonical node missing: %#v", store.nodes)
	}
	if saved.Body != "new body" {
		t.Fatalf("saved body = %q", saved.Body)
	}
}

func TestGraphRuntimeDeleteCanonicalIDRemovesLegacyBareNode(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())
	ctx := context.Background()
	botID := "bot-1"
	store.nodes["mem_legacy"] = migrate.NodeSpec{
		ID:    "mem_legacy",
		BotID: botID,
		Body:  "old body",
		Layer: migrate.LayerNote,
	}

	if _, err := rt.Delete(ctx, botID+":mem_legacy"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := store.nodes["mem_legacy"]; ok {
		t.Fatal("legacy bare node still exists after canonical delete")
	}
}

func TestGraphRuntimeCompactCanonicalizesLegacyBareIDs(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())
	ctx := context.Background()
	botID := "bot-1"
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store.nodes["mem_legacy"] = migrate.NodeSpec{
		ID:         "mem_legacy",
		BotID:      botID,
		Body:       "Legacy memory survives under the canonical ID.",
		Layer:      migrate.LayerNote,
		CapturedAt: now,
	}
	store.nodes[botID+":mem_other"] = migrate.NodeSpec{
		ID:         botID + ":mem_other",
		BotID:      botID,
		Body:       "Another memory stays separate.",
		Layer:      migrate.LayerNote,
		CapturedAt: now.Add(time.Minute),
	}

	result, err := rt.Compact(ctx, map[string]any{"bot_id": botID}, 1, 0)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.BeforeCount != 2 || result.AfterCount != 2 {
		t.Fatalf("Compact counts = %d/%d, want 2/2", result.BeforeCount, result.AfterCount)
	}
	if _, ok := store.nodes["mem_legacy"]; ok {
		t.Fatal("legacy bare node still exists after compact")
	}
	if _, ok := store.nodes[botID+":mem_legacy"]; !ok {
		t.Fatalf("canonical legacy node missing: %#v", store.nodes)
	}
}

func TestGraphRuntimeCompactWithLLMMergesConceptNodes(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())
	ctx := context.Background()
	botID := "bot-1"
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store.nodes[botID+":mem_alice_1"] = migrate.NodeSpec{
		ID:         botID + ":mem_alice_1",
		BotID:      botID,
		Body:       "Alice likes tea.",
		Layer:      migrate.LayerNote,
		Subject:    "Alice",
		Topic:      "drinks",
		Metadata:   map[string]any{"subject": "Alice", "topic": "drinks"},
		CapturedAt: now.Add(-2 * time.Hour),
	}
	store.nodes["mem_alice_2"] = migrate.NodeSpec{
		ID:         "mem_alice_2",
		BotID:      botID,
		Body:       "Alice prefers oolong tea.",
		Layer:      migrate.LayerNote,
		Subject:    "Alice",
		Topic:      "drinks",
		Metadata:   map[string]any{"subject": "Alice", "topic": "drinks"},
		CapturedAt: now.Add(-time.Hour),
	}
	store.nodes[botID+":mem_bob"] = migrate.NodeSpec{
		ID:         botID + ":mem_bob",
		BotID:      botID,
		Body:       "Bob uses Vim.",
		Layer:      migrate.LayerNote,
		Subject:    "Bob",
		Metadata:   map[string]any{"subject": "Bob"},
		CapturedAt: now,
	}
	llm := &fakeLLM{compactFunc: func(req adapters.CompactRequest) adapters.CompactResponse {
		if req.BotID != botID {
			t.Errorf("Compact BotID = %q, want %q", req.BotID, botID)
		}
		if req.TargetCount != 1 {
			t.Errorf("Compact TargetCount = %d, want 1", req.TargetCount)
		}
		if len(req.Memories) != 2 {
			t.Errorf("Compact memories = %d, want 2", len(req.Memories))
		}
		for _, memory := range req.Memories {
			if !strings.HasPrefix(memory.ID, botID+":") {
				t.Errorf("Compact candidate id = %q, want canonical bot prefix", memory.ID)
			}
		}
		return adapters.CompactResponse{Facts: []string{"Alice likes tea and prefers oolong."}}
	}}

	result, err := rt.CompactWithLLM(ctx, map[string]any{"bot_id": botID}, 1, 0, llm)
	if err != nil {
		t.Fatalf("CompactWithLLM: %v", err)
	}
	if result.BeforeCount != 3 || result.AfterCount != 2 {
		t.Fatalf("CompactWithLLM counts = %d/%d, want 3/2", result.BeforeCount, result.AfterCount)
	}
	if llm.compactCalls != 1 {
		t.Fatalf("LLM compact calls = %d, want 1", llm.compactCalls)
	}
	if _, ok := store.nodes["mem_alice_2"]; ok {
		t.Fatal("legacy bare Alice node still exists after compact")
	}

	aliceNodes := graphTestNodesBySubject(store.nodes, "Alice")
	if len(aliceNodes) != 1 {
		t.Fatalf("Alice node count = %d, want 1: %#v", len(aliceNodes), aliceNodes)
	}
	alice := aliceNodes[0]
	if alice.Body != "Alice likes tea and prefers oolong." {
		t.Fatalf("Alice body = %q", alice.Body)
	}
	sourceIDs := graphTestMetadataStrings(t, alice.Metadata, "compaction_source_ids")
	graphTestRequireStrings(t, sourceIDs, botID+":mem_alice_1", botID+":mem_alice_2")
	if got := alice.Metadata["compaction_strategy"]; got != "concept_merge" {
		t.Fatalf("compaction_strategy = %#v, want concept_merge", got)
	}
	for id := range store.nodes {
		if !strings.HasPrefix(id, botID+":") {
			t.Fatalf("store still contains non-canonical id %q", id)
		}
	}
}

func TestGraphRuntimeCompactWithLLMPreservesProtectedConceptNodes(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())
	ctx := context.Background()
	botID := "bot-1"
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store.nodes[botID+":mem_pinned"] = migrate.NodeSpec{
		ID:         botID + ":mem_pinned",
		BotID:      botID,
		Body:       "Pinned Alice memory must not be rewritten.",
		Layer:      migrate.LayerNote,
		Subject:    "Alice",
		Metadata:   map[string]any{"subject": "Alice", "pinned": true},
		CapturedAt: now,
	}
	store.nodes[botID+":mem_a"] = migrate.NodeSpec{
		ID:         botID + ":mem_a",
		BotID:      botID,
		Body:       "Alice likes tea.",
		Layer:      migrate.LayerNote,
		Subject:    "Alice",
		Metadata:   map[string]any{"subject": "Alice"},
		CapturedAt: now.Add(-2 * time.Hour),
	}
	store.nodes[botID+":mem_b"] = migrate.NodeSpec{
		ID:         botID + ":mem_b",
		BotID:      botID,
		Body:       "Alice prefers oolong.",
		Layer:      migrate.LayerNote,
		Subject:    "Alice",
		Metadata:   map[string]any{"subject": "Alice"},
		CapturedAt: now.Add(-time.Hour),
	}
	llm := &fakeLLM{compactFunc: func(req adapters.CompactRequest) adapters.CompactResponse {
		for _, memory := range req.Memories {
			if memory.ID == botID+":mem_pinned" {
				t.Errorf("protected node was sent to compact LLM")
			}
		}
		return adapters.CompactResponse{Facts: []string{"Alice likes tea and prefers oolong."}}
	}}

	result, err := rt.CompactWithLLM(ctx, map[string]any{"bot_id": botID}, 1, 0, llm)
	if err != nil {
		t.Fatalf("CompactWithLLM: %v", err)
	}
	if result.BeforeCount != 3 || result.AfterCount != 2 {
		t.Fatalf("CompactWithLLM counts = %d/%d, want 3/2", result.BeforeCount, result.AfterCount)
	}
	pinned, ok := store.nodes[botID+":mem_pinned"]
	if !ok {
		t.Fatal("pinned node missing after compact")
	}
	if pinned.Body != "Pinned Alice memory must not be rewritten." {
		t.Fatalf("pinned body = %q", pinned.Body)
	}
	if llm.compactCalls != 1 {
		t.Fatalf("LLM compact calls = %d, want 1", llm.compactCalls)
	}
}

func graphTestNodesBySubject(nodes map[string]migrate.NodeSpec, subject string) []migrate.NodeSpec {
	out := make([]migrate.NodeSpec, 0)
	for _, node := range nodes {
		if node.Subject == subject {
			out = append(out, node)
		}
	}
	return out
}

func graphTestMetadataStrings(t *testing.T, metadata map[string]any, key string) []string {
	t.Helper()
	raw, ok := metadata[key]
	if !ok {
		t.Fatalf("metadata missing %q: %#v", key, metadata)
	}
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			s, ok := value.(string)
			if !ok {
				t.Fatalf("metadata %q entry has type %T, want string", key, value)
			}
			out = append(out, strings.TrimSpace(s))
		}
		return out
	default:
		t.Fatalf("metadata %q has type %T, want string list", key, raw)
		return nil
	}
}

func graphTestRequireStrings(t *testing.T, got []string, want ...string) {
	t.Helper()
	seen := make(map[string]struct{}, len(got))
	for _, value := range got {
		seen[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := seen[value]; !ok {
			t.Fatalf("string list %v missing %q", got, value)
		}
	}
}

func TestGraphRuntimeFileFallback(t *testing.T) {
	t.Parallel()
	// A wiki store that returns an error on ListNodes forces the file fallback.
	store := &errWikiStore{}
	fs := newFakeStore(
		storefs.MemoryItem{ID: "bot-x:m1", Memory: "fallback oolong memory", CreatedAt: time.Now().Format(time.RFC3339)},
	)
	rt := NewGraphRuntime(nil, store, fs)

	resp, err := rt.Search(context.Background(), adapters.SearchRequest{
		BotID: "bot-x", Query: "oolong", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search (fallback) should not error: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("file fallback returned no results")
	}
	if !strings.Contains(resp.Results[0].Memory, "oolong") {
		t.Fatalf("file fallback result = %q, want oolong", resp.Results[0].Memory)
	}
}

// errWikiStore returns an error on every read so the graph runtime degrades to
// the file-lexical fallback path.
type errWikiStore struct{}

func (errWikiStore) UpsertNode(context.Context, migrate.NodeSpec) (migrate.NodeSpec, error) {
	return migrate.NodeSpec{}, errForced
}

func (errWikiStore) GetNode(context.Context, string, string) (migrate.NodeSpec, error) {
	return migrate.NodeSpec{}, errForced
}

func (errWikiStore) ListNodes(context.Context, string) ([]migrate.NodeSpec, error) {
	return nil, errForced
}

func (errWikiStore) ListNodesByLayer(context.Context, string, migrate.MemoryLayer) ([]migrate.NodeSpec, error) {
	return nil, errForced
}
func (errWikiStore) DeleteNode(context.Context, string, string) error      { return nil }
func (errWikiStore) DeleteAllNodes(context.Context, string) error          { return nil }
func (errWikiStore) CountNodes(context.Context, string) (int, error)       { return 0, errForced }
func (errWikiStore) UpsertEdges(context.Context, []migrate.EdgeSpec) error { return nil }
func (errWikiStore) ListEdges(context.Context, string) ([]migrate.EdgeSpec, error) {
	return nil, errForced
}
func (errWikiStore) DeleteEdgesForNode(context.Context, string, string) error { return nil }
func (errWikiStore) DeleteAllEdges(context.Context, string) error             { return nil }
func (errWikiStore) CountEdges(context.Context, string) (int, error)          { return 0, errForced }
func (errWikiStore) RebuildDerivedEdges(context.Context, string) (int, error) {
	return 0, errForced
}

// TestGraphRuntimeSearchCJKSentence is the end-to-end regression for the
// Chinese-word-segmentation bug: a whole Chinese sentence used to collapse into
// a single token under strings.Fields and never matched a stored memory body.
// With segment.LexicalScore (gse), "语言"/"交流" split out and seed the node.
func TestGraphRuntimeSearchCJKSentence(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	rt := NewGraphRuntime(nil, store, newFakeStore())

	botID := "graph-bot-cjk"
	ctx := context.Background()

	if _, err := rt.Add(ctx, adapters.AddRequest{
		BotID:   botID,
		Message: "用户使用中文交流",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A full Chinese sentence query (no spaces). Pre-fix this scored 0 and
	// returned no results.
	resp, err := rt.Search(ctx, adapters.SearchRequest{BotID: botID, Query: "你还记得我用什么语言交流吗？", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("CJK sentence search returned no results; expected the 中文交流 memory to surface")
	}
	if !strings.Contains(resp.Results[0].Memory, "中文交流") {
		t.Fatalf("CJK search result = %q, want the 中文交流 memory", resp.Results[0].Memory)
	}
}
