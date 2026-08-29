package builtin

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/memory/migrate"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

// ingestFakeStore is a minimal memoryStore for ingest tests: it serves a
// fixed set of parsed memory items as the "files on disk", including items
// with no frontmatter id (which ReadAllMemoryFilesForIngest must synthesise).
type ingestFakeStore struct {
	items []storefs.MemoryItem
}

func (*ingestFakeStore) PersistMemories(context.Context, string, []storefs.MemoryItem, map[string]any) error {
	return nil
}

func (s *ingestFakeStore) ReadAllMemoryFiles(context.Context, string) ([]storefs.MemoryItem, error) {
	out := make([]storefs.MemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if strings.TrimSpace(it.ID) != "" {
			out = append(out, it)
		}
	}
	return out, nil
}

func (s *ingestFakeStore) ReadAllMemoryFilesForIngest(context.Context, string) ([]storefs.MemoryItem, error) {
	// Mirror storefs.ReadAllMemoryFilesForIngest: keep explicit ids, synthesise
	// a deterministic id for items that lack one.
	out := make([]storefs.MemoryItem, 0, len(s.items))
	for _, it := range s.items {
		if strings.TrimSpace(it.ID) != "" {
			out = append(out, it)
			continue
		}
		cp := it
		cp.ID = "mem_synth_" + strings.TrimSpace(it.Memory)
		out = append(out, cp)
	}
	return out, nil
}
func (*ingestFakeStore) RemoveMemories(context.Context, string, []string) error { return nil }
func (*ingestFakeStore) RemoveAllMemories(context.Context, string) error        { return nil }
func (*ingestFakeStore) RebuildFiles(context.Context, string, []storefs.MemoryItem, map[string]any) error {
	return nil
}
func (*ingestFakeStore) SyncOverview(context.Context, string) error { return nil }
func (s *ingestFakeStore) CountMemoryFiles(context.Context, string) (int, error) {
	return len(s.items), nil
}

var _ memoryStore = (*ingestFakeStore)(nil)

func TestIngestMarkdownFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newFakeWikiStore()
	fs := &ingestFakeStore{items: []storefs.MemoryItem{
		// 1. explicit id, preference layer.
		{ID: "mem_20260706_001", Memory: "User prefers oolong tea", Metadata: map[string]any{"layer": "preference", "confidence": "0.9"}},
		// 2. no id — must be synthesised deterministically.
		{Memory: "用户使用中文交流", Metadata: map[string]any{"layer": "identity"}},
		// 3. empty body — must be skipped.
		{ID: "mem_empty", Memory: "   "},
	}}
	rt := NewGraphRuntime(nil, store, fs)

	// First ingest: 2 items ingested (1 skipped for empty body).
	res, err := rt.IngestMarkdownFiles(ctx, "bot-1")
	if err != nil {
		t.Fatalf("IngestMarkdownFiles: %v", err)
	}
	if res.Ingested != 2 {
		t.Fatalf("Ingested = %d, want 2", res.Ingested)
	}
	if res.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", res.Skipped)
	}
	if got := len(store.nodes); got != 2 {
		t.Fatalf("store nodes = %d, want 2", got)
	}
	// Explicit id is preserved but qualified to "<botID>:<localId>" so
	// Update/Delete can recover the bot id from the memory id.
	if _, ok := store.nodes["bot-1:mem_20260706_001"]; !ok {
		t.Fatal("explicit frontmatter id not qualified/preserved on ingest")
	}
	// Synthesised id is stable and content-derived.
	var synthID string
	for id, n := range store.nodes {
		if n.Body == "用户使用中文交流" {
			synthID = id
		}
	}
	if synthID == "" {
		t.Fatal("synthesised-id item not ingested")
	}

	// Second ingest of the SAME files must be idempotent: no new nodes, same ids.
	res2, err := rt.IngestMarkdownFiles(ctx, "bot-1")
	if err != nil {
		t.Fatalf("second IngestMarkdownFiles: %v", err)
	}
	if res2.Ingested != 2 {
		t.Fatalf("re-ingest Ingested = %d, want 2 (idempotent)", res2.Ingested)
	}
	if got := len(store.nodes); got != 2 {
		t.Fatalf("after re-ingest, store nodes = %d, want 2 (no duplicates)", got)
	}
	// The CJK item keeps the SAME synthesised id (deterministic).
	if _, ok := store.nodes[synthID]; !ok {
		t.Fatalf("re-ingest changed the synthesised id; idempotency broken (old=%q)", synthID)
	}

	// Edit the no-id file's body -> new synthesised id => a third node appears.
	fs.items[1].Memory = "用户使用中文和英文交流"
	res3, err := rt.IngestMarkdownFiles(ctx, "bot-1")
	if err != nil {
		t.Fatalf("third IngestMarkdownFiles: %v", err)
	}
	if res3.Ingested != 2 {
		t.Fatalf("post-edit Ingested = %d, want 2", res3.Ingested)
	}
	if got := len(store.nodes); got != 3 {
		t.Fatalf("after body edit, store nodes = %d, want 3 (edited body -> new node)", got)
	}
}

