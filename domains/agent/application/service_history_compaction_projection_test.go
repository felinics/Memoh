package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestReplaceCompactedMessagesLoadsSessionSummaryWithoutRecentRows(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f003"
	compactID := "00000000-0000-0000-0000-00000000c003"
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:        compactID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "older condensed context",
			},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord("row-current", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("current")}, nil),
	}

	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

	if queries.sessionID != sessionID {
		t.Fatalf("queried session id = %#v, want %s", queries.sessionID, sessionID)
	}
	if len(got) != 2 {
		t.Fatalf("records = %d, want summary plus recent row: %#v", len(got), got)
	}
	if got[0].CompactID != compactID || got[0].Kind != fragment.KindConversationSummary || got[0].Lifecycle != history.LifecycleActiveSummary {
		t.Fatalf("first record is not loaded active summary: %#v", got[0])
	}
	if got[0].ModelMessage.TextContent() != "<summary>\nolder condensed context\n</summary>" {
		t.Fatalf("summary text mismatch: %q", got[0].ModelMessage.TextContent())
	}
	if got[1].DBMessageID != "row-current" {
		t.Fatalf("recent row lost or reordered: %#v", got)
	}
}

func TestReplaceCompactedMessagesOnlyRestoresArtifactsWhollyBeforeIntentionalCutoff(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f014"
	oldCompactID := "00000000-0000-0000-0000-00000000c014"
	unsafeCompactID := "00000000-0000-0000-0000-00000000c015"
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:            oldCompactID,
				SessionID:     sessionID,
				Status:        "ok",
				Summary:       "older condensed context",
				AnchorStartMs: base.Add(time.Minute).UnixMilli(),
				AnchorEndMs:   base.Add(5 * time.Minute).UnixMilli(),
			},
			{
				ID:            unsafeCompactID,
				SessionID:     sessionID,
				Status:        "ok",
				Summary:       "content excluded by the retry cutoff",
				AnchorStartMs: base.Add(15 * time.Minute).UnixMilli(),
				AnchorEndMs:   base.Add(25 * time.Minute).UnixMilli(),
			},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	loaded := []history.HistoryRecord{
		historyRecord("prior-user", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("prior")}, func(record *history.HistoryRecord) {
			record.CreatedAt = base.Add(10 * time.Minute)
			record.CompactID = unsafeCompactID
		}),
		historyRecord("cutoff-assistant", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("old answer")}, func(record *history.HistoryRecord) {
			record.CreatedAt = base.Add(20 * time.Minute)
			record.CompactID = unsafeCompactID
		}),
	}
	boundary := compactionArtifactBoundaryBeforeMessage(loaded, "cutoff-assistant")

	got, err := resolver.replaceCompactedMessages(
		context.Background(),
		sessionID,
		fragment.Scope{SessionID: sessionID},
		filterMessagesBeforeID(loaded, "cutoff-assistant"),
		boundary,
	)
	if err != nil {
		t.Fatalf("replaceCompactedMessages() error = %v", err)
	}
	want := []string{"<summary>\nolder condensed context\n</summary>", "prior"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("cutoff projection = %#v, want %#v", gotTexts, want)
	}
}

func TestReplaceCompactedMessagesLoadsSessionSummaryCoverageFromCompactedRows(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f004"
	compactID := "00000000-0000-0000-0000-00000000c004"
	coverage, err := json.Marshal([]compaction.CoveredSource{
		{
			Ref: fragment.ContextRef{
				Namespace:   "bot_history_message",
				ID:          "00000000-0000-0000-0000-000000000401",
				Version:     1,
				Schema:      fragment.SchemaContextRef,
				Durability:  fragment.RefDurable,
				ContentHash: "hash-401",
				HashAlgo:    fragment.HashAlgoSHA256,
				HashScope:   fragment.HashScopeSourcePayload,
			},
		},
		{
			Ref: fragment.ContextRef{
				Namespace:   "bot_history_message",
				ID:          "00000000-0000-0000-0000-000000000402",
				Version:     1,
				Schema:      fragment.SchemaContextRef,
				Durability:  fragment.RefDurable,
				ContentHash: "hash-402",
				HashAlgo:    fragment.HashAlgoSHA256,
				HashScope:   fragment.HashScopeSourcePayload,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal coverage: %v", err)
	}
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:        compactID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "older condensed context",
				Coverage:  coverage,
			},
		},
	}
	resolver := serviceWithCompactionReads(queries)

	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, nil)

	if len(got) != 1 {
		t.Fatalf("records = %d, want one session summary: %#v", len(got), got)
	}
	if got[0].Coverage == nil || len(got[0].Coverage.CoveredRefs) != 2 {
		t.Fatalf("summary coverage = %#v, want covered message refs", got[0].Coverage)
	}
	if got[0].Coverage.CoveredRefs[0].ID != "00000000-0000-0000-0000-000000000401" ||
		got[0].Coverage.CoveredRefs[1].ID != "00000000-0000-0000-0000-000000000402" {
		t.Fatalf("covered refs mismatch: %#v", got[0].Coverage.CoveredRefs)
	}
	if len(queries.refCalls) != 0 {
		t.Fatalf("persisted artifact coverage must not query message refs, called: %#v", queries.refCalls)
	}
	frags := historyContextFragsForMessages(history.ToModelMessages(got), got)
	if len(frags) != 1 || frags[0].Coverage == nil || len(frags[0].Coverage.CoveredRefs) != 2 {
		t.Fatalf("summary frag lost loaded coverage: %#v", frags)
	}
}

