package search

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	searchport "github.com/memohai/memoh/domains/model/internal/port/search"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const searchProviderID = "b65fdcc0-d39e-4d38-b62c-050f2c1f4b6b"

type recordingQueries struct {
	row          dbsqlc.ModelSearchProvider
	createArg    dbsqlc.CreateSearchProviderParams
	findID       pgtype.UUID
	listAllCalls int
	listProvider string
	updateArg    dbsqlc.UpdateSearchProviderParams
	deleteID     pgtype.UUID
}

func (q *recordingQueries) CreateSearchProvider(_ context.Context, arg dbsqlc.CreateSearchProviderParams) (dbsqlc.ModelSearchProvider, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *recordingQueries) GetSearchProviderByID(_ context.Context, id pgtype.UUID) (dbsqlc.ModelSearchProvider, error) {
	q.findID = id
	return q.row, nil
}

func (q *recordingQueries) ListSearchProviders(context.Context) ([]dbsqlc.ModelSearchProvider, error) {
	q.listAllCalls++
	return []dbsqlc.ModelSearchProvider{q.row}, nil
}

func (q *recordingQueries) ListSearchProvidersByProvider(_ context.Context, provider string) ([]dbsqlc.ModelSearchProvider, error) {
	q.listProvider = provider
	return []dbsqlc.ModelSearchProvider{q.row}, nil
}

func (q *recordingQueries) UpdateSearchProvider(_ context.Context, arg dbsqlc.UpdateSearchProviderParams) (dbsqlc.ModelSearchProvider, error) {
	q.updateArg = arg
	return q.row, nil
}

func (q *recordingQueries) DeleteSearchProvider(_ context.Context, id pgtype.UUID) error {
	q.deleteID = id
	return nil
}

func TestStoreMapsSearchProviderCommandsAndRows(t *testing.T) {
	t.Parallel()

	id, err := db.ParseUUID(searchProviderID)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	queries := &recordingQueries{row: dbsqlc.ModelSearchProvider{
		ID: id, Name: "Brave", Provider: "brave", Config: []byte(`{"api_key":"secret"}`), Enable: true,
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}}
	store := NewStoreWithQueries(queries)

	created, err := store.CreateProvider(t.Context(), searchport.ProviderWrite{
		Name: "Brave", Provider: "brave", Config: []byte(`{"base_url":"https://example.test"}`), Enable: true,
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if created.ID != searchProviderID || created.Name != "Brave" || created.Provider != "brave" || !created.Enable {
		t.Fatalf("CreateProvider() = %+v", created)
	}
	if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("CreateProvider() timestamps = %v, %v", created.CreatedAt, created.UpdatedAt)
	}
	if queries.createArg.Name != "Brave" || queries.createArg.Provider != "brave" || !queries.createArg.Enable {
		t.Fatalf("CreateSearchProvider params = %+v", queries.createArg)
	}
	created.Config[0] = 'x'
	if queries.row.Config[0] == 'x' {
		t.Fatal("ProviderRecord.Config aliases generated row storage")
	}

	if _, err := store.FindProvider(t.Context(), searchProviderID); err != nil {
		t.Fatalf("FindProvider() error = %v", err)
	}
	if queries.findID != id {
		t.Fatalf("GetSearchProviderByID id = %v, want %v", queries.findID, id)
	}
	if _, err := store.ListProviders(t.Context(), ""); err != nil {
		t.Fatalf("ListProviders(all) error = %v", err)
	}
	if queries.listAllCalls != 1 {
		t.Fatalf("ListSearchProviders calls = %d, want 1", queries.listAllCalls)
	}
	if _, err := store.ListProviders(t.Context(), "brave"); err != nil {
		t.Fatalf("ListProviders(filtered) error = %v", err)
	}
	if queries.listProvider != "brave" {
		t.Fatalf("ListSearchProvidersByProvider provider = %q", queries.listProvider)
	}

	if _, err := store.UpdateProvider(t.Context(), searchport.ProviderWrite{
		ID: searchProviderID, Name: "Search", Provider: "brave", Config: []byte(`{}`), Enable: false,
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	if queries.updateArg.ID != id || queries.updateArg.Name != "Search" || queries.updateArg.Enable {
		t.Fatalf("UpdateSearchProvider params = %+v", queries.updateArg)
	}
	if err := store.DeleteProvider(t.Context(), searchProviderID); err != nil {
		t.Fatalf("DeleteProvider() error = %v", err)
	}
	if queries.deleteID != id {
		t.Fatalf("DeleteSearchProvider id = %v, want %v", queries.deleteID, id)
	}
}

func TestStoreRejectsInvalidSearchProviderID(t *testing.T) {
	t.Parallel()
	store := NewStoreWithQueries(&recordingQueries{})
	if _, err := store.FindProvider(t.Context(), "not-a-uuid"); err == nil {
		t.Fatal("FindProvider() error = nil, want invalid UUID")
	}
}

func TestClassifyWriteError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "team provider constraint",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "search_providers_team_provider_unique"},
			want: searchport.ErrProviderTypeConflict,
		},
		{
			name: "canonical provider constraint wrapped",
			err: fmt.Errorf("insert: %w", &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "search_providers_provider_unique",
			}),
			want: searchport.ErrProviderTypeConflict,
		},
		{
			name: "name constraint",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "search_providers_name_unique"},
			want: searchport.ErrProviderNameTaken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyWriteError(tt.err)
			if !errors.Is(got, tt.want) {
				t.Fatalf("classifyWriteError() = %v, want %v", got, tt.want)
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("classifyWriteError() = %v, want original error %v in chain", got, tt.err)
			}
		})
	}
}

func TestClassifyWriteErrorPreservesInfrastructureError(t *testing.T) {
	t.Parallel()
	errDatabaseUnavailable := errors.New("database unavailable")
	if got := classifyWriteError(errDatabaseUnavailable); !errors.Is(got, errDatabaseUnavailable) {
		t.Fatalf("classifyWriteError() = %v, want original infrastructure error", got)
	}
}
