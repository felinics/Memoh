package template

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	"github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
)

const (
	templateID = "11111111-1111-1111-1111-111111111111"
	modelID    = "22222222-2222-2222-2222-222222222222"
)

type transactionBeginnerFake struct {
	tx    pgx.Tx
	err   error
	calls int
}

func (b *transactionBeginnerFake) Begin(context.Context) (pgx.Tx, error) {
	b.calls++
	return b.tx, b.err
}

type transactionFake struct {
	pgx.Tx
	commits, rollbacks int
	commitErr          error
}

func (tx *transactionFake) Commit(context.Context) error   { tx.commits++; return tx.commitErr }
func (tx *transactionFake) Rollback(context.Context) error { tx.rollbacks++; return nil }

type recordingQueries struct {
	events                    []string
	upsertTemplate            sqlc.UpsertProviderTemplateParams
	setTemplateActive         sqlc.SetProviderTemplateActiveParams
	listedModelsForTemplateID pgtype.UUID
	upsertModel               sqlc.UpsertProviderTemplateModelParams
	setModelActive            sqlc.SetProviderTemplateModelActiveParams
}

func (q *recordingQueries) AcquireProviderTemplateSyncLock(context.Context) error {
	q.events = append(q.events, "lock")
	return nil
}

func (q *recordingQueries) ListAllProviderTemplates(context.Context) ([]sqlc.ModelProviderTemplate, error) {
	q.events = append(q.events, "list-templates")
	return []sqlc.ModelProviderTemplate{{
		ID: mustUUID(templateID), Domain: "llm", Key: "old", ContentHash: "old-hash", Active: true,
	}}, nil
}

func (q *recordingQueries) UpsertProviderTemplate(_ context.Context, input sqlc.UpsertProviderTemplateParams) (sqlc.ModelProviderTemplate, error) {
	q.events = append(q.events, "upsert-template")
	q.upsertTemplate = input
	return sqlc.ModelProviderTemplate{
		ID: mustUUID(templateID), Domain: input.Domain, Key: input.Key,
		ContentHash: input.ContentHash, Active: true,
	}, nil
}

func (q *recordingQueries) SetProviderTemplateActive(_ context.Context, input sqlc.SetProviderTemplateActiveParams) error {
	q.events = append(q.events, "deactivate-template")
	q.setTemplateActive = input
	return nil
}

func (q *recordingQueries) ListAllProviderTemplateModels(_ context.Context, id pgtype.UUID) ([]sqlc.ModelProviderTemplateModel, error) {
	q.events = append(q.events, "list-models")
	q.listedModelsForTemplateID = id
	return []sqlc.ModelProviderTemplateModel{{
		ID: mustUUID(modelID), ProviderTemplateID: id, ModelID: "old-model", Type: "chat",
	}}, nil
}

func (q *recordingQueries) UpsertProviderTemplateModel(_ context.Context, input sqlc.UpsertProviderTemplateModelParams) (sqlc.ModelProviderTemplateModel, error) {
	q.events = append(q.events, "upsert-model")
	q.upsertModel = input
	return sqlc.ModelProviderTemplateModel{ID: mustUUID(modelID)}, nil
}

func (q *recordingQueries) SetProviderTemplateModelActive(_ context.Context, input sqlc.SetProviderTemplateModelActiveParams) error {
	q.events = append(q.events, "deactivate-model")
	q.setModelActive = input
	return nil
}

