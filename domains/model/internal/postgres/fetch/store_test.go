package fetch

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	fetchport "github.com/memohai/memoh/domains/model/internal/port/fetch"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const fetchProviderID = "7a63f6b5-133c-42b4-a56f-ad7203db5c8d"

type recordingQueries struct {
	row          dbsqlc.ModelFetchProvider
	createArg    dbsqlc.CreateFetchProviderParams
	findID       pgtype.UUID
	listAllCalls int
	listProvider string
	updateArg    dbsqlc.UpdateFetchProviderParams
	deleteID     pgtype.UUID
}

func (q *recordingQueries) CreateFetchProvider(_ context.Context, arg dbsqlc.CreateFetchProviderParams) (dbsqlc.ModelFetchProvider, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *recordingQueries) GetFetchProviderByID(_ context.Context, id pgtype.UUID) (dbsqlc.ModelFetchProvider, error) {
	q.findID = id
	return q.row, nil
}

func (q *recordingQueries) ListFetchProviders(context.Context) ([]dbsqlc.ModelFetchProvider, error) {
	q.listAllCalls++
	return []dbsqlc.ModelFetchProvider{q.row}, nil
}

func (q *recordingQueries) ListFetchProvidersByProvider(_ context.Context, provider string) ([]dbsqlc.ModelFetchProvider, error) {
	q.listProvider = provider
	return []dbsqlc.ModelFetchProvider{q.row}, nil
}

func (q *recordingQueries) UpdateFetchProvider(_ context.Context, arg dbsqlc.UpdateFetchProviderParams) (dbsqlc.ModelFetchProvider, error) {
	q.updateArg = arg
	return q.row, nil
}

func (q *recordingQueries) DeleteFetchProvider(_ context.Context, id pgtype.UUID) error {
	q.deleteID = id
	return nil
}

func TestStoreMapsFetchProviderCommandsAndRows(t *testing.T) {
	t.Parallel()

	id, err := db.ParseUUID(fetchProviderID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	queries := &recordingQueries{row: dbsqlc.ModelFetchProvider{
		ID: id, Name: "Jina", Provider: "jina", Config: []byte(`{"api_key":"secret"}`), Enable: true,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}}
	store := NewStoreWithQueries(queries)

	created, err := store.CreateProvider(t.Context(), fetchport.ProviderWrite{
		Name: "Jina", Provider: "jina", Config: []byte(`{"base_url":"https://example.test"}`), Enable: true,
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if created.ID != fetchProviderID || created.Name != "Jina" || created.Provider != "jina" || !created.Enable {
		t.Fatalf("CreateProvider() = %+v", created)
	}
	if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("CreateProvider() timestamps = %v, %v", created.CreatedAt, created.UpdatedAt)
	}
	if queries.createArg.Name != "Jina" || queries.createArg.Provider != "jina" || !queries.createArg.Enable {
		t.Fatalf("CreateFetchProvider params = %+v", queries.createArg)
	}
	created.Config[0] = 'x'
	if queries.row.Config[0] == 'x' {
		t.Fatal("ProviderRecord.Config aliases generated row storage")
	}

	if _, err := store.FindProvider(t.Context(), fetchProviderID); err != nil {
		t.Fatalf("FindProvider() error = %v", err)
	}
	if queries.findID != id {
		t.Fatalf("GetFetchProviderByID id = %v, want %v", queries.findID, id)
	}
	if _, err := store.ListProviders(t.Context(), ""); err != nil {
		t.Fatalf("ListProviders(all) error = %v", err)
	}
	if queries.listAllCalls != 1 {
		t.Fatalf("ListFetchProviders calls = %d, want 1", queries.listAllCalls)
	}
	if _, err := store.ListProviders(t.Context(), "jina"); err != nil {
		t.Fatalf("ListProviders(filtered) error = %v", err)
	}
	if queries.listProvider != "jina" {
		t.Fatalf("ListFetchProvidersByProvider provider = %q", queries.listProvider)
	}

	if _, err := store.UpdateProvider(t.Context(), fetchport.ProviderWrite{
		ID: fetchProviderID, Name: "Reader", Provider: "jina", Config: []byte(`{}`), Enable: false,
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	if queries.updateArg.ID != id || queries.updateArg.Name != "Reader" || queries.updateArg.Enable {
		t.Fatalf("UpdateFetchProvider params = %+v", queries.updateArg)
	}
	if err := store.DeleteProvider(t.Context(), fetchProviderID); err != nil {
		t.Fatalf("DeleteProvider() error = %v", err)
	}
	if queries.deleteID != id {
		t.Fatalf("DeleteFetchProvider id = %v, want %v", queries.deleteID, id)
	}
}

func TestStoreRejectsInvalidFetchProviderID(t *testing.T) {
	t.Parallel()
	store := NewStoreWithQueries(&recordingQueries{})
	if _, err := store.FindProvider(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("FindProvider() error = nil, want invalid UUID")
	}
}
