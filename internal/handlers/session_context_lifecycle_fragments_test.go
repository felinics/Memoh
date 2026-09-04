package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type contextFragmentTextStub struct {
	contextLifecycleDecisionStub
	texts     map[string]sqlc.ListContextFragmentTextsRow
	requested []string
}

func (s *contextFragmentTextStub) ListContextFragmentTexts(_ context.Context, hashes []string) ([]sqlc.ListContextFragmentTextsRow, error) {
	s.requested = append(s.requested, hashes...)
	rows := make([]sqlc.ListContextFragmentTextsRow, 0, len(hashes))
	for _, hash := range hashes {
		if row, ok := s.texts[hash]; ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func fragmentSnapshotJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(contextfrag.LifecycleSnapshot{
		Version: contextfrag.LifecycleSnapshotVersion,
		Fragments: []contextfrag.FragmentRef{
			{Kind: contextfrag.KindSystemPrompt, Slot: contextfrag.SlotSystem, ContentHash: "sys", TokenEstimate: 40, TextBytes: 160},
			{Kind: contextfrag.KindWorkspaceInstruction, Slot: contextfrag.SlotSystem, ContentHash: "rules", TokenEstimate: 120, TextBytes: 480},
		},
		ToolDefs: []contextfrag.ToolDefAccounting{{Provider: "workspace", Name: "exec", Bytes: 90, TokenEstimate: 22, ContentHash: "tool-exec"}},
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return raw
}

func TestLoadContextLifecycleFragmentsJoinsStoredTextsToTheRunsRefs(t *testing.T) {
	t.Parallel()

	sessionID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	stub := &contextFragmentTextStub{
		contextLifecycleDecisionStub: contextLifecycleDecisionStub{run: sqlc.GetContextLifecycleByRunIDRow{RunID: runID, SessionID: sessionID, Snapshot: fragmentSnapshotJSON(t)}},
		texts: map[string]sqlc.ListContextFragmentTextsRow{
			"sys":       {ContentHash: "sys", Kind: "system_prompt", Label: "system.prompt.body", Text: "You are Memoh.", TextBytes: 14},
			"tool-exec": {ContentHash: "tool-exec", Kind: "tool_definition", Text: `{"name":"exec"}`, TextBytes: 15, Truncated: true},
		},
	}

	fragments, err := loadContextLifecycleFragments(context.Background(), stub, sessionID, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(fragments) != 3 {
		t.Fatalf("fragments = %#v, want two refs plus the tool definition", fragments)
	}
	if fragments[0].Label != "system.prompt.body" || !fragments[0].Available || fragments[0].Text != "You are Memoh." || fragments[0].TokenEstimate != 40 {
		t.Fatalf("system fragment = %#v", fragments[0])
	}
	if fragments[1].Label != "" || fragments[1].Kind != contextfrag.KindWorkspaceInstruction || fragments[1].Available || fragments[1].Text != "" || fragments[1].ContentHash != "rules" {
		t.Fatalf("missing text must stay unavailable, not empty-available: %#v", fragments[1])
	}
	if fragments[2].Label != "workspace/exec" || fragments[2].Kind != contextfrag.KindToolDefinition || !fragments[2].Truncated || fragments[2].Text != `{"name":"exec"}` {
		t.Fatalf("tool fragment = %#v", fragments[2])
	}
	sort.Strings(stub.requested)
	if len(stub.requested) != 3 || stub.requested[0] != "rules" || stub.requested[1] != "sys" || stub.requested[2] != "tool-exec" {
		t.Fatalf("requested hashes = %v", stub.requested)
	}
}

func TestLoadContextLifecycleFragmentsHidesForeignRuns(t *testing.T) {
	t.Parallel()

	sessionID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	stub := &contextFragmentTextStub{contextLifecycleDecisionStub: contextLifecycleDecisionStub{run: sqlc.GetContextLifecycleByRunIDRow{RunID: runID, SessionID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}, Snapshot: fragmentSnapshotJSON(t)}}}
	if _, err := loadContextLifecycleFragments(context.Background(), stub, sessionID, runID); !errors.Is(err, errContextLifecycleRunNotFound) {
		t.Fatalf("err = %v, want run-not-found", err)
	}
	if len(stub.requested) != 0 {
		t.Fatalf("texts were read for a foreign run")
	}
}

func TestContextFragmentPreviewsCoverEveryHashOnThePage(t *testing.T) {
	t.Parallel()

	turns := []ContextLifecycleTurn{
		{RunID: "run-1", Snapshot: contextfrag.LifecycleSnapshot{
			Fragments: []contextfrag.FragmentRef{{Kind: contextfrag.KindSystemPrompt, ContentHash: "sys"}, {Kind: contextfrag.KindWorkspaceInstruction, ContentHash: "rules"}},
			ToolDefs:  []contextfrag.ToolDefAccounting{{Provider: "workspace", Name: "exec", ContentHash: "tool-exec"}},
		}},
		{RunID: "run-2", Snapshot: contextfrag.LifecycleSnapshot{
			Fragments: []contextfrag.FragmentRef{{Kind: contextfrag.KindSystemPrompt, ContentHash: "sys"}},
		}},
	}
	queries := &contextLifecycleQueryStub{previewRows: []sqlc.ListContextFragmentPreviewsRow{
		{ContentHash: "sys", Kind: "system_prompt", Label: "system.prompt.body", Preview: "You are Memoh.", TextBytes: 14},
		{ContentHash: "tool-exec", Kind: "tool_definition", Preview: `{"name":"exec"}`, TextBytes: 15, Truncated: true},
	}}

	previews, err := contextFragmentPreviews(context.Background(), queries, turns)
	if err != nil {
		t.Fatalf("previews: %v", err)
	}
	if len(queries.previewParams) != 1 {
		t.Fatalf("preview queries = %d, want one for the whole page", len(queries.previewParams))
	}
	requested := append([]string(nil), queries.previewParams[0].ContentHashes...)
	sort.Strings(requested)
	if len(requested) != 3 || requested[0] != "rules" || requested[1] != "sys" || requested[2] != "tool-exec" || queries.previewParams[0].PreviewChars != contextFragmentPreviewChars {
		t.Fatalf("preview params = %#v", queries.previewParams[0])
	}
	if previews["sys"].Preview != "You are Memoh." || previews["sys"].Label != "system.prompt.body" || previews["sys"].Kind != contextfrag.KindSystemPrompt || previews["tool-exec"].Truncated != true || previews["tool-exec"].TextBytes != 15 {
		t.Fatalf("previews = %#v", previews)
	}
	if _, ok := previews["rules"]; ok {
		t.Fatalf("a hash without stored text must not appear in the map")
	}
	if empty, err := contextFragmentPreviews(context.Background(), queries, nil); err != nil || len(empty) != 0 || len(queries.previewParams) != 1 {
		t.Fatalf("a page without hashes must not query: %#v %v", empty, err)
	}
}