func TestIngestMarkdownFilesRequiresStore(t *testing.T) {
	t.Parallel()
	rt := &graphRuntime{fs: &ingestFakeStore{}, logger: slog.Default()}
	if _, err := rt.IngestMarkdownFiles(context.Background(), "bot-x"); err == nil {
		t.Fatal("IngestMarkdownFiles without a store must error")
	}
}

func TestIngestMarkdownFilesRequiresBotID(t *testing.T) {
	t.Parallel()
	rt := NewGraphRuntime(nil, newFakeWikiStore(), &ingestFakeStore{})
	if _, err := rt.IngestMarkdownFiles(context.Background(), "  "); err == nil {
		t.Fatal("IngestMarkdownFiles with empty bot id must error")
	}
}

// TestIngestLayerClassification ensures ingest routes the parsed item's layer
// metadata through migrate.Plan so DB nodes carry a meaningful layer rather
// than defaulting everything to "note".
func TestIngestLayerClassification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newFakeWikiStore()
	fs := &ingestFakeStore{items: []storefs.MemoryItem{
		{ID: "mem_pref", Memory: "likes tea", Metadata: map[string]any{"layer": "preference"}},
		{ID: "mem_id", Memory: "is a developer", Metadata: map[string]any{"layer": "identity"}},
		{ID: "mem_note", Memory: "random fact"},
	}}
	rt := NewGraphRuntime(nil, store, fs)

	if _, err := rt.IngestMarkdownFiles(ctx, "bot-l"); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	want := map[string]migrate.MemoryLayer{
		"bot-l:mem_pref": migrate.LayerPreference,
		"bot-l:mem_id":   migrate.LayerIdentity,
		"bot-l:mem_note": migrate.LayerNote,
	}
	for id, layer := range want {
		n, ok := store.nodes[id]
		if !ok {
			t.Fatalf("node %q missing after ingest", id)
		}
		if n.Layer != layer {
			t.Fatalf("node %q layer = %q, want %q", id, n.Layer, layer)
		}
	}
}

func TestQualifyIngestID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		botID string
		in    string
		want  string
	}{
		{"empty id", "bot-1", "", ""},
		{"bare local id gets prefixed", "bot-1", "mem_20260706_005", "bot-1:mem_20260706_005"},
		{"already qualified stays single-prefixed", "bot-1", "bot-1:mem_5", "bot-1:mem_5"},
		{"different prefix replaced", "bot-1", "other:mem_5", "bot-1:mem_5"},
		{"synth id prefixed", "bot-1", "a1b2c3d4", "bot-1:a1b2c3d4"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := qualifyIngestID(c.botID, c.in); got != c.want {
				t.Fatalf("qualifyIngestID(%q,%q) = %q, want %q", c.botID, c.in, got, c.want)
			}
		})
	}
}
