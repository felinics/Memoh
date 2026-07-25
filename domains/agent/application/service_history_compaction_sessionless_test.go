package application

import (
	"reflect"
	"testing"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/compaction"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestReplaceRecentCompactedMessagesFollowsSupersession(t *testing.T) {
	t.Parallel()

	parentID := "00000000-0000-0000-0000-00000000c011"
	activeID := "00000000-0000-0000-0000-00000000c012"
	coverage := persistedCoverage(t, "row-parent")
	parent := compaction.ArtifactRecord{
		ID:           parentID,
		Status:       "ok",
		Summary:      "stale parent summary",
		Coverage:     coverage,
		SupersededBy: activeID,
		SupersededAt: time.Unix(10, 0),
	}
	active := compaction.ArtifactRecord{
		ID:        activeID,
		Status:    "ok",
		Summary:   "active restacked summary",
		Coverage:  coverage,
		ParentIDs: []string{parentID},
	}
	queries := &recordingCompactionLogQueries{
		byID: map[string]compaction.ArtifactRecord{
			parent.ID: parent,
			active.ID: active,
		},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord("row-parent", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("covered by parent")}, func(record *history.HistoryRecord) {
			record.CompactID = parentID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, "", fragment.Scope{}, recent)

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
	foreign := compaction.ArtifactRecord{
		ID:        foreignCompactID,
		BotID:     botID,
		SessionID: foreignSessionID,
		Status:    "ok",
		Summary:   "foreign session summary",
	}
	queries := &recordingCompactionLogQueries{
		byID: map[string]compaction.ArtifactRecord{foreign.ID: foreign},
	}
	resolver := serviceWithCompactionReads(queries)
	recent := []history.HistoryRecord{
		historyRecord("row-owned-by-session", agentdomain.ModelMessage{Role: "user", Content: agentdomain.NewTextContent("keep local raw")}, func(record *history.HistoryRecord) {
			record.BotID = botID
			record.SessionID = recordSessionID
			record.CompactID = foreignCompactID
		}),
	}

	got := mustReplaceCompactedMessages(t, resolver, "", fragment.Scope{BotID: botID}, recent)

	if gotText := recordTexts(got); !reflect.DeepEqual(gotText, []string{"keep local raw"}) {
		t.Fatalf("cross-session artifact replaced local history: %#v", gotText)
	}
	if len(queries.getCalls) != 0 {
		t.Fatalf("session-owned group fell back to unscoped point lookup: %#v", queries.getCalls)
	}
}
