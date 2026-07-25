package template

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

type recordingSyncStore struct {
	tx        *recordingSyncTransaction
	runCalls  int
	commits   int
	rollbacks int
}

func (s *recordingSyncStore) RunSyncTransaction(_ context.Context, fn func(templateport.Transaction) error) error {
	s.runCalls++
	s.tx.events = append(s.tx.events, "begin")
	if err := fn(s.tx); err != nil {
		s.rollbacks++
		s.tx.events = append(s.tx.events, "rollback")
		return err
	}
	s.commits++
	s.tx.events = append(s.tx.events, "commit")
	return nil
}

type recordingSyncTransaction struct {
	templates          []templateport.TemplateRecord
	models             map[string][]templateport.ModelRecord
	upsertedTemplates  []templateport.UpsertTemplateCommand
	upsertedModels     []templateport.UpsertModelCommand
	deactivated        []string
	deactivatedModels  []string
	events             []string
	acquireLockErr     error
	listTemplatesError error
}

func (tx *recordingSyncTransaction) AcquireSyncLock(context.Context) error {
	tx.events = append(tx.events, "lock")
	return tx.acquireLockErr
}

func (tx *recordingSyncTransaction) ListTemplates(context.Context) ([]templateport.TemplateRecord, error) {
	tx.events = append(tx.events, "list-templates")
	return tx.templates, tx.listTemplatesError
}

func (tx *recordingSyncTransaction) UpsertTemplate(_ context.Context, input templateport.UpsertTemplateCommand) (templateport.TemplateRecord, error) {
	tx.events = append(tx.events, "upsert-template:"+input.Domain+"/"+input.Key)
	tx.upsertedTemplates = append(tx.upsertedTemplates, input)
	for _, template := range tx.templates {
		if identity(template.Domain, template.Key) == identity(input.Domain, input.Key) {
			return templateport.TemplateRecord{
				ID: input.Key, Domain: input.Domain, Key: input.Key,
				ContentHash: input.ContentHash, Active: true,
			}, nil
		}
	}
	return templateport.TemplateRecord{
		ID: input.Key, Domain: input.Domain, Key: input.Key,
		ContentHash: input.ContentHash, Active: true,
	}, nil
}

func (tx *recordingSyncTransaction) DeactivateTemplate(_ context.Context, id string) error {
	tx.events = append(tx.events, "deactivate-template:"+id)
	tx.deactivated = append(tx.deactivated, id)
	return nil
}

func (tx *recordingSyncTransaction) ListModels(_ context.Context, templateID string) ([]templateport.ModelRecord, error) {
	tx.events = append(tx.events, "list-models:"+templateID)
	return tx.models[templateID], nil
}

func (tx *recordingSyncTransaction) UpsertModel(_ context.Context, input templateport.UpsertModelCommand) error {
	tx.events = append(tx.events, "upsert-model:"+input.TemplateID+"/"+input.Type+"/"+input.ModelID)
	tx.upsertedModels = append(tx.upsertedModels, input)
	return nil
}

func (tx *recordingSyncTransaction) DeactivateModel(_ context.Context, id string) error {
	tx.events = append(tx.events, "deactivate-model:"+id)
	tx.deactivatedModels = append(tx.deactivatedModels, id)
	return nil
}

func TestSyncUsesOneLockedTransactionAndPreservesCatalogSemantics(t *testing.T) {
	t.Parallel()

	unchanged := Definition{
		Key: "alpha", Domain: DomainLLM, Name: "Alpha", Driver: "driver-alpha",
		Models: []ModelDefinition{{ModelID: "alpha-model"}},
	}
	_, unchangedHash, err := normalizeDefinition(unchanged, 0)
	if err != nil {
		t.Fatalf("normalizeDefinition() error = %v", err)
	}
	updated := Definition{
		Key: " Beta ", Domain: DomainLLM, Name: " Beta ", Driver: " driver-beta ",
		ConfigSchema: map[string]any{"type": "object"},
		Models:       []ModelDefinition{{ModelID: " beta-model ", Name: " Beta Model "}},
	}
	tx := &recordingSyncTransaction{
		templates: []templateport.TemplateRecord{
			{ID: "alpha-id", Domain: "llm", Key: "alpha", ContentHash: unchangedHash, Active: true},
			{ID: "beta", Domain: "llm", Key: "beta", ContentHash: "old", Active: true},
			{ID: "removed-id", Domain: "video", Key: "removed", ContentHash: "old", Active: true},
		},
		models: map[string][]templateport.ModelRecord{
			"beta": {
				{ID: "kept-model-id", ModelID: "beta-model", Type: "chat"},
				{ID: "removed-model-id", ModelID: "retired", Type: "chat"},
			},
		},
	}
	store := &recordingSyncStore{tx: tx}
	logger := slog.New(slog.DiscardHandler)

	if err := Sync(t.Context(), logger, store, []Definition{unchanged, updated}); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.runCalls != 1 || store.commits != 1 || store.rollbacks != 0 {
		t.Fatalf("transaction counts = run:%d commit:%d rollback:%d, want 1/1/0", store.runCalls, store.commits, store.rollbacks)
	}
	wantEvents := []string{
		"begin", "lock", "list-templates",
		"upsert-template:llm/beta", "list-models:beta", "upsert-model:beta/chat/beta-model",
		"deactivate-model:removed-model-id", "deactivate-template:removed-id", "commit",
	}
	if !reflect.DeepEqual(tx.events, wantEvents) {
		t.Fatalf("sync events = %v, want %v", tx.events, wantEvents)
	}
	if len(tx.upsertedTemplates) != 1 {
		t.Fatalf("upserted templates = %d, want 1", len(tx.upsertedTemplates))
	}
	upsert := tx.upsertedTemplates[0]
	if upsert.Key != "beta" || upsert.Name != "Beta" || upsert.Driver != "driver-beta" || upsert.SortOrder != 1 {
		t.Fatalf("normalized template command = %#v", upsert)
	}
	if upsert.ContentHash == "" || string(upsert.ConfigSchema) != `{"type":"object"}` {
		t.Fatalf("serialized template command = %#v", upsert)
	}
	if len(tx.upsertedModels) != 1 {
		t.Fatalf("upserted models = %d, want 1", len(tx.upsertedModels))
	}
	model := tx.upsertedModels[0]
	if model.ModelID != "beta-model" || model.Name != "Beta Model" || model.Type != "chat" || model.SortOrder != 0 {
		t.Fatalf("normalized model command = %#v", model)
	}
}

