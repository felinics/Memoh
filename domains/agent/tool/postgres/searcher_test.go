package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/domains/agent/tool"
)

type fakeQueries struct {
	params dbsqlc.SearchMessagesParams
	rows   []dbsqlc.SearchMessagesRow
	calls  int
}

func (f *fakeQueries) SearchMessages(_ context.Context, params dbsqlc.SearchMessagesParams) ([]dbsqlc.SearchMessagesRow, error) {
	f.params = params
	f.calls++
	return f.rows, nil
}

func TestSearcherMapsFilterAndRows(t *testing.T) {
	t.Parallel()

	const (
		botID     = "11111111-1111-1111-1111-111111111111"
		sessionID = "22222222-2222-2222-2222-222222222222"
		messageID = "33333333-3333-3333-3333-333333333333"
		contactID = "44444444-4444-4444-4444-444444444444"
	)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	content := []byte(`{"role":"user","content":"matching text"}`)
	queries := &fakeQueries{
		rows: []dbsqlc.SearchMessagesRow{{
			ID:                      testUUID(t, messageID),
			SessionID:               testUUID(t, sessionID),
			SenderChannelIdentityID: testUUID(t, contactID),
			Role:                    "user",
			Content:                 content,
			CreatedAt:               pgtype.Timestamptz{Time: createdAt, Valid: true},
			SenderDisplayName:       pgtype.Text{String: "Ada", Valid: true},
			Platform:                pgtype.Text{String: "telegram", Valid: true},
		}},
	}
	searcher := NewSearcher(queries)

	results, err := searcher.SearchHistory(context.Background(), tool.HistorySearchFilter{
		BotID:     botID,
		SessionID: sessionID,
		ContactID: "not-a-uuid",
		Role:      "user",
		Keyword:   "matching",
		StartTime: start,
		EndTime:   end,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("SearchHistory() error = %v", err)
	}

	params := queries.params
	if params.BotID.String() != botID || params.SessionID.String() != sessionID {
		t.Fatalf("UUID params = (%s, %s), want (%s, %s)", params.BotID.String(), params.SessionID.String(), botID, sessionID)
	}
	if params.ContactID.Valid {
		t.Fatalf("contact ID = %#v, want invalid optional UUID", params.ContactID)
	}
	if !params.StartTime.Valid || !params.StartTime.Time.Equal(start) ||
		!params.EndTime.Valid || !params.EndTime.Time.Equal(end) {
		t.Fatalf("time params = (%#v, %#v), want requested range", params.StartTime, params.EndTime)
	}
	if !params.Role.Valid || params.Role.String != "user" ||
		!params.Keyword.Valid || params.Keyword.String != "matching" ||
		params.MaxCount != 100 {
		t.Fatalf("query params = %#v, want requested text filters and limit", params)
	}

	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	result := results[0]
	if result.ID != messageID ||
		result.SessionID != sessionID ||
		result.ContactID != contactID ||
		result.Role != "user" ||
		result.CreatedAt != createdAt ||
		result.Sender != "Ada" ||
		result.Platform != "telegram" ||
		string(result.Content) != string(content) {
		t.Fatalf("result = %#v, want converted SQLC row", result)
	}
	content[0] = '!'
	if result.Content[0] == '!' {
		t.Fatal("result content aliases SQLC row content")
	}
}

func TestSearcherRejectsInvalidBotID(t *testing.T) {
	t.Parallel()

	queries := &fakeQueries{}
	_, err := NewSearcher(queries).SearchHistory(context.Background(), tool.HistorySearchFilter{
		BotID: "not-a-uuid",
		Limit: 50,
	})
	if err == nil || err.Error() != "invalid bot_id" {
		t.Fatalf("SearchHistory() error = %v, want invalid bot_id", err)
	}
	if queries.calls != 0 {
		t.Fatalf("query calls = %d, want 0", queries.calls)
	}
}

func TestSearcherLeavesOptionalFiltersUnset(t *testing.T) {
	t.Parallel()

	queries := &fakeQueries{}
	_, err := NewSearcher(queries).SearchHistory(context.Background(), tool.HistorySearchFilter{
		BotID: "11111111-1111-1111-1111-111111111111",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("SearchHistory() error = %v", err)
	}

	params := queries.params
	if params.SessionID.Valid ||
		params.ContactID.Valid ||
		params.StartTime.Valid ||
		params.EndTime.Valid ||
		params.Role.Valid ||
		params.Keyword.Valid {
		t.Fatalf("optional params = %#v, want all unset", params)
	}
}

func testUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}
