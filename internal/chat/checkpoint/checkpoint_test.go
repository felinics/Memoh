package checkpoint

import (
	"encoding/json"
	"strings"
	"testing"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	testCheckpointID = "00000000-0000-0000-0000-0000000000c1"
	testSessionID    = "00000000-0000-0000-0000-0000000000a1"
	testRowID        = "00000000-0000-0000-0000-0000000000b1"
	testSummaryID    = "00000000-0000-0000-0000-0000000000d1"
)

func TestFromRowReadsItemsInOrder(t *testing.T) {
	row := sqlc.BotHistoryCheckpoint{
		ID:             dbpkg.ParseUUIDOrEmpty(testCheckpointID),
		SessionID:      dbpkg.ParseUUIDOrEmpty(testSessionID),
		TurnPosition:   4,
		TurnMessageSeq: 2,
		Items:          []byte(`[{"row":"` + testRowID + `"},{"summary":"` + testSummaryID + `"}]`),
	}
	checkpoint, err := fromRow(row)
	if err != nil {
		t.Fatalf("fromRow: %v", err)
	}
	if checkpoint.ID != testCheckpointID || checkpoint.ParentID != "" || checkpoint.Position.Turn != 4 || checkpoint.Position.Seq != 2 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	if rows, summaries := checkpoint.RowIDs(), checkpoint.SummaryIDs(); len(rows) != 1 || rows[0] != testRowID || len(summaries) != 1 || summaries[0] != testSummaryID {
		t.Fatalf("rows %v summaries %v, want the row then the summary", rows, summaries)
	}
}

func TestFromRowRefusesAnItemNamingNeitherOrBoth(t *testing.T) {
	for _, items := range []string{
		`[{}]`,
		`[{"row":"` + testRowID + `","summary":"` + testSummaryID + `"}]`,
	} {
		_, err := fromRow(sqlc.BotHistoryCheckpoint{ID: dbpkg.ParseUUIDOrEmpty(testCheckpointID), Items: []byte(items)})
		if err == nil || !strings.Contains(err.Error(), "neither one row nor one summary") {
			t.Fatalf("fromRow(%s) = %v, want a refusal", items, err)
		}
	}
}

func TestItemsStoreNoSummaryText(t *testing.T) {
	encoded, err := json.Marshal([]Item{{RowID: testRowID}, {SummaryID: testSummaryID, Summary: "not stored"}})
	if err != nil || string(encoded) != `[{"row":"`+testRowID+`"},{"summary":"`+testSummaryID+`"}]` {
		t.Fatalf("items = %s, %v", encoded, err)
	}
}
