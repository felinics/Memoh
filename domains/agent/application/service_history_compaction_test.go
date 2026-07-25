package application

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestReplaceCompactedHistoryRecordsPreservesOnlyRequiredSourceGroupAcrossRestack(t *testing.T) {
	t.Parallel()

	records := []history.HistoryRecord{
		historyRecord("a-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("a required")}, func(record *history.HistoryRecord) {
			record.CompactID = "artifact-a"
			record.Required = true
		}),
		historyRecord("a-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("a peer")}, func(record *history.HistoryRecord) {
			record.CompactID = "artifact-a"
		}),
		historyRecord("b-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("b old")}, func(record *history.HistoryRecord) {
			record.CompactID = "artifact-b"
		}),
		historyRecord("b-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("b old reply")}, func(record *history.HistoryRecord) {
			record.CompactID = "artifact-b"
		}),
	}
	terminal := compaction.Artifact{ID: "artifact-terminal", Summary: "restacked"}

	got := replaceCompactedHistoryRecordsWithService(records, fragment.Scope{}, func(history.HistoryRecord) (compaction.Artifact, bool) {
		return terminal, true
	})
	want := []string{"<summary>\nrestacked\n</summary>", "a required", "a peer"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("restacked required preservation = %#v, want %#v", gotTexts, want)
	}
}

func TestResolveCatalogArtifactRejectsDurableCoverageHashMismatch(t *testing.T) {
	t.Parallel()

	owner := compaction.ArtifactOwner{BotID: "bot-1", SessionID: "session-1", SessionIDKnown: true}
	record := historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("edited")}, func(record *history.HistoryRecord) {
		record.BotID = owner.BotID
		record.SessionID = owner.SessionID
		record.SessionIDKnown = true
		record.CompactID = "artifact-1"
		record.Ref.HashAlgo = fragment.HashAlgoSHA256
		record.Ref.HashScope = fragment.HashScopeSourcePayload
		record.Ref.ContentHash = "new-source-hash"
	})
	covered := record.Ref
	covered.ContentHash = "old-source-hash"
	artifact := compaction.Artifact{
		ID:        "artifact-1",
		BotID:     owner.BotID,
		SessionID: owner.SessionID,
		Summary:   "stale summary",
		Coverage:  []compaction.CoveredSource{{Ref: covered}},
	}
	catalog := compaction.NewArtifactCatalog()
	catalog.Add(owner, compaction.NewArtifactAliasFrontier(artifact.ID, artifact))

	if resolved, ok := resolveCatalogArtifact(catalog, owner, record); ok {
		t.Fatalf("hash-mismatched record resolved to stale artifact: %#v", resolved)
	}
	got := replaceCompactedHistoryRecordsWithService([]history.HistoryRecord{record}, fragment.Scope{}, func(record history.HistoryRecord) (compaction.Artifact, bool) {
		return resolveCatalogArtifact(catalog, owner, record)
	})
	if len(got) != 1 || got[0].ModelMessage.TextContent() != "edited" {
		t.Fatalf("hash-mismatched raw record was not preserved: %#v", got)
	}
}

func TestResolveCatalogArtifactRejectsDurableCoverageMetadataMismatch(t *testing.T) {
	t.Parallel()

	owner := compaction.ArtifactOwner{BotID: "bot-1", SessionID: "session-1", SessionIDKnown: true}
	record := historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("unchanged")}, func(record *history.HistoryRecord) {
		record.BotID = owner.BotID
		record.SessionID = owner.SessionID
		record.SessionIDKnown = true
		record.CompactID = "artifact-1"
		record.ExternalMessageID = "external-1"
		record.SourceReplyToMessageID = "reply-1"
		record.CreatedAt = time.UnixMilli(2)
	})
	artifact := compaction.Artifact{
		ID:        "artifact-1",
		BotID:     owner.BotID,
		SessionID: owner.SessionID,
		Summary:   "stale summary",
		Coverage: []compaction.CoveredSource{{
			Ref:                    record.Ref,
			ExternalMessageID:      record.ExternalMessageID,
			SourceReplyToMessageID: record.SourceReplyToMessageID,
			CreatedAtMs:            1,
		}},
	}
	catalog := compaction.NewArtifactCatalog()
	catalog.Add(owner, compaction.NewArtifactAliasFrontier(artifact.ID, artifact))

	if resolved, ok := resolveCatalogArtifact(catalog, owner, record); ok {
		t.Fatalf("metadata-mismatched record resolved to stale artifact: %#v", resolved)
	}
}

