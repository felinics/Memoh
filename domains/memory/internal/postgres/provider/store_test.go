package provider

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	dbsqlc "github.com/memohai/memoh/domains/memory/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const memoryProviderID = "b65fdcc0-d39e-4d38-b62c-050f2c1f4b6b"

type recordingQueries struct {
	row          dbsqlc.MemoryMemoryProvider
	createArg    dbsqlc.CreateMemoryProviderParams
	findID       pgtype.UUID
	listCalls    int
	updateArg    dbsqlc.UpdateMemoryProviderParams
	deleteID     pgtype.UUID
	defaultCalls int
}

func (q *recordingQueries) CreateMemoryProvider(_ context.Context, arg dbsqlc.CreateMemoryProviderParams) (dbsqlc.MemoryMemoryProvider, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *recordingQueries) GetMemoryProviderByID(_ context.Context, id pgtype.UUID) (dbsqlc.MemoryMemoryProvider, error) {
	q.findID = id
	return q.row, nil
}

func (q *recordingQueries) ListMemoryProviders(context.Context) ([]dbsqlc.MemoryMemoryProvider, error) {
	q.listCalls++
	return []dbsqlc.MemoryMemoryProvider{q.row}, nil
}

func (q *recordingQueries) UpdateMemoryProvider(_ context.Context, arg dbsqlc.UpdateMemoryProviderParams) (dbsqlc.MemoryMemoryProvider, error) {
	q.updateArg = arg
	return q.row, nil
}

func (q *recordingQueries) DeleteMemoryProvider(_ context.Context, id pgtype.UUID) error {
	q.deleteID = id
	return nil
}

func (q *recordingQueries) GetDefaultMemoryProvider(context.Context) (dbsqlc.MemoryMemoryProvider, error) {
	q.defaultCalls++
	return q.row, nil
}

func TestStoreMapsMemoryProviderCommandsAndRows(t *testing.T) {
	t.Parallel()

	id, err := db.ParseUUID(memoryProviderID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	queries := &recordingQueries{row: dbsqlc.MemoryMemoryProvider{
		ID: id, Name: "Memory", Provider: "mem0", Config: []byte(`{"api_key":"secret"}`), IsDefault: true,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}}
	store := NewStore(queries)

	created, err := store.CreateProvider(t.Context(), memport.ProviderCreate{
		Name: "Memory", Provider: "mem0", Config: []byte(`{"base_url":"https://example.test"}`), IsDefault: true,
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if created.ID != memoryProviderID || created.Provider != "mem0" || !created.IsDefault {
		t.Fatalf("CreateProvider() = %+v", created)
	}
	if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("CreateProvider() timestamps = %v, %v", created.CreatedAt, created.UpdatedAt)
	}
	if queries.createArg.Name != "Memory" || queries.createArg.Provider != "mem0" || !queries.createArg.IsDefault {
		t.Fatalf("CreateMemoryProvider params = %+v", queries.createArg)
	}
	created.Config[0] = 'x'
	if queries.row.Config[0] == 'x' {
		t.Fatal("ProviderRecord.Config aliases generated row storage")
	}

	if _, err := store.FindProvider(t.Context(), memoryProviderID); err != nil {
		t.Fatalf("FindProvider() error = %v", err)
	}
	if queries.findID != id {
		t.Fatalf("GetMemoryProviderByID id = %v, want %v", queries.findID, id)
	}
	if providers, err := store.ListProviders(t.Context()); err != nil || len(providers) != 1 {
		t.Fatalf("ListProviders() = %v, %v", providers, err)
	}
	if queries.listCalls != 1 {
		t.Fatalf("ListMemoryProviders calls = %d, want 1", queries.listCalls)
	}

	if _, err := store.UpdateProvider(t.Context(), memport.ProviderUpdate{
		ID: memoryProviderID, Name: "Updated", Config: []byte(`{}`),
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	if queries.updateArg.ID != id || queries.updateArg.Name != "Updated" || string(queries.updateArg.Config) != `{}` {
		t.Fatalf("UpdateMemoryProvider params = %+v", queries.updateArg)
	}
	if err := store.DeleteProvider(t.Context(), memoryProviderID); err != nil {
		t.Fatalf("DeleteProvider() error = %v", err)
	}
	if queries.deleteID != id {
		t.Fatalf("DeleteMemoryProvider id = %v, want %v", queries.deleteID, id)
	}
	if provider, err := store.FindDefaultProvider(t.Context()); err != nil || !provider.IsDefault {
		t.Fatalf("FindDefaultProvider() = %+v, %v", provider, err)
	}
	if queries.defaultCalls != 1 {
		t.Fatalf("GetDefaultMemoryProvider calls = %d, want 1", queries.defaultCalls)
	}
}

func TestStoreRejectsInvalidMemoryProviderID(t *testing.T) {
	t.Parallel()

	store := NewStore(&recordingQueries{})
	if _, err := store.FindProvider(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("FindProvider() error = nil, want invalid UUID")
	}
	if _, err := store.UpdateProvider(t.Context(), memport.ProviderUpdate{ID: "not-a-uuid"}); err == nil {
		t.Fatal("UpdateProvider() error = nil, want invalid UUID")
	}
	if err := store.DeleteProvider(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("DeleteProvider() error = nil, want invalid UUID")
	}
}