func TestSyncStoreUsesOnlyTransactionScopedQueries(t *testing.T) {
	t.Parallel()

	queries := &recordingQueries{}
	pgxTx := &transactionFake{}
	base := &transactionBeginnerFake{tx: pgxTx}
	store := newSyncStore(base, func(got pgx.Tx) legacyQueries {
		if got != pgxTx {
			t.Fatalf("bound transaction = %T, want test transaction", got)
		}
		return queries
	})

	err := store.RunSyncTransaction(t.Context(), func(tx templateport.Transaction) error {
		if err := tx.AcquireSyncLock(t.Context()); err != nil {
			return err
		}
		templates, err := tx.ListTemplates(t.Context())
		if err != nil {
			return err
		}
		if len(templates) != 1 || templates[0].ID != templateID || templates[0].ContentHash != "old-hash" {
			t.Fatalf("templates = %#v", templates)
		}
		created, err := tx.UpsertTemplate(t.Context(), templateport.UpsertTemplateCommand{
			Key: "provider", Domain: "llm", Name: "Provider", Description: "description",
			Icon: " icon.svg ", Driver: "driver", ConfigSchema: []byte(`{"type":"object"}`),
			DefaultConfig: []byte(`{"enabled":true}`), Metadata: []byte(`{"source":"test"}`),
			Source: "provider.yaml", ContentHash: "new-hash", SortOrder: 7,
		})
		if err != nil {
			return err
		}
		if created.ID != templateID || !created.Active {
			t.Fatalf("upserted template = %#v", created)
		}
		models, err := tx.ListModels(t.Context(), templateID)
		if err != nil {
			return err
		}
		if len(models) != 1 || models[0].ID != modelID || models[0].ModelID != "old-model" {
			t.Fatalf("models = %#v", models)
		}
		if err := tx.UpsertModel(t.Context(), templateport.UpsertModelCommand{
			TemplateID: templateID, ModelID: "new-model", Name: "New Model", Type: "chat",
			Config: []byte(`{"temperature":1}`), Metadata: []byte(`{}`), SortOrder: 8,
		}); err != nil {
			return err
		}
		if err := tx.DeactivateModel(t.Context(), modelID); err != nil {
			return err
		}
		return tx.DeactivateTemplate(t.Context(), templateID)
	})
	if err != nil {
		t.Fatalf("RunSyncTransaction() error = %v", err)
	}
	if base.calls != 1 || pgxTx.commits != 1 || pgxTx.rollbacks != 1 {
		t.Fatalf("transaction calls = begin:%d commit:%d rollback:%d", base.calls, pgxTx.commits, pgxTx.rollbacks)
	}
	wantEvents := []string{"lock", "list-templates", "upsert-template", "list-models", "upsert-model", "deactivate-model", "deactivate-template"}
	if !reflect.DeepEqual(queries.events, wantEvents) {
		t.Fatalf("query events = %v, want %v", queries.events, wantEvents)
	}
	if !queries.upsertTemplate.Icon.Valid || queries.upsertTemplate.Icon.String != " icon.svg " || queries.upsertTemplate.SortOrder != 7 {
		t.Fatalf("template params = %#v", queries.upsertTemplate)
	}
	if queries.upsertModel.ProviderTemplateID != mustUUID(templateID) || queries.upsertModel.SortOrder != 8 {
		t.Fatalf("model params = %#v", queries.upsertModel)
	}
	if queries.listedModelsForTemplateID != mustUUID(templateID) {
		t.Fatalf("list model template id = %v", queries.listedModelsForTemplateID)
	}
	if queries.setModelActive.Active || queries.setModelActive.ID != mustUUID(modelID) {
		t.Fatalf("model active params = %#v", queries.setModelActive)
	}
	if queries.setTemplateActive.Active || queries.setTemplateActive.ID != mustUUID(templateID) {
		t.Fatalf("template active params = %#v", queries.setTemplateActive)
	}
}

func TestSyncStoreRejectsMissingTransactionCapability(t *testing.T) {
	t.Parallel()

	store := newSyncStore(nil, nil)
	err := store.RunSyncTransaction(t.Context(), func(templateport.Transaction) error { return nil })
	if !errors.Is(err, templateport.ErrTransactionsRequired) {
		t.Fatalf("RunSyncTransaction() error = %v, want ErrTransactionsRequired", err)
	}
}

func TestSyncStoreRejectsMissingScopedQueriesAndNilCallback(t *testing.T) {
	t.Parallel()

	base := &transactionBeginnerFake{tx: &transactionFake{}}
	store := newSyncStore(base, func(pgx.Tx) legacyQueries { return &recordingQueries{} })
	err := store.RunSyncTransaction(t.Context(), nil)
	if err == nil || err.Error() != "provider template sync transaction callback is required" {
		t.Fatalf("RunSyncTransaction(nil) error = %v", err)
	}
	if base.calls != 0 {
		t.Fatalf("nil callback opened %d transactions, want 0", base.calls)
	}
}

func TestSyncStoreRollsBackCallbackError(t *testing.T) {
	wantErr := errors.New("sync failed")
	pgxTx := &transactionFake{}
	store := newSyncStore(&transactionBeginnerFake{tx: pgxTx}, func(pgx.Tx) legacyQueries { return &recordingQueries{} })
	err := store.RunSyncTransaction(t.Context(), func(templateport.Transaction) error { return wantErr })
	if !errors.Is(err, wantErr) || pgxTx.commits != 0 || pgxTx.rollbacks != 1 {
		t.Fatalf("RunSyncTransaction() = %v, commit/rollback = %d/%d", err, pgxTx.commits, pgxTx.rollbacks)
	}
}

func mustUUID(value string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		panic(err)
	}
	return id
}