func TestReplaceCompactedMessagesRejectsMalformedPersistedCoverage(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f008"
	compactID := "00000000-0000-0000-0000-00000000c008"
	coveredID := "00000000-0000-0000-0000-000000000801"
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:        compactID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "recoverable condensed context",
				Coverage:  []byte(`{"unexpected":"shape"}`),
			},
		},
		refs: map[string][]string{
			compactID: {coveredID},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord(coveredID, agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("edited raw")}, func(record *history.HistoryRecord) {
			record.CompactID = compactID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

	if len(got) != 1 || got[0].DBMessageID != coveredID || got[0].ModelMessage.TextContent() != "edited raw" {
		t.Fatalf("malformed non-empty coverage replaced current raw history: %#v", got)
	}
	if len(queries.refCalls) != 0 {
		t.Fatalf("malformed non-empty coverage used legacy refs-only fallback: %#v", queries.refCalls)
	}
}

func TestReplaceCompactedMessagesInWindowGroupCoversRowsOutsideLoadWindow(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f007"
	compactID := "00000000-0000-0000-0000-00000000c007"
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:        compactID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "condensed context",
			},
		},
		refs: map[string][]string{
			compactID: {
				"00000000-0000-0000-0000-000000000501",
				"00000000-0000-0000-0000-000000000502",
			},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	// Only the newer half of the compact group is inside the loaded window; row
	// ...501 already aged out of the 24h load slice but is still part of the group.
	recent := []history.HistoryRecord{
		historyRecord("00000000-0000-0000-0000-000000000502", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("old")}, func(r *history.HistoryRecord) {
			r.CompactID = compactID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

	if len(got) != 1 || got[0].Kind != fragment.KindConversationSummary {
		t.Fatalf("expected single in-window summary record, got %#v", got)
	}
	if got[0].Coverage == nil || len(got[0].Coverage.CoveredRefs) != 2 {
		t.Fatalf("in-window summary should cover the full compact group, got %#v", got[0].Coverage)
	}
	if got[0].Coverage.CoveredRefs[0].ID != "00000000-0000-0000-0000-000000000501" {
		t.Fatalf("covered refs should include the row outside the load window: %#v", got[0].Coverage.CoveredRefs)
	}
	for _, ref := range got[0].Coverage.CoveredRefs {
		if ref.ContentHash != "" || ref.HashAlgo != "" || ref.HashScope != "" {
			t.Fatalf("refs-only legacy coverage must not claim a source hash: %#v", ref)
		}
	}
	if len(queries.refCalls) != 1 || queries.refCalls[0] != compactID {
		t.Fatalf("refs-only query should be called once for the compact group, got: %#v", queries.refCalls)
	}
}

func TestReplaceCompactedMessagesResolvesInWindowGroupsFromSessionLogs(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f005"
	inWindowCompact := "00000000-0000-0000-0000-00000000c005"
	outOfWindowCompact := "00000000-0000-0000-0000-00000000c006"
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:        inWindowCompact,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "in-window condensed context",
			},
			{
				ID:        outOfWindowCompact,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "aged-out condensed context",
			},
		},
		refs: map[string][]string{
			inWindowCompact:    {"00000000-0000-0000-0000-000000000600"},
			outOfWindowCompact: {"00000000-0000-0000-0000-000000000601"},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord("row-compacted", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("old")}, func(r *history.HistoryRecord) {
			r.CompactID = inWindowCompact
		}),
		historyRecord("row-current", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("current")}, nil),
	}

	// The fake does not implement GetCompactionLogByID: resolving in-window
	// groups must come from the single session log load, not per-group lookups.
	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

	if queries.listCalls != 1 {
		t.Fatalf("session logs loaded %d times, want exactly once", queries.listCalls)
	}
	if len(got) != 3 {
		t.Fatalf("records = %d, want prepended summary + in-window summary + recent row: %#v", len(got), got)
	}
	if got[0].CompactID != outOfWindowCompact || got[0].Kind != fragment.KindConversationSummary {
		t.Fatalf("first record should be the aged-out session summary: %#v", got[0])
	}
	if got[1].CompactID != inWindowCompact || got[1].Kind != fragment.KindConversationSummary {
		t.Fatalf("in-window group was not replaced by its summary: %#v", got[1])
	}
	if got[2].DBMessageID != "row-current" {
		t.Fatalf("recent row lost or reordered: %#v", got)
	}
	wantRefCalls := map[string]bool{
		inWindowCompact:    true,
		outOfWindowCompact: true,
	}
	if len(queries.refCalls) != len(wantRefCalls) {
		t.Fatalf("refs-only query calls = %#v, want exactly one per compact group %#v", queries.refCalls, wantRefCalls)
	}
	for _, called := range queries.refCalls {
		if !wantRefCalls[called] {
			t.Fatalf("unexpected refs-only query for compact id: %#v", called)
		}
	}
}