func TestSyncRollsBackDuplicateDefinitions(t *testing.T) {
	t.Parallel()

	tx := &recordingSyncTransaction{models: map[string][]templateport.ModelRecord{}}
	store := &recordingSyncStore{tx: tx}
	definitions := []Definition{
		{Key: "provider", Domain: DomainLLM, Name: "Provider", Driver: "driver"},
		{Key: " PROVIDER ", Domain: DomainLLM, Name: "Duplicate", Driver: "driver"},
	}
	err := Sync(t.Context(), nil, store, definitions)
	if err == nil || err.Error() != "duplicate provider template llm/provider" {
		t.Fatalf("Sync() error = %v, want duplicate definition error", err)
	}
	if store.commits != 0 || store.rollbacks != 1 {
		t.Fatalf("transaction counts = commit:%d rollback:%d, want 0/1", store.commits, store.rollbacks)
	}
	if got := tx.events[len(tx.events)-1]; got != "rollback" {
		t.Fatalf("last event = %q, want rollback", got)
	}
}

func TestSyncRequiresTransactionStoreAndWrapsLockFailure(t *testing.T) {
	t.Parallel()

	if err := Sync(t.Context(), nil, nil, nil); err == nil || err.Error() != "provider template sync store is required" {
		t.Fatalf("Sync(nil store) error = %v", err)
	}
	lockErr := errors.New("lock unavailable")
	tx := &recordingSyncTransaction{acquireLockErr: lockErr}
	store := &recordingSyncStore{tx: tx}
	err := Sync(t.Context(), nil, store, nil)
	if !errors.Is(err, lockErr) || err.Error() != "acquire provider template sync lock: lock unavailable" {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.rollbacks != 1 || len(tx.events) != 3 || tx.events[2] != "rollback" {
		t.Fatalf("lock failure events = %v, rollbacks = %d", tx.events, store.rollbacks)
	}
}

func TestNormalizeDefinitionProducesStableCatalogEntry(t *testing.T) {
	t.Parallel()

	raw := Definition{
		Key:    " OpenAI ",
		Domain: DomainLLM,
		Name:   " OpenAI ",
		Driver: " openai-responses ",
		Source: " openai.yaml ",
		Models: []ModelDefinition{{
			ModelID: " gpt-test ",
			Name:    " GPT Test ",
		}},
	}

	first, firstHash, err := normalizeDefinition(raw, 7)
	if err != nil {
		t.Fatalf("normalizeDefinition() error = %v", err)
	}
	second, secondHash, err := normalizeDefinition(raw, 7)
	if err != nil {
		t.Fatalf("normalizeDefinition() second error = %v", err)
	}

	if firstHash == "" || firstHash != secondHash {
		t.Fatalf("content hash is not stable: %q != %q", firstHash, secondHash)
	}
	if first.Key != "openai" || first.Name != "OpenAI" || first.Driver != "openai-responses" || first.Source != "openai.yaml" {
		t.Fatalf("normalized template = %#v", first)
	}
	if first.SortOrder != 7 {
		t.Fatalf("sort order = %d, want 7", first.SortOrder)
	}
	if len(first.Models) != 1 || first.Models[0].ModelID != "gpt-test" || first.Models[0].Type != "chat" {
		t.Fatalf("normalized models = %#v", first.Models)
	}
	if first.ConfigSchema == nil || first.DefaultConfig == nil || first.Metadata == nil {
		t.Fatal("normalized JSON objects must not be nil")
	}
	if second.Key != first.Key {
		t.Fatalf("second normalized key = %q, want %q", second.Key, first.Key)
	}
}

func TestNormalizeDefinitionRejectsInvalidCatalogEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		definition Definition
	}{
		{
			name: "invalid domain",
			definition: Definition{
				Key: "provider", Domain: Domain("search"), Name: "Provider", Driver: "driver",
			},
		},
		{
			name: "missing driver",
			definition: Definition{
				Key: "provider", Domain: DomainLLM, Name: "Provider",
			},
		},
		{
			name: "empty model id",
			definition: Definition{
				Key: "provider", Domain: DomainLLM, Name: "Provider", Driver: "driver",
				Models: []ModelDefinition{{Name: "Missing ID"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := normalizeDefinition(tt.definition, 0); err == nil {
				t.Fatal("normalizeDefinition() error = nil")
			}
		})
	}
}