func TestReplaceCompactedMessagesRejectsArtifactWhenAnyLoadedSourceConflicts(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f501"
	artifactID := "00000000-0000-0000-0000-00000000c501"
	recordAID := "00000000-0000-0000-0000-000000000501"
	recordBID := "00000000-0000-0000-0000-000000000502"
	records := []history.HistoryRecord{
		historyRecord(recordAID, agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("raw a")}, func(record *history.HistoryRecord) {
			record.SessionID = sessionID
			record.SessionIDKnown = true
			record.CompactID = artifactID
		}),
		historyRecord(recordBID, agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("edited raw b")}, func(record *history.HistoryRecord) {
			record.SessionID = sessionID
			record.SessionIDKnown = true
			record.CompactID = artifactID
		}),
	}
	staleRef := records[1].Ref
	staleRef.ContentHash = "old-source-hash"
	coverage, err := json.Marshal([]compaction.CoveredSource{{Ref: records[0].Ref}, {Ref: staleRef}})
	if err != nil {
		t.Fatalf("marshal stale coverage: %v", err)
	}
	queries := &recordingCompactionLogQueries{logs: []compaction.ArtifactRecord{{
		ID:        artifactID,
		SessionID: sessionID,
		Status:    "ok",
		Summary:   "partially stale summary",
		Coverage:  coverage,
	}}}

	got := mustReplaceCompactedMessages(t, serviceWithCompactionReads(queries), sessionID, fragment.Scope{SessionID: sessionID}, records)
	want := []string{"raw a", "edited raw b"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("partially stale artifact remained active: %#v, want %#v", gotTexts, want)
	}
}

func TestReplaceCompactedMessagesRejectsArtifactForStaleCoverageFallback(t *testing.T) {
	t.Parallel()

	sessionID := "00000000-0000-0000-0000-00000000f502"
	artifactID := "00000000-0000-0000-0000-00000000c502"
	recordID := "00000000-0000-0000-0000-000000000503"
	record := historyRecord(recordID, agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("edited raw")}, func(record *history.HistoryRecord) {
		record.SessionID = sessionID
		record.SessionIDKnown = true
	})
	staleRef := record.Ref
	staleRef.ContentHash = "old-source-hash"
	coverage, err := json.Marshal([]compaction.CoveredSource{{Ref: staleRef}})
	if err != nil {
		t.Fatalf("marshal stale coverage: %v", err)
	}
	queries := &recordingCompactionLogQueries{logs: []compaction.ArtifactRecord{{
		ID:        artifactID,
		SessionID: sessionID,
		Status:    "ok",
		Summary:   "stale summary",
		Coverage:  coverage,
	}}}

	got := mustReplaceCompactedMessages(t, serviceWithCompactionReads(queries), sessionID, fragment.Scope{SessionID: sessionID}, []history.HistoryRecord{record})
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, []string{"edited raw"}) {
		t.Fatalf("stale coverage fallback remained active: %#v", gotTexts)
	}
}

func TestRecordArtifactOwnerPreservesKnownNullSession(t *testing.T) {
	t.Parallel()

	record := historyRecord("row-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("sessionless")}, func(record *history.HistoryRecord) {
		record.SessionID = ""
		record.SessionIDKnown = true
	})
	owner := recordArtifactOwner(record, fragment.Scope{SessionID: "fallback-session"})
	if !owner.SessionIDKnown || owner.SessionID != "" {
		t.Fatalf("known-null session was replaced by fallback: %#v", owner)
	}
}

func TestReplaceRecentCompactedMessagesKeepsArtifactResolutionOwnerBound(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-00000000b201"
	session1 := "00000000-0000-0000-0000-00000000f201"
	session2 := "00000000-0000-0000-0000-00000000f202"
	artifactID := "00000000-0000-0000-0000-00000000c201"
	corruptRawID := "00000000-0000-0000-0000-000000000201"
	queries := &recordingCompactionLogQueries{
		logs: []compaction.ArtifactRecord{{
			ID:        artifactID,
			BotID:     botID,
			SessionID: session2,
			Status:    "ok",
			Summary:   "session two summary",
			Coverage:  persistedCoverage(t, corruptRawID),
		}},
	}
	records := []history.HistoryRecord{
		historyRecord(corruptRawID, agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("session one raw")}, func(record *history.HistoryRecord) {
			record.BotID = botID
			record.SessionID = session1
			record.SessionIDKnown = true
			record.CompactID = artifactID
		}),
		historyRecord("session-two-trigger", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("session two raw")}, func(record *history.HistoryRecord) {
			record.BotID = botID
			record.SessionID = session2
			record.SessionIDKnown = true
		}),
	}

	got := mustReplaceCompactedMessages(t, serviceWithCompactionReads(queries), "", fragment.Scope{BotID: botID}, records)
	want := []string{"session one raw", "session two raw"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("cross-owner artifact replaced history: %#v, want %#v", gotTexts, want)
	}
}