func TestReplaceCompactedMessagesResolvesSupersededGroupToActiveArtifact(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f009"
	parentID := "00000000-0000-0000-0000-00000000c009"
	activeID := "00000000-0000-0000-0000-00000000c010"
	coverage := persistedCoverage(t, "row-parent")
	supersededAt := time.Unix(10, 0)
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{
			{
				ID:           parentID,
				SessionID:    sessionID,
				Status:       "ok",
				Summary:      "stale parent summary",
				Coverage:     coverage,
				SupersededBy: activeID,
				SupersededAt: supersededAt,
			},
			{
				ID:        activeID,
				SessionID: sessionID,
				Status:    "ok",
				Summary:   "active restacked summary",
				Coverage:  coverage,
				ParentIDs: []string{parentID},
			},
		},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord("row-parent", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("covered by parent")}, func(record *history.HistoryRecord) {
			record.CompactID = parentID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

	if len(got) != 1 || got[0].CompactID != activeID {
		t.Fatalf("superseded raw group must resolve once to active artifact %s: %#v", activeID, got)
	}
	if got[0].ModelMessage.TextContent() != "<summary>\nactive restacked summary\n</summary>" {
		t.Fatalf("resolved summary = %q", got[0].ModelMessage.TextContent())
	}
}

func TestReplaceCompactedMessagesReconcilesStaleRawRowsByDurableCoverage(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f010"
	compactID := "00000000-0000-0000-0000-00000000c013"
	coveredID := "00000000-0000-0000-0000-000000000910"
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%v", required), func(t *testing.T) {
			t.Parallel()
			queries := &recordingCompactionLogQueries{
				logs: []compaction.ArtifactRecord{{
					ID:        compactID,
					SessionID: sessionID,
					Status:    "ok",
					Summary:   "newly completed summary",
					Coverage:  persistedCoverage(t, coveredID),
				}},
			}
			resolver := serviceWithCompactionReads(queries)
			recent := []history.HistoryRecord{
				historyRecord("before-row", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("before")}, nil),
				historyRecord(coveredID, agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("stale raw")}, func(record *history.HistoryRecord) {
					record.Required = required
				}),
				historyRecord("after-row", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("after")}, nil),
			}

			got := mustReplaceCompactedMessages(t, resolver, sessionID, fragment.Scope{SessionID: sessionID}, recent)

			wantText := []string{"before", "<summary>\nnewly completed summary\n</summary>"}
			if required {
				wantText = append(wantText, "stale raw")
			}
			wantText = append(wantText, "after")
			if gotText := recordTexts(got); !reflect.DeepEqual(gotText, wantText) {
				t.Fatalf("reconciled history = %#v, want %#v", gotText, wantText)
			}
		})
	}
}

func TestReplaceCompactedMessagesPropagatesFrontierStorageError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("frontier database unavailable")
	resolver := serviceWithCompactionReads(&recordingCompactionLogQueries{listErr: sentinel})
	_, err := resolver.replaceCompactedMessages(
		context.Background(),
		"00000000-0000-0000-0000-00000000f011",
		fragment.Scope{},
		[]history.HistoryRecord{historyRecord("row-current", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("current")}, nil)},
		compactionArtifactBoundary{},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("replaceCompactedMessages error = %v, want %v", err, sentinel)
	}
}
