package template

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
)

type fakeProviderQueries struct {
	normal, speech, transcription []dbsqlc.ModelProvider
	upsertArg                     dbsqlc.UpsertRegistryProviderParams
	updateArg                     dbsqlc.UpdateProviderParams
}

func (f *fakeProviderQueries) ListProviders(context.Context) ([]dbsqlc.ModelProvider, error) {
	return f.normal, nil
}

func (f *fakeProviderQueries) ListSpeechProviders(context.Context) ([]dbsqlc.ModelProvider, error) {
	return f.speech, nil
}

func (f *fakeProviderQueries) ListTranscriptionProviders(context.Context) ([]dbsqlc.ModelProvider, error) {
	return f.transcription, nil
}

func (f *fakeProviderQueries) UpsertRegistryProvider(_ context.Context, arg dbsqlc.UpsertRegistryProviderParams) (dbsqlc.ModelProvider, error) {
	f.upsertArg = arg
	return providerRow("00000000-0000-0000-0000-000000000001"), nil
}

func (f *fakeProviderQueries) UpdateProvider(_ context.Context, arg dbsqlc.UpdateProviderParams) (dbsqlc.ModelProvider, error) {
	f.updateArg = arg
	return providerRow(arg.ID.String()), nil
}

func providerRow(id string) dbsqlc.ModelProvider {
	parsed, _ := uuid.Parse(id)
	return dbsqlc.ModelProvider{ID: pgtype.UUID{Bytes: parsed, Valid: true}, Name: "provider", Config: []byte(`{"base_url":"https://example.test"}`), Metadata: []byte(`{}`)}
}

func TestProviderCatalogMergesProviderStatements(t *testing.T) {
	first := providerRow("00000000-0000-0000-0000-000000000001")
	second := providerRow("00000000-0000-0000-0000-000000000002")
	q := &fakeProviderQueries{
		normal:        []dbsqlc.ModelProvider{first},
		speech:        []dbsqlc.ModelProvider{first, second},
		transcription: []dbsqlc.ModelProvider{second},
	}
	items, err := NewProviderCatalogWithQueries(q).ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(items) != 2 || items[0].ID != first.ID.String() || items[1].ID != second.ID.String() {
		t.Fatalf("ListProviders() = %#v, want two unique provider records", items)
	}
}

func TestProviderCatalogMapsRegistryCommands(t *testing.T) {
	q := &fakeProviderQueries{}
	catalog := NewProviderCatalogWithQueries(q)
	created, err := catalog.UpsertProvider(context.Background(), templateport.ProviderSeed{
		Name:       "OpenAI",
		ClientType: "openai-responses",
		Icon:       "openai",
		Config:     []byte(`{"base_url":"https://api.openai.com/v1"}`),
	})
	if err != nil {
		t.Fatalf("UpsertProvider() error = %v", err)
	}
	if created.ID == "" || q.upsertArg.Icon.String != "openai" || !q.upsertArg.Icon.Valid {
		t.Fatalf("upsert mapping = %#v", q.upsertArg)
	}
	if _, err := catalog.UpdateProvider(context.Background(), templateport.ProviderUpdate{ID: "bad"}); err == nil {
		t.Fatal("UpdateProvider() with invalid UUID returned nil error")
	}
}

type fakeModelQueries struct {
	rows      []dbsqlc.ModelModel
	listArg   pgtype.UUID
	upsertArg dbsqlc.UpsertRegistryModelParams
}

func (f *fakeModelQueries) ListModelsByProviderID(_ context.Context, id pgtype.UUID) ([]dbsqlc.ModelModel, error) {
	f.listArg = id
	return f.rows, nil
}

func (f *fakeModelQueries) UpsertRegistryModel(_ context.Context, arg dbsqlc.UpsertRegistryModelParams) (dbsqlc.ModelModel, error) {
	f.upsertArg = arg
	return dbsqlc.ModelModel{}, nil
}

func TestModelCatalogMapsIDsAndUpsert(t *testing.T) {
	providerID := "00000000-0000-0000-0000-000000000001"
	q := &fakeModelQueries{rows: []dbsqlc.ModelModel{{ModelID: "gpt-test"}, {ModelID: "gpt-embed"}}}
	catalog := NewModelCatalogWithQueries(q)
	ids, err := catalog.ListModelIDs(context.Background(), providerID)
	if err != nil {
		t.Fatalf("ListModelIDs() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != "gpt-test" || q.listArg.String() != providerID {
		t.Fatalf("ListModelIDs() = %#v, query arg = %s", ids, q.listArg.String())
	}
	if err := catalog.UpsertModel(context.Background(), templateport.ModelSeed{
		ProviderID: providerID,
		ModelID:    "gpt-test",
		Name:       "GPT Test",
		Type:       "chat",
		Config:     []byte(`{"context_window":128000}`),
	}); err != nil {
		t.Fatalf("UpsertModel() error = %v", err)
	}
	if q.upsertArg.ProviderID.String() != providerID || q.upsertArg.Name.String != "GPT Test" || !q.upsertArg.Name.Valid {
		t.Fatalf("upsert mapping = %#v", q.upsertArg)
	}
}