func TestReplaceRecentCompactedMessagesLoadsEveryKnownNullSessionGroup(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-00000000b301"
	artifactAID := "00000000-0000-0000-0000-00000000c301"
	artifactBID := "00000000-0000-0000-0000-00000000c302"
	recordAID := "00000000-0000-0000-0000-000000000301"
	recordBID := "00000000-0000-0000-0000-000000000302"
	artifactA := compaction.ArtifactRecord{
		ID:       artifactAID,
		BotID:    botID,
		Status:   "ok",
		Summary:  "summary a",
		Coverage: persistedCoverage(t, recordAID),
	}
	artifactB := compaction.ArtifactRecord{
		ID:       artifactBID,
		BotID:    botID,
		Status:   "ok",
		Summary:  "summary b",
		Coverage: persistedCoverage(t, recordBID),
	}
	queries := &recordingCompactionLogQueries{
		byID: map[string]compaction.ArtifactRecord{
			artifactA.ID: artifactA,
			artifactB.ID: artifactB,
		},
	}
	records := []history.HistoryRecord{
		historyRecord(recordAID, agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("raw a")}, func(record *history.HistoryRecord) {
			record.BotID = botID
			record.SessionIDKnown = true
			record.CompactID = artifactAID
		}),
		historyRecord(recordBID, agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("raw b")}, func(record *history.HistoryRecord) {
			record.BotID = botID
			record.SessionIDKnown = true
			record.CompactID = artifactBID
		}),
	}

	got := mustReplaceCompactedMessages(t, serviceWithCompactionReads(queries), "", fragment.Scope{BotID: botID}, records)
	want := []string{"<summary>\nsummary a\n</summary>", "<summary>\nsummary b\n</summary>"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("known-null compact groups = %#v, want %#v", gotTexts, want)
	}
	if len(queries.getCalls) != 2 {
		t.Fatalf("point-loaded compact groups = %d, want 2: %#v", len(queries.getCalls), queries.getCalls)
	}
}

func anchoredSummaryRecord(id, summary string, anchor time.Time, scope fragment.Scope) history.HistoryRecord {
	artifact := compaction.Artifact{
		ID:            id,
		BotID:         scope.BotID,
		SessionID:     scope.SessionID,
		Summary:       summary,
		AnchorStartMs: anchor.UnixMilli(),
	}
	return artifact.HistoryRecord(scope)
}

func recordSequenceIDs(records []history.HistoryRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Ref.ID)
	}
	return ids
}

func TestPrependMissingCompactionSummariesMergesAtAnchorPositions(t *testing.T) {
	t.Parallel()

	scope := fragment.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record history.HistoryRecord, at time.Time) history.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []history.HistoryRecord{
		timed(historyRecord("m-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("first")}, nil), base.Add(10*time.Minute)),
		timed(historyRecord("m-2", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("second")}, nil), base.Add(20*time.Minute)),
		timed(historyRecord("m-3", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("third")}, nil), base.Add(30*time.Minute)),
	}
	summaries := []history.HistoryRecord{
		anchoredSummaryRecord("artifact-early", "early span", base.Add(time.Minute), scope),
		anchoredSummaryRecord("artifact-mid", "mid span", base.Add(25*time.Minute), scope),
	}

	got := mergeMissingCompactionSummaries(messages, summaries)

	want := []string{"artifact-early", "m-1", "m-2", "artifact-mid", "m-3"}
	if !reflect.DeepEqual(recordSequenceIDs(got), want) {
		t.Fatalf("aged-out summaries not merged at anchor positions:\n got %v\nwant %v", recordSequenceIDs(got), want)
	}
}

func TestPrependMissingCompactionSummariesDoesNotSplitToolExchanges(t *testing.T) {
	t.Parallel()

	scope := fragment.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record history.HistoryRecord, at time.Time) history.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []history.HistoryRecord{
		timed(historyRecord("m-call", agentdomain.ModelMessage{Role: "assistant", Content: agentdomain.NewTextContent("calling")}, nil), base.Add(10*time.Minute)),
		timed(historyRecord("m-result", agentdomain.ModelMessage{Role: "tool", Content: agentdomain.NewTextContent("result")}, nil), base.Add(20*time.Minute)),
		timed(historyRecord("m-after", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("after")}, nil), base.Add(30*time.Minute)),
	}
	summaries := []history.HistoryRecord{
		anchoredSummaryRecord("artifact-between", "between call and result", base.Add(15*time.Minute), scope),
	}

	got := mergeMissingCompactionSummaries(messages, summaries)

	want := []string{"m-call", "m-result", "artifact-between", "m-after"}
	if !reflect.DeepEqual(recordSequenceIDs(got), want) {
		t.Fatalf("summary split a tool exchange:\n got %v\nwant %v", recordSequenceIDs(got), want)
	}
}

func TestPrependMissingCompactionSummariesKeepsUnanchoredSummariesFirst(t *testing.T) {
	t.Parallel()

	scope := fragment.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record history.HistoryRecord, at time.Time) history.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []history.HistoryRecord{
		timed(historyRecord("m-1", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("first")}, nil), base.Add(10*time.Minute)),
	}
	summaries := []history.HistoryRecord{
		anchoredSummaryRecord("artifact-legacy", "legacy summary", time.UnixMilli(0), scope),
	}

	got := mergeMissingCompactionSummaries(messages, summaries)

	want := []string{"artifact-legacy", "m-1"}
	if !reflect.DeepEqual(recordSequenceIDs(got), want) {
		t.Fatalf("unanchored summary not kept first:\n got %v\nwant %v", recordSequenceIDs(got), want)
	}
}
