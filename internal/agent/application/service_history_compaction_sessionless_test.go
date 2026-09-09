package application

import (
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestReplaceRecentCompactedMessagesFollowsSupersession(t *testing.T) {
	t.Parallel()

	parentID := "00000000-0000-0000-0000-00000000c011"
	activeID := "00000000-0000-0000-0000-00000000c012"
	coverage := persistedCoverage(t, "row-parent")
	parent := sqlc.BotHistoryMessageCompact{
		ID:           mustPGUUID(t, parentID),
		Status:       "ok",
		Summary:      "stale parent summary",
		Coverage:     coverage,
		SupersededBy: mustPGUUID(t, activeID),
		SupersededAt: pgtype.Timestamptz{Time: time.Unix(10, 0), Valid: true},
	}
	active := sqlc.BotHistoryMessageCompact{
		ID:        mustPGUUID(t, activeID),
		Status:    "ok",
		Summary:   "active restacked summary",
		Coverage:  coverage,
		ParentIds: []pgtype.UUID{mustPGUUID(t, parentID)},
	}
	queries := &recordingCompactionLogQueries{
		byID: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{
			parent.ID: parent,
			active.ID: active,
		},
	}
	resolver := &Service{queries: queries}
	recent := []historyfrag.HistoryRecord{
		historyRecord("row-parent", ModelMessage{Role: "user", Content: newTextContent("covered by parent")}, func(record *historyfrag.HistoryRecord) {
			record.CompactID = parentID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, "", contextfrag.Scope{}, recent)

	if len(got) != 1 || got[0].CompactID != activeID {
		t.Fatalf("sessionless superseded group must resolve to active artifact %s: %#v", activeID, got)
	}
	if len(queries.getCalls) != 2 || queries.getCalls[0] != parent.ID || queries.getCalls[1] != active.ID {
		t.Fatalf("lineage lookups = %#v, want parent then active", queries.getCalls)
	}
}

func TestReplaceRecentCompactedMessagesUsesRecordSessionAsOwner(t *testing.T) {
	t.Parallel()

	botID := "00000000-0000-0000-0000-00000000b101"
	recordSessionID := "00000000-0000-0000-0000-00000000f101"
	foreignSessionID := "00000000-0000-0000-0000-00000000f102"
	foreignCompactID := "00000000-0000-0000-0000-00000000c101"
	foreign := sqlc.BotHistoryMessageCompact{
		ID:        mustPGUUID(t, foreignCompactID),
		BotID:     mustPGUUID(t, botID),
		SessionID: mustPGUUID(t, foreignSessionID),
		Status:    "ok",
		Summary:   "foreign session summary",
	}
	queries := &recordingCompactionLogQueries{
		byID: map[pgtype.UUID]sqlc.BotHistoryMessageCompact{foreign.ID: foreign},
	}
	resolver := &Service{queries: queries}
	recent := []historyfrag.HistoryRecord{
		historyRecord("row-owned-by-session", ModelMessage{Role: "user", Content: newTextContent("keep local raw")}, func(record *historyfrag.HistoryRecord) {
			record.BotID = botID
			record.SessionID = recordSessionID
			record.CompactID = foreignCompactID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, "", contextfrag.Scope{BotID: botID}, recent)

	if gotText := recordTexts(got); !reflect.DeepEqual(gotText, []string{"keep local raw"}) {
		t.Fatalf("cross-session artifact replaced local history: %#v", gotText)
	}
	if len(queries.getCalls) != 0 {
		t.Fatalf("session-owned group fell back to unscoped point lookup: %#v", queries.getCalls)
	}
}
