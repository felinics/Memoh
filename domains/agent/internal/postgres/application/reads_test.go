package application

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	testSessionID = "11111111-1111-1111-1111-111111111111"
	testModelID   = "22222222-2222-2222-2222-222222222222"
	testCompactID = "33333333-3333-3333-3333-333333333333"
	testMessageID = "44444444-4444-4444-4444-444444444444"
)

type readsQueryFake struct {
	sessionID pgtype.UUID
	modelID   pgtype.UUID
	modelErr  error

	compactID pgtype.UUID
	refs      []dbsqlc.ListMessageRefsByCompactIDRow
	refsErr   error
}

func (f *readsQueryFake) GetLatestSessionModelID(_ context.Context, sessionID pgtype.UUID) (pgtype.UUID, error) {
	f.sessionID = sessionID
	return f.modelID, f.modelErr
}

func (f *readsQueryFake) ListMessageRefsByCompactID(_ context.Context, compactID pgtype.UUID) ([]dbsqlc.ListMessageRefsByCompactIDRow, error) {
	f.compactID = compactID
	return f.refs, f.refsErr
}

func TestReadsLatestSessionModelID(t *testing.T) {
	t.Parallel()

	queries := &readsQueryFake{modelID: mustUUID(t, testModelID)}
	got, err := NewReads(queries).LatestSessionModelID(t.Context(), testSessionID)
	if err != nil {
		t.Fatalf("LatestSessionModelID() error = %v", err)
	}
	if queries.sessionID.String() != testSessionID {
		t.Fatalf("session id = %s, want %s", queries.sessionID.String(), testSessionID)
	}
	if got != testModelID {
		t.Fatalf("model id = %q, want %q", got, testModelID)
	}
}

func TestReadsLatestSessionModelIDEmptyWhenInvalid(t *testing.T) {
	t.Parallel()

	got, err := NewReads(&readsQueryFake{}).LatestSessionModelID(t.Context(), testSessionID)
	if err != nil {
		t.Fatalf("LatestSessionModelID() error = %v", err)
	}
	if got != "" {
		t.Fatalf("model id = %q, want empty", got)
	}
}

func TestReadsLatestSessionModelIDPropagatesQueryError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("query failed")
	_, err := NewReads(&readsQueryFake{modelErr: wantErr}).LatestSessionModelID(t.Context(), testSessionID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestReadsListCompactionMessageRefs(t *testing.T) {
	t.Parallel()

	queries := &readsQueryFake{refs: []dbsqlc.ListMessageRefsByCompactIDRow{
		{ID: mustUUID(t, testMessageID)},
		{ID: pgtype.UUID{}},
	}}
	got, err := NewReads(queries).ListCompactionMessageRefs(t.Context(), testCompactID)
	if err != nil {
		t.Fatalf("ListCompactionMessageRefs() error = %v", err)
	}
	if queries.compactID.String() != testCompactID {
		t.Fatalf("compact id = %s, want %s", queries.compactID.String(), testCompactID)
	}
	if len(got) != 1 || got[0] != testMessageID {
		t.Fatalf("refs = %#v, want [%s]", got, testMessageID)
	}
}

func TestReadsListCompactionMessageRefsPropagatesQueryError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("query failed")
	_, err := NewReads(&readsQueryFake{refsErr: wantErr}).ListCompactionMessageRefs(t.Context(), testCompactID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestNewReadsFromDBUsesAgentSQLC(t *testing.T) {
	t.Parallel()

	if NewReadsFromDB(nil) == nil {
		t.Fatal("NewReadsFromDB() returned nil")
	}
}

func mustUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(raw)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", raw, err)
	}
	return id
}
