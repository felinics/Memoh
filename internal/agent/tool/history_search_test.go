package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	thread "github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func TestHistorySearchExplicitCursorIgnoresUnrelatedSessionChanges(t *testing.T) {
	q := &historySearchQueries{rows: []sqlc.SearchMessagesRow{
		{ID: dbpkg.ParseUUIDOrEmpty(uuid.NewString()), SessionID: dbpkg.ParseUUIDOrEmpty(searchTestSessionID), CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}},
		{ID: dbpkg.ParseUUIDOrEmpty(uuid.NewString()), SessionID: dbpkg.ParseUUIDOrEmpty(searchTestSessionID), CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Second), Valid: true}},
	}}
	current := thread.Thread{ID: searchTestSessionID, CreatedByUserID: "owner"}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{sessions: []thread.Thread{current}}, nil, q)
	args := map[string]any{"session_id": searchTestSessionID, "limit": 1}
	first, err := executeHistorySearch(t, provider, args)
	if err != nil {
		t.Fatal(err)
	}
	args["cursor"] = first.(map[string]any)["next_cursor"]
	provider.sessions = fakeHistorySessionLister{sessions: []thread.Thread{current, {ID: uuid.NewString(), CreatedByUserID: "owner"}}}
	if _, err := executeHistorySearch(t, provider, args); err != nil {
		t.Fatalf("unrelated accessible session invalidated an explicit-session cursor: %v", err)
	}
	provider.sessions = fakeHistorySessionLister{}
	if _, err := executeHistorySearch(t, provider, args); err == nil {
		t.Fatal("removing the selected session must still revoke cursor access")
	}
}

const (
	searchTestBotID     = "00000000-0000-0000-0000-000000098101"
	searchTestSessionID = "00000000-0000-0000-0000-000000098102"
)

type historySearchQueries struct {
	dbstore.Queries
	params sqlc.SearchMessagesParams
	rows   []sqlc.SearchMessagesRow
	calls  int
}

func (q *historySearchQueries) SearchMessages(_ context.Context, params sqlc.SearchMessagesParams) ([]sqlc.SearchMessagesRow, error) {
	q.params = params
	q.calls++
	return q.rows, nil
}

func executeHistorySearch(t *testing.T, provider *HistoryProvider, args map[string]any) (any, error) {
	t.Helper()
	registered, err := provider.Tools(context.Background(), SessionContext{BotID: searchTestBotID, SessionID: searchTestSessionID})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range registered {
		if tool.Name == ToolSearchMessages().String() {
			return tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, args)
		}
	}
	t.Fatal("search_messages was not registered")
	return nil, nil
}

func TestHistorySearchExplicitSessionHasNoImplicitLowerBound(t *testing.T) {
	q := &historySearchQueries{}
	result, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{"session_id": searchTestSessionID, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if q.params.StartTime.Valid {
		t.Fatalf("explicit session limited to %s", q.params.StartTime.Time)
	}
	if q.params.MaxCount != 3 {
		t.Fatalf("max count = %d, want limit plus sentinel", q.params.MaxCount)
	}
	if result.(map[string]any)["has_more"] != false {
		t.Fatalf("missing completion marker: %#v", result)
	}
}

func TestHistorySearchRejectsInvalidArgumentsBeforeQuery(t *testing.T) {
	for _, args := range []map[string]any{
		{"limit": 0},
		{"limit": 201},
		{"limit": 1.5},
		{"limit": "2"},
		{"start_time": "invalid"},
		{"end_time": "tomorrow"},
		{"start_time": "2026-02-02T00:00:00Z", "end_time": "2026-01-01T00:00:00Z"},
		{"session_id": "broken"},
		{"contact_id": "broken"},
		{"role": "system"},
		{"keyword": 2},
		{"cursor": "broken"},
	} {
		encoded, _ := json.Marshal(args)
		t.Run(string(encoded), func(t *testing.T) {
			q := &historySearchQueries{}
			_, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), args)
			if err == nil || q.calls != 0 {
				t.Fatalf("error=%v query calls=%d", err, q.calls)
			}
		})
	}
}

func TestHistorySearchRecentScopeIsReported(t *testing.T) {
	q := &historySearchQueries{}
	result, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{"keyword": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if !q.params.StartTime.Valid || !strings.Contains(string(encoded), "time_scope") {
		t.Fatalf("unreported default window: params=%+v result=%s", q.params, encoded)
	}
}

func TestHistorySearchCursorBindsFiltersAndFreezesDefaultWindow(t *testing.T) {
	q := &historySearchQueries{rows: []sqlc.SearchMessagesRow{
		{ID: dbpkg.ParseUUIDOrEmpty(uuid.NewString()), SessionID: dbpkg.ParseUUIDOrEmpty(searchTestSessionID), CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}},
		{ID: dbpkg.ParseUUIDOrEmpty(uuid.NewString())},
	}}
	provider := NewHistoryProvider(nil, nil, nil, q)
	first, err := executeHistorySearch(t, provider, map[string]any{"limit": 1, "keyword": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	start := q.params.StartTime.Time
	cursor := first.(map[string]any)["next_cursor"]
	_, err = executeHistorySearch(t, provider, map[string]any{"cursor": cursor, "keyword": "needle", "limit": 2})
	if err != nil || !q.params.StartTime.Time.Equal(start) || !q.params.CursorID.Valid {
		t.Fatalf("continuation: %v %+v", err, q.params)
	}
	for _, changed := range []map[string]any{
		{"cursor": cursor, "keyword": "other"},
		{"cursor": cursor, "keyword": "needle", "session_id": searchTestSessionID},
		{"cursor": cursor, "keyword": "needle", "start_time": "2020-01-01"},
	} {
		calls := q.calls
		if _, err := executeHistorySearch(t, provider, changed); err == nil || q.calls != calls {
			t.Fatalf("changed filters accepted: %v", changed)
		}
	}
}

func TestHistorySearchPageBudgetKeepsResumablePrefix(t *testing.T) {
	q := &historySearchQueries{}
	for range 201 {
		q.rows = append(q.rows, sqlc.SearchMessagesRow{ID: dbpkg.ParseUUIDOrEmpty(uuid.NewString()), SessionID: dbpkg.ParseUUIDOrEmpty(searchTestSessionID), Role: "assistant", SearchText: strings.Repeat("\"中\\", 250), CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	}
	value, err := executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{"session_id": searchTestSessionID, "limit": 200})
	if err != nil {
		t.Fatal(err)
	}
	out := value.(map[string]any)
	encoded, _ := json.Marshal(out)
	rows := out["messages"].([]map[string]any)
	if len(encoded) > 32*1024 || len(rows) == 0 || len(rows) >= 200 || out["has_more"] != true {
		t.Fatalf("budget: bytes=%d count=%d more=%v", len(encoded), len(rows), out["has_more"])
	}
	for i, row := range rows {
		if row["id"] != q.rows[i].ID.String() || !utf8.ValidString(row["text"].(string)) || len(row["text"].(string)) > 512 {
			t.Fatalf("invalid prefix/preview: %v", row)
		}
	}
	_, err = executeHistorySearch(t, NewHistoryProvider(nil, nil, nil, q), map[string]any{"session_id": searchTestSessionID, "limit": 200, "cursor": out["next_cursor"]})
	if err != nil || q.params.CursorID.String() != rows[len(rows)-1]["id"] {
		t.Fatalf("wrong continuation: %v %v", q.params.CursorID, err)
	}
}
