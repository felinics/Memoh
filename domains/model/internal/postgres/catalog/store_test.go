package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	modeldomain "github.com/memohai/memoh/domains/model"
	catalogport "github.com/memohai/memoh/domains/model/internal/port/catalog"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type fakeQueries struct {
	modelQueries
	modelRows        []dbsqlc.ModelModel
	created          dbsqlc.ModelModel
	createErr        error
	createParams     dbsqlc.CreateModelParams
	providerModel    dbsqlc.ModelModel
	providerModelErr error
	modelByID        dbsqlc.ModelModel
	modelByIDErr     error
}

func (q *fakeQueries) CreateModel(_ context.Context, arg dbsqlc.CreateModelParams) (dbsqlc.ModelModel, error) {
	q.createParams = arg
	return q.created, q.createErr
}

func (q *fakeQueries) ListModelsByModelID(context.Context, string) ([]dbsqlc.ModelModel, error) {
	return q.modelRows, nil
}

func (q *fakeQueries) GetModelByProviderAndModelID(context.Context, dbsqlc.GetModelByProviderAndModelIDParams) (dbsqlc.ModelModel, error) {
	return q.providerModel, q.providerModelErr
}

func (q *fakeQueries) GetModelByID(context.Context, pgtype.UUID) (dbsqlc.ModelModel, error) {
	return q.modelByID, q.modelByIDErr
}

func TestStoreGetByModelIDPreservesAmbiguousContract(t *testing.T) {
	q := &fakeQueries{modelRows: []dbsqlc.ModelModel{{ModelID: "same"}, {ModelID: "same"}}}
	store := NewStoreWithQueries(q)

	_, err := store.GetByModelID(t.Context(), "same")
	if !errors.Is(err, catalogport.ErrModelIDAmbiguous) {
		t.Fatalf("GetByModelID() error = %v, want ErrModelIDAmbiguous", err)
	}
}

func TestStoreGetByModelIDMapsMissingRows(t *testing.T) {
	store := NewStoreWithQueries(&fakeQueries{})

	_, err := store.GetByModelID(t.Context(), "missing")
	if !errors.Is(err, catalogport.ErrModelNotFound) {
		t.Fatalf("GetByModelID() error = %v, want ErrModelNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetByModelID() leaked pgx.ErrNoRows: %v", err)
	}
}

func TestStoreGetByIDMapsMissingRow(t *testing.T) {
	store := NewStoreWithQueries(&fakeQueries{modelByIDErr: pgx.ErrNoRows})

	_, err := store.GetByID(t.Context(), "31d5f0b6-88b8-4e03-b2cb-d2ef2b4e66d2")
	if !errors.Is(err, catalogport.ErrModelNotFound) {
		t.Fatalf("GetByID() error = %v, want ErrModelNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetByID() leaked pgx.ErrNoRows: %v", err)
	}
}

func TestStoreCreateConvertsPersistenceTypesAndUniqueConflict(t *testing.T) {
	providerID, err := db.ParseUUID("3a16ce55-20e3-48aa-b5c5-8c07b9480b73")
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := db.ParseUUID("31d5f0b6-88b8-4e03-b2cb-d2ef2b4e66d2")
	if err != nil {
		t.Fatal(err)
	}
	q := &fakeQueries{created: dbsqlc.ModelModel{
		ID:         modelID,
		ModelID:    "gpt-4o",
		Name:       pgtype.Text{String: "GPT-4o", Valid: true},
		ProviderID: providerID,
		Type:       "chat",
		Enable:     true,
		Config:     []byte(`{"context_window":128000}`),
	}}
	store := NewStoreWithQueries(q)
	created, err := store.Create(t.Context(), catalogport.CreateInput{
		ModelID:    "gpt-4o",
		Name:       "GPT-4o",
		ProviderID: providerID.String(),
		Type:       modeldomain.ModelTypeChat,
		Enable:     true,
		Config:     []byte(`{"context_window":128000}`),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != modelID.String() || created.ProviderID != providerID.String() || created.Name != "GPT-4o" {
		t.Fatalf("Create() record = %#v", created)
	}
	if !q.createParams.Name.Valid || q.createParams.Name.String != "GPT-4o" {
		t.Fatalf("Create() name params = %#v", q.createParams.Name)
	}

	q.createErr = &pgconn.PgError{Code: "23505"}
	_, err = store.Create(t.Context(), catalogport.CreateInput{ModelID: "gpt-4o", ProviderID: providerID.String(), Type: modeldomain.ModelTypeChat})
	if !errors.Is(err, catalogport.ErrModelIDAlreadyExists) {
		t.Fatalf("Create() unique error = %v, want ErrModelIDAlreadyExists", err)
	}
}
