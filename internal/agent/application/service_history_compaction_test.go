package application

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestReplaceCompactedHistoryRecordsPreservesOnlyRequiredSourceGroupAcrossRestack(t *testing.T) {
	t.Parallel()

	records := []historyfrag.HistoryRecord{
		historyRecord("a-1", ModelMessage{Role: "user", Content: newTextContent("a required")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "artifact-a"
			record.Required = true
		}),
		historyRecord("a-2", ModelMessage{Role: "assistant", Content: newTextContent("a peer")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "artifact-a"
		}),
		historyRecord("b-1", ModelMessage{Role: "user", Content: newTextContent("b old")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "artifact-b"
		}),
		historyRecord("b-2", ModelMessage{Role: "assistant", Content: newTextContent("b old reply")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = "artifact-b"
		}),
	}
	terminal := compaction.Artifact{ID: "artifact-terminal", Summary: "restacked"}

	got := replaceCompactedHistoryRecordsWithService(records, contextfrag.Scope{}, func(historyfrag.HistoryRecord) (compaction.Artifact, bool) {
		return terminal, true
	})
	want := []string{"<summary>\nrestacked\n</summary>", "a required", "a peer"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("restacked required preservation = %#v, want %#v", gotTexts, want)
	}
}

func TestReplaceCompactedHistoryRecordsFoldsFusedParentsAroundMustKeepIsland(t *testing.T) {
	t.Parallel()

	const (
		parentAID = "artifact-a"
		parentBID = "artifact-b"
		rollupID  = "artifact-rollup"
	)
	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"}
	owner := compaction.ArtifactOwner{BotID: scope.BotID, SessionID: scope.SessionID, SessionIDKnown: true}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record historyfrag.HistoryRecord, at time.Time) historyfrag.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	records := []historyfrag.HistoryRecord{
		timed(historyRecord("a-1", ModelMessage{Role: "user", Content: newTextContent("a question")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = parentAID
		}), base.Add(time.Minute)),
		timed(historyRecord("a-2", ModelMessage{Role: "assistant", Content: newTextContent("a answer")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = parentAID
		}), base.Add(2*time.Minute)),
		timed(historyRecord("island-call", ModelMessage{Role: "assistant", Content: newTextContent("ask you something")}, func(record *historyfrag.HistoryRecord) {
			record.Required = true
		}), base.Add(3*time.Minute)),
		timed(historyRecord("island-result", ModelMessage{Role: "tool", Content: newTextContent("answered")}, nil), base.Add(4*time.Minute)),
		timed(historyRecord("b-1", ModelMessage{Role: "user", Content: newTextContent("b question")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = parentBID
		}), base.Add(5*time.Minute)),
		timed(historyRecord("b-2", ModelMessage{Role: "assistant", Content: newTextContent("b answer")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = parentBID
		}), base.Add(6*time.Minute)),
	}

	coverage := make([]compaction.CoveredSource, 0, 4)
	for _, index := range []int{0, 1, 4, 5} {
		record := records[index]
		coverage = append(coverage, compaction.CoveredSource{
			Ref:                    record.Ref,
			ExternalMessageID:      record.ExternalMessageID,
			SourceReplyToMessageID: record.SourceReplyToMessageID,
			CreatedAtMs:            record.CreatedAt.UnixMilli(),
		})
	}
	rollup := compaction.Artifact{
		ID:            rollupID,
		BotID:         scope.BotID,
		SessionID:     scope.SessionID,
		Status:        "ok",
		Summary:       "fused earlier context",
		Coverage:      coverage,
		AnchorStartMs: coverage[0].CreatedAtMs,
		AnchorEndMs:   coverage[len(coverage)-1].CreatedAtMs,
	}
	catalog := compaction.NewArtifactCatalog()
	catalog.Add(owner, compaction.NewArtifactAliasFrontier(parentAID, rollup))
	catalog.Add(owner, compaction.NewArtifactAliasFrontier(parentBID, rollup))

	got := replaceCompactedHistoryRecordsWithService(records, scope, func(record historyfrag.HistoryRecord) (compaction.Artifact, bool) {
		return resolveCatalogArtifact(catalog, recordArtifactOwner(record, scope), record)
	})

	wantIDs := []string{rollupID, "island-call", "island-result"}
	if gotIDs := recordSequenceIDs(got); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("fused-parent replacement ids = %#v, want %#v", gotIDs, wantIDs)
	}
	wantTexts := []string{"<summary>\nfused earlier context\n</summary>", "ask you something", "answered"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, wantTexts) {
		t.Fatalf("fused-parent replacement texts = %#v, want %#v", gotTexts, wantTexts)
	}
}

