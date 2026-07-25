package fs

import (
	"strings"
	"testing"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

func TestFormatAndParseMemoryDayMD_Roundtrip(t *testing.T) {
	items := []memorydomain.Item{
		{
			ID:        "mem_2",
			Memory:    "second record",
			Hash:      "h2",
			CreatedAt: "2026-03-01T11:15:00Z",
			Metadata:  map[string]any{"topic": "Notes"},
		},
		{
			ID:        "mem_1",
			Memory:    "first record",
			Hash:      "h1",
			CreatedAt: "2026-03-01T09:40:00Z",
			Metadata:  map[string]any{"topic": "Decision"},
		},
	}

	md := formatMemoryDayMD("2026-03-01", items)
	if !strings.Contains(md, "# Memory 2026-03-01") {
		t.Fatalf("expected header in markdown: %s", md)
	}
	if !strings.Contains(md, "## Entry mem_1") {
		t.Fatalf("expected entry heading in markdown: %s", md)
	}
	if !strings.Contains(md, "```yaml") {
		t.Fatalf("expected yaml block in markdown: %s", md)
	}

	parsed, err := parseMemoryDayMD(md)
	if err != nil {
		t.Fatalf("parseMemoryDayMD error: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed items, got %d", len(parsed))
	}
	// formatMemoryDayMD sorts by created_at ascending.
	if parsed[0].ID != "mem_1" || parsed[1].ID != "mem_2" {
		t.Fatalf("unexpected order after roundtrip: %#v", parsed)
	}
	if got := parsed[0].Metadata["topic"]; got != "Decision" {
		t.Fatalf("expected metadata preserved, got %#v", parsed[0].Metadata)
	}
}

func TestParseJSONMemoryItems(t *testing.T) {
	raw := `[
  {
    "id": "mem_json",
    "topic": "Decision",
    "memory": "Choose provider architecture."
  }
]`

	items, err := parseJSONMemoryItems(raw)
	if err != nil {
		t.Fatalf("parseJSONMemoryItems error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ID != "mem_json" {
		t.Fatalf("unexpected id: %#v", items[0])
	}
	if got := items[0].Metadata["topic"]; got != "Decision" {
		t.Fatalf("expected topic metadata, got %#v", items[0].Metadata)
	}
	if items[0].Memory != "Choose provider architecture." {
		t.Fatalf("unexpected memory body: %#v", items[0])
	}
}

func TestParseJSONMemoryItemsCanBeFormattedToCanonicalMarkdown(t *testing.T) {
	raw := `[
  {
    "id": "mem_json",
    "topic": "Decision",
    "memory": "Choose provider architecture."
  }
]`

	items, err := parseJSONMemoryItems(raw)
	if err != nil {
		t.Fatalf("parseJSONMemoryItems error: %v", err)
	}
	md := formatMemoryDayMD("2026-03-01", items)
	if !strings.Contains(md, "## Entry mem_json") {
		t.Fatalf("expected canonical heading, got: %s", md)
	}
	if !strings.Contains(md, "topic: Decision") {
		t.Fatalf("expected yaml metadata topic, got: %s", md)
	}
	parsed, err := parseMemoryDayMD(md)
	if err != nil {
		t.Fatalf("parseMemoryDayMD error: %v", err)
	}
	if len(parsed) != 1 || parsed[0].ID != "mem_json" {
		t.Fatalf("unexpected parsed canonical items: %#v", parsed)
	}
}

func TestFormatAndParseConceptMDRoundtrip(t *testing.T) {
	t.Parallel()

	plans := planConceptFiles([]memorydomain.Item{
		{
			ID:        "bot-1:mem_1",
			Memory:    "Alice prefers oolong and references [[tea-stack]].",
			Hash:      "h1",
			CreatedAt: "2026-07-06T10:00:00Z",
			UpdatedAt: "2026-07-06T11:00:00Z",
			Metadata: map[string]any{
				"layer":       "preference",
				"fact_type":   "preference",
				"subject":     "Alice Profile",
				"topic":       "tea",
				"confidence":  0.8,
				"profile_ref": "user:alice",
			},
		},
		{
			ID:        "bot-1:mem_2",
			Memory:    "Tea stack details.",
			CreatedAt: "2026-07-06T09:00:00Z",
			Metadata:  map[string]any{"layer": "context", "subject": "Tea Stack"},
		},
	})
	links := conceptLinkIndex(plans)

	md := formatConceptMD(plans[0], links)
	for _, want := range []string{
		"---\n",
		"type: preference",
		"title: Alice Profile",
		"layer: preference",
		"tags:",
		"- tea",
		"timestamp: \"2026-07-06T10:00:00Z\"",
		"[tea-stack](../context/tea-stack.md)",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("formatted concept missing %q:\n%s", want, md)
		}
	}

	parsed, err := parseConceptMD(md)
	if err != nil {
		t.Fatalf("parseConceptMD error: %v", err)
	}
	if parsed.ID != "bot-1:mem_1" || parsed.Hash != "h1" {
		t.Fatalf("unexpected parsed identity: %#v", parsed)
	}
	if parsed.CreatedAt != "2026-07-06T10:00:00Z" || parsed.UpdatedAt != "2026-07-06T11:00:00Z" {
		t.Fatalf("unexpected timestamps: %#v", parsed)
	}
	if got := parsed.Metadata["fact_type"]; got != "preference" {
		t.Fatalf("fact_type metadata = %#v", got)
	}
	if got := parsed.Metadata["topic"]; got != "tea" {
		t.Fatalf("topic metadata = %#v", got)
	}
	if got := parsed.Metadata["subject"]; got != "Alice Profile" {
		t.Fatalf("subject metadata = %#v", got)
	}
	if !strings.Contains(parsed.Memory, "[[tea-stack]]") {
		t.Fatalf("expected relative md link restored to wiki link, got %q", parsed.Memory)
	}
}

func TestParseConceptMDUsesTitleAsSubjectFallback(t *testing.T) {
	t.Parallel()

	parsed, err := parseConceptMD(`---
type: preference
title: Alice Profile
id: bot-1:mem_1
layer: preference
tags:
  - tea
timestamp: "2026-07-06T10:00:00Z"
---

Alice prefers oolong tea.
`)
	if err != nil {
		t.Fatalf("parseConceptMD error: %v", err)
	}
	if got := parsed.Metadata["subject"]; got != "Alice Profile" {
		t.Fatalf("title should become subject fallback, got %#v", parsed.Metadata)
	}
}

func TestPlanConceptFilesDeduplicatesSlugCollisions(t *testing.T) {
	t.Parallel()

	plans := planConceptFiles([]memorydomain.Item{
		{ID: "bot-1:mem_b", Memory: "B", Metadata: map[string]any{"layer": "identity", "subject": "Alice"}},
		{ID: "bot-1:mem_a", Memory: "A", Metadata: map[string]any{"layer": "identity", "subject": "Alice"}},
		{ID: "bot-1:mem_c", Memory: "C", Metadata: map[string]any{"layer": "note", "subject": "Alice"}},
	})
	byID := map[string]conceptFilePlan{}
	for _, plan := range plans {
		byID[plan.Item.ID] = plan
	}
	if byID["bot-1:mem_a"].Slug != "alice" {
		t.Fatalf("first deterministic slug = %q", byID["bot-1:mem_a"].Slug)
	}
	if byID["bot-1:mem_b"].Slug != "alice-2" {
		t.Fatalf("collision slug = %q", byID["bot-1:mem_b"].Slug)
	}
	if byID["bot-1:mem_c"].Slug != "alice" || byID["bot-1:mem_c"].Layer != "note" {
		t.Fatalf("different layer should keep slug, got %+v", byID["bot-1:mem_c"])
	}
}

func TestPlanConceptFilesAvoidsOKFReservedFilenames(t *testing.T) {
	t.Parallel()

	plans := planConceptFiles([]memorydomain.Item{
		{ID: "bot-1:mem_a", Memory: "A", Metadata: map[string]any{"layer": "note", "subject": "Index"}},
		{ID: "bot-1:mem_b", Memory: "B", Metadata: map[string]any{"layer": "note", "subject": "Log"}},
	})
	byID := map[string]conceptFilePlan{}
	for _, plan := range plans {
		byID[plan.Item.ID] = plan
	}
	if got := byID["bot-1:mem_a"].Slug; got != "index-memory" {
		t.Fatalf("reserved index slug = %q", got)
	}
	if got := byID["bot-1:mem_b"].Slug; got != "log-memory" {
		t.Fatalf("reserved log slug = %q", got)
	}
}

func TestPlanConceptPersistenceOnlyWritesIncoming(t *testing.T) {
	t.Parallel()

	existing := memorydomain.Item{
		ID:       "bot-1:mem_existing",
		Memory:   "Alice likes tea.",
		Metadata: map[string]any{"layer": "context", "subject": "Alice"},
	}
	incoming := memorydomain.Item{
		ID:       "bot-1:mem_incoming",
		Memory:   "Bob likes coffee.",
		Metadata: map[string]any{"layer": "preference", "subject": "Bob"},
	}
	writes, _, overviewItems, overviewPaths := planConceptPersistence(map[string]scanEntry{
		existing.ID: {FilePath: memoryConceptPath("context", "alice"), Item: existing},
	}, []memorydomain.Item{incoming})

	if len(writes) != 1 {
		t.Fatalf("expected one incoming write, got %#v", writes)
	}
	if writes[0].Plan.Item.ID != incoming.ID {
		t.Fatalf("unexpected write plan: %#v", writes[0])
	}
	if writes[0].Plan.FilePath != memoryConceptPath("preference", "bob") {
		t.Fatalf("unexpected incoming path: %#v", writes[0].Plan)
	}
	if len(overviewItems) != 2 {
		t.Fatalf("overview should include existing and incoming items, got %#v", overviewItems)
	}
	if overviewPaths[existing.ID] != "memory/context/alice.md" {
		t.Fatalf("existing overview path should stay actual, got %#v", overviewPaths)
	}
	if overviewPaths[incoming.ID] != "memory/preference/bob.md" {
		t.Fatalf("incoming overview path should be planned concept path, got %#v", overviewPaths)
	}
}

func TestPlanConceptPersistencePreservesExistingSlugCollision(t *testing.T) {
	t.Parallel()

	existing := memorydomain.Item{
		ID:       "bot-1:mem_b",
		Memory:   "Existing Alice profile.",
		Metadata: map[string]any{"layer": "identity", "subject": "Alice"},
	}
	incoming := memorydomain.Item{
		ID:       "bot-1:mem_a",
		Memory:   "New Alice profile.",
		Metadata: map[string]any{"layer": "identity", "subject": "Alice"},
	}
	writes, _, _, _ := planConceptPersistence(map[string]scanEntry{
		existing.ID: {FilePath: memoryConceptPath("identity", "alice"), Item: existing},
	}, []memorydomain.Item{incoming})

	if len(writes) != 1 {
		t.Fatalf("expected one incoming write, got %#v", writes)
	}
	if writes[0].Plan.FilePath != memoryConceptPath("identity", "alice-2") {
		t.Fatalf("incoming should not steal existing alice.md, got %#v", writes[0].Plan)
	}
}

func TestPlanConceptPersistenceTracksMovedOldPath(t *testing.T) {
	t.Parallel()

	oldPath := memoryConceptPath("preference", "tea")
	existing := memorydomain.Item{
		ID:       "bot-1:mem_a",
		Memory:   "Alice likes tea.",
		Metadata: map[string]any{"layer": "preference", "subject": "Tea"},
	}
	incoming := memorydomain.Item{
		ID:       existing.ID,
		Memory:   "Alice likes oolong.",
		Metadata: map[string]any{"layer": "preference", "subject": "Oolong"},
	}
	writes, _, _, _ := planConceptPersistence(map[string]scanEntry{
		existing.ID: {FilePath: oldPath, Item: existing},
	}, []memorydomain.Item{incoming})

	if len(writes) != 1 {
		t.Fatalf("expected one incoming write, got %#v", writes)
	}
	if writes[0].OldFilePath != oldPath {
		t.Fatalf("expected old path tracked for cleanup, got %#v", writes[0])
	}
	if writes[0].Plan.FilePath != memoryConceptPath("preference", "oolong") {
		t.Fatalf("expected moved concept path, got %#v", writes[0].Plan)
	}
}

func TestFormatConceptMDUsesActualLinkRelPath(t *testing.T) {
	t.Parallel()

	source := memorydomain.Item{
		ID:       "bot-1:mem_source",
		Memory:   "Alice references [[legacy-topic]].",
		Metadata: map[string]any{"layer": "preference", "subject": "Alice"},
	}
	target := memorydomain.Item{
		ID:       "bot-1:mem_target",
		Memory:   "Legacy topic body.",
		Metadata: map[string]any{"layer": "context", "subject": "Legacy Topic"},
	}
	sourcePlan := planConceptFileWithReservedPath(source, nil)
	links := conceptLinkIndexForItems([]memorydomain.Item{source, target}, map[string]string{
		source.ID: sourcePlan.RelPath,
		target.ID: "memory/20260706.md",
	})

	md := formatConceptMD(sourcePlan, links)
	if !strings.Contains(md, "[legacy-topic](../20260706.md)") {
		t.Fatalf("expected link to actual legacy path, got:\n%s", md)
	}
}

func TestFormatMemoryOverviewLinksToConceptFiles(t *testing.T) {
	t.Parallel()

	md := formatMemoryOverviewMD([]memorydomain.Item{
		{
			ID:        "bot-1:mem_1",
			Memory:    "Alice prefers oolong tea.",
			CreatedAt: "2026-07-06T10:00:00Z",
			Metadata:  map[string]any{"layer": "preference", "subject": "Alice"},
		},
	})
	if !strings.Contains(md, "[Alice](memory/preference/alice.md)") {
		t.Fatalf("overview should link to concept file, got:\n%s", md)
	}
}

func TestFormatMemoryOverviewUsesProvidedPaths(t *testing.T) {
	t.Parallel()

	md := formatMemoryOverviewMDWithPaths([]memorydomain.Item{
		{
			ID:        "bot-1:mem_1",
			Memory:    "Legacy memory entry.",
			CreatedAt: "2026-07-06T10:00:00Z",
			Metadata:  map[string]any{"layer": "context", "subject": "Legacy Topic"},
		},
	}, map[string]string{"bot-1:mem_1": "memory/20260706.md"})
	if !strings.Contains(md, "[Legacy Topic](memory/20260706.md)") {
		t.Fatalf("overview should preserve actual path, got:\n%s", md)
	}
}

// TestSynthIngestID verifies the deterministic id synthesised for agent files
// that lack a frontmatter id: same (path, body) => same id; any change => diff.
// This is what makes file→DB ingest idempotent for agent-authored notes.
func TestSynthIngestID(t *testing.T) {
	a := synthIngestID("memory/note/x.md", "hello")
	b := synthIngestID("memory/note/x.md", "hello")
	if a != b {
		t.Fatalf("same inputs produced different ids: %q vs %q", a, b)
	}
	if synthIngestID("memory/note/x.md", "hello ") == synthIngestID("memory/note/x.md", "world") {
		t.Fatal("different bodies must produce different ids")
	}
	if synthIngestID("memory/note/x.md", "hello") == synthIngestID("memory/note/y.md", "hello") {
		t.Fatal("different paths must produce different ids")
	}
	// Whitespace-only differences are ignored (trim), matching runtimeHash semantics.
	if synthIngestID("p.md", "  hi  ") != synthIngestID("p.md", "hi") {
		t.Fatal("whitespace padding should not change the synthesised id")
	}
}

// TestIsProjectionItems verifies which memory files RebuildFiles may delete:
// only files whose every entry id carries the "<botID>:" prefix minted by the
// wiki store are DB projections (current or stale). Agent-authored files use
// unprefixed ids (per the _memory.md prompt) and must be preserved.
func TestIsProjectionItems(t *testing.T) {
	botID := "bot-1"
	cases := []struct {
		name  string
		items []memorydomain.Item
		want  bool
	}{
		{
			name:  "bot-prefixed projection is deletable",
			items: []memorydomain.Item{{ID: "bot-1:mem_1"}, {ID: "bot-1:mem_2"}},
			want:  true,
		},
		{
			name:  "stale projection of a deleted node is still deletable",
			items: []memorydomain.Item{{ID: "bot-1:mem_gone"}},
			want:  true,
		},
		{
			name:  "agent-authored unprefixed id is preserved",
			items: []memorydomain.Item{{ID: "mem_20260313_001"}},
			want:  false,
		},
		{
			name:  "mixed file with any unprefixed entry is preserved",
			items: []memorydomain.Item{{ID: "bot-1:mem_1"}, {ID: "mem_agent"}},
			want:  false,
		},
		{
			name:  "another bot's prefix is not ours to delete",
			items: []memorydomain.Item{{ID: "bot-2:mem_1"}},
			want:  false,
		},
		{
			name:  "no parsed entries is preserved",
			items: nil,
			want:  false,
		},
	}
	for _, tc := range cases {
		if got := isProjectionItems(botID, tc.items); got != tc.want {
			t.Errorf("%s: isProjectionItems = %v, want %v", tc.name, got, tc.want)
		}
	}
}
