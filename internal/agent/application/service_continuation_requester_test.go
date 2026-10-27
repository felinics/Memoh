package application

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type admittedRunQueries struct {
	dbstore.Queries
	input []byte
	err   error
}

func (q admittedRunQueries) GetSessionRun(context.Context, pgtype.UUID) (sqlc.SessionRun, error) {
	return sqlc.SessionRun{InputJson: q.input}, q.err
}

func TestRunRequesterUserIDReadsTheRunsAdmission(t *testing.T) {
	t.Parallel()

	const runID = "8a7f6a6e-2b1c-4d3e-9f00-0123456789ab"
	for _, tc := range []struct {
		name    string
		queries admittedRunQueries
		want    string
		wantErr bool
	}{
		{name: "admitted user", queries: admittedRunQueries{input: []byte(`{"kind":"message","resume":{"version":1,"chat_id":"bot","user_id":" requester "}}`)}, want: "requester"},
		{name: "admitted for no account", queries: admittedRunQueries{input: []byte(`{"kind":"message","resume":{"version":1,"chat_id":"bot"}}`)}},
		{name: "no recorded intent", queries: admittedRunQueries{input: []byte(`{"kind":"message"}`)}},
		{name: "unreadable admission", queries: admittedRunQueries{input: []byte(`{`)}, wantErr: true},
		{name: "missing run", queries: admittedRunQueries{err: errors.New("no rows")}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&Service{queries: tc.queries}).runRequesterUserID(context.Background(), runID)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("runRequesterUserID() = %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
	if got, err := (&Service{queries: admittedRunQueries{err: errors.New("unused")}}).runRequesterUserID(context.Background(), " "); got != "" || err != nil {
		t.Fatalf("a run without an id has no requester, got %q, %v", got, err)
	}
}