func TestResolveCatalogArtifactRejectsDurableCoverageHashMismatch(t *testing.T) {
	t.Parallel()

	owner := compaction.ArtifactOwner{BotID: "bot-1", SessionID: "session-1", SessionIDKnown: true}
	record := historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("edited")}, func(record *historyfrag.HistoryRecord) {
		record.BotID = owner.BotID
		record.SessionID = owner.SessionID
		record.SessionIDKnown = true
		record.CompactID = "artifact-1"
		record.Ref.HashAlgo = contextfrag.HashAlgoSHA256
		record.Ref.HashScope = contextfrag.HashScopeSourcePayload
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
	got := replaceCompactedHistoryRecordsWithService([]historyfrag.HistoryRecord{record}, contextfrag.Scope{}, func(record historyfrag.HistoryRecord) (compaction.Artifact, bool) {
		return resolveCatalogArtifact(catalog, owner, record)
	})
	if len(got) != 1 || got[0].ModelMessage.TextContent() != "edited" {
		t.Fatalf("hash-mismatched raw record was not preserved: %#v", got)
	}
}

func TestResolveCatalogArtifactRejectsDurableCoverageMetadataMismatch(t *testing.T) {
	t.Parallel()

	owner := compaction.ArtifactOwner{BotID: "bot-1", SessionID: "session-1", SessionIDKnown: true}
	record := historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("unchanged")}, func(record *historyfrag.HistoryRecord) {
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
	records := []historyfrag.HistoryRecord{
		historyRecord(recordAID, ModelMessage{Role: "user", Content: newTextContent("raw a")}, func(record *historyfrag.HistoryRecord) {
			record.SessionID = sessionID
			record.SessionIDKnown = true
			record.CompactID = artifactID
		}),
		historyRecord(recordBID, ModelMessage{Role: "assistant", Content: newTextContent("edited raw b")}, func(record *historyfrag.HistoryRecord) {
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
	queries := &recordingCompactionLogQueries{logs: []sqlc.BotHistoryMessageCompact{{
		ID:        mustPGUUID(t, artifactID),
		SessionID: mustPGUUID(t, sessionID),
		Status:    "ok",
		Summary:   "partially stale summary",
		Coverage:  coverage,
	}}}

	got := mustReplaceCompactedMessages(t, &Service{queries: queries}, sessionID, contextfrag.Scope{SessionID: sessionID}, records)
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
	record := historyRecord(recordID, ModelMessage{Role: "user", Content: newTextContent("edited raw")}, func(record *historyfrag.HistoryRecord) {
		record.SessionID = sessionID
		record.SessionIDKnown = true
	})
	staleRef := record.Ref
	staleRef.ContentHash = "old-source-hash"
	coverage, err := json.Marshal([]compaction.CoveredSource{{Ref: staleRef}})
	if err != nil {
		t.Fatalf("marshal stale coverage: %v", err)
	}
	queries := &recordingCompactionLogQueries{logs: []sqlc.BotHistoryMessageCompact{{
		ID:        mustPGUUID(t, artifactID),
		SessionID: mustPGUUID(t, sessionID),
		Status:    "ok",
		Summary:   "stale summary",
		Coverage:  coverage,
	}}}

	got := mustReplaceCompactedMessages(t, &Service{queries: queries}, sessionID, contextfrag.Scope{SessionID: sessionID}, []historyfrag.HistoryRecord{record})
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, []string{"edited raw"}) {
		t.Fatalf("stale coverage fallback remained active: %#v", gotTexts)
	}
}

func TestRecordArtifactOwnerPreservesKnownNullSession(t *testing.T) {
	t.Parallel()

	record := historyRecord("row-1", ModelMessage{Role: "user", Content: newTextContent("sessionless")}, func(record *historyfrag.HistoryRecord) {
		record.SessionID = ""
		record.SessionIDKnown = true
	})
	owner := recordArtifactOwner(record, contextfrag.Scope{SessionID: "fallback-session"})
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
		logs: []sqlc.BotHistoryMessageCompact{{
			ID:        mustPGUUID(t, artifactID),
			BotID:     mustPGUUID(t, botID),
			SessionID: mustPGUUID(t, session2),
			Status:    "ok",
			Summary:   "session two summary",
			Coverage:  persistedCoverage(t, corruptRawID),
		}},
	}
	records := []historyfrag.HistoryRecord{
		historyRecord(corruptRawID, ModelMessage{Role: "user", Content: newTextContent("session one raw")}, func(record *historyfrag.HistoryRecord) {
			record.BotID = botID
			record.SessionID = session1
			record.SessionIDKnown = true
			record.CompactID = artifactID
		}),
		historyRecord("session-two-trigger", ModelMessage{Role: "user", Content: newTextContent("session two raw")}, func(record *historyfrag.HistoryRecord) {
			record.BotID = botID
			record.SessionID = session2
			record.SessionIDKnown = true
		}),
	}

	got := mustReplaceCompactedMessages(t, &Service{queries: queries}, "", contextfrag.Scope{BotID: botID}, records)
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
	artifactA := sqlc.BotHistoryMessageCompact{
		ID:       mustPGUUID(t, artifactAID),
		BotID:    mustPGUUID(t, botID),
		Status:   "ok",
		Summary:  "summary a",
		Coverage: persistedCoverage(t, recordAID),
	}
	artifactB := sqlc.BotHistoryMessageCompact{
		ID:       mustPGUUID(t, artifactBID),
		BotID:    mustPGUUID(t, botID),
		Status:   "ok",
		Summary:  "summary b",
		Coverage: persistedCoverage(t, recordBID),
	}
	queries := &recordingCompactionLogQueries{
		byID: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
			artifactA.ID: artifactA,
			artifactB.ID: artifactB,
		},
	}
	records := []historyfrag.HistoryRecord{
		historyRecord(recordAID, ModelMessage{Role: "user", Content: newTextContent("raw a")}, func(record *historyfrag.HistoryRecord) {
			record.BotID = botID
			record.SessionIDKnown = true
			record.CompactID = artifactAID
		}),
		historyRecord(recordBID, ModelMessage{Role: "assistant", Content: newTextContent("raw b")}, func(record *historyfrag.HistoryRecord) {
			record.BotID = botID
			record.SessionIDKnown = true
			record.CompactID = artifactBID
		}),
	}

	got := mustReplaceCompactedMessages(t, &Service{queries: queries}, "", contextfrag.Scope{BotID: botID}, records)
	want := []string{"<summary>\nsummary a\n</summary>", "<summary>\nsummary b\n</summary>"}
	if gotTexts := recordTexts(got); !reflect.DeepEqual(gotTexts, want) {
		t.Fatalf("known-null compact groups = %#v, want %#v", gotTexts, want)
	}
	if len(queries.getCalls) != 2 {
		t.Fatalf("point-loaded compact groups = %d, want 2: %#v", len(queries.getCalls), queries.getCalls)
	}
}

func anchoredSummaryRecord(id, summary string, anchor time.Time, scope contextfrag.Scope) historyfrag.HistoryRecord {
	artifact := compaction.Artifact{
		ID:            id,
		BotID:         scope.BotID,
		SessionID:     scope.SessionID,
		Summary:       summary,
		AnchorStartMs: anchor.UnixMilli(),
	}
	return artifact.HistoryRecord(scope)
}

func recordSequenceIDs(records []historyfrag.HistoryRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.Ref.ID)
	}
	return ids
}

func TestPrependMissingCompactionSummariesMergesAtAnchorPositions(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record historyfrag.HistoryRecord, at time.Time) historyfrag.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []historyfrag.HistoryRecord{
		timed(historyRecord("m-1", ModelMessage{Role: "user", Content: newTextContent("first")}, nil), base.Add(10*time.Minute)),
		timed(historyRecord("m-2", ModelMessage{Role: "assistant", Content: newTextContent("second")}, nil), base.Add(20*time.Minute)),
		timed(historyRecord("m-3", ModelMessage{Role: "user", Content: newTextContent("third")}, nil), base.Add(30*time.Minute)),
	}
	summaries := []historyfrag.HistoryRecord{
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

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record historyfrag.HistoryRecord, at time.Time) historyfrag.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []historyfrag.HistoryRecord{
		timed(historyRecord("m-call", ModelMessage{Role: "assistant", Content: newTextContent("calling")}, nil), base.Add(10*time.Minute)),
		timed(historyRecord("m-result", ModelMessage{Role: "tool", Content: newTextContent("result")}, nil), base.Add(20*time.Minute)),
		timed(historyRecord("m-after", ModelMessage{Role: "user", Content: newTextContent("after")}, nil), base.Add(30*time.Minute)),
	}
	summaries := []historyfrag.HistoryRecord{
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

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	timed := func(record historyfrag.HistoryRecord, at time.Time) historyfrag.HistoryRecord {
		record.CreatedAt = at
		return record
	}
	messages := []historyfrag.HistoryRecord{
		timed(historyRecord("m-1", ModelMessage{Role: "user", Content: newTextContent("first")}, nil), base.Add(10*time.Minute)),
	}
	summaries := []historyfrag.HistoryRecord{
		anchoredSummaryRecord("artifact-legacy", "legacy summary", time.UnixMilli(0), scope),
	}

	got := mergeMissingCompactionSummaries(messages, summaries)

	want := []string{"artifact-legacy", "m-1"}
	if !reflect.DeepEqual(recordSequenceIDs(got), want) {
		t.Fatalf("unanchored summary not kept first:\n got %v\nwant %v", recordSequenceIDs(got), want)
	}
}
