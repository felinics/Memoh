package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

type fakeCatalog struct {
	providers []templateport.ProviderRecord
	models    []fakeModel
	idSeq     int

	providerInserts int
	modelInserts    int
}

type fakeModel struct {
	ID         string
	ModelID    string
	Name       string
	ProviderID string
	Type       string
	Enable     bool
	Config     []byte
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{}
}

func (f *fakeCatalog) newID() string {
	f.idSeq++
	return fmt.Sprintf("generated-%d", f.idSeq)
}

func seedID(n byte) string {
	return fmt.Sprintf("seed-%d", n)
}

func (f *fakeCatalog) ListProviders(_ context.Context) ([]templateport.ProviderRecord, error) {
	return append([]templateport.ProviderRecord(nil), f.providers...), nil
}

func (f *fakeCatalog) ListModelIDs(_ context.Context, providerID string) ([]string, error) {
	var out []string
	for _, m := range f.models {
		if m.ProviderID != providerID {
			continue
		}
		if m.Type == "speech" || m.Type == "transcription" || m.Type == "video" {
			continue
		}
		out = append(out, m.ModelID)
	}
	return out, nil
}

func (f *fakeCatalog) UpsertProvider(_ context.Context, arg templateport.ProviderSeed) (templateport.ProviderRecord, error) {
	for i := range f.providers {
		if f.providers[i].Name == arg.Name {
			f.providers[i].Icon = arg.Icon
			f.providers[i].ClientType = arg.ClientType
			return f.providers[i], nil
		}
	}
	p := templateport.ProviderRecord{
		ID:         f.newID(),
		Name:       arg.Name,
		ClientType: arg.ClientType,
		Icon:       arg.Icon,
		Enable:     false,
		Config:     append([]byte(nil), arg.Config...),
		Metadata:   []byte(`{}`),
	}
	f.providers = append(f.providers, p)
	f.providerInserts++
	return p, nil
}

func (f *fakeCatalog) UpdateProvider(_ context.Context, arg templateport.ProviderUpdate) (templateport.ProviderRecord, error) {
	for i := range f.providers {
		if f.providers[i].ID != arg.ID {
			continue
		}
		f.providers[i].Name = arg.Name
		f.providers[i].ClientType = arg.ClientType
		f.providers[i].Icon = arg.Icon
		f.providers[i].Enable = arg.Enable
		f.providers[i].Config = append([]byte(nil), arg.Config...)
		f.providers[i].Metadata = append([]byte(nil), arg.Metadata...)
		return f.providers[i], nil
	}
	return templateport.ProviderRecord{}, errors.New("provider not found")
}

func (f *fakeCatalog) UpsertModel(_ context.Context, arg templateport.ModelSeed) error {
	for i := range f.models {
		if f.models[i].ProviderID == arg.ProviderID && f.models[i].ModelID == arg.ModelID {
			f.models[i].Name = arg.Name
			f.models[i].Type = arg.Type
			existingConfig := jsonMapBytes(f.models[i].Config)
			incomingConfig := jsonMapBytes(arg.Config)
			if description, ok := existingConfig["description"]; ok {
				incomingConfig["description"] = description
			}
			f.models[i].Config, _ = json.Marshal(incomingConfig)
			return nil
		}
	}
	m := fakeModel{
		ID:         f.newID(),
		ModelID:    arg.ModelID,
		Name:       arg.Name,
		ProviderID: arg.ProviderID,
		Type:       arg.Type,
		Enable:     false,
		Config:     append([]byte(nil), arg.Config...),
	}
	f.models = append(f.models, m)
	f.modelInserts++
	return nil
}

func (f *fakeCatalog) modelsByProviderID(providerID string) []fakeModel {
	var out []fakeModel
	for _, model := range f.models {
		if model.ProviderID == providerID {
			out = append(out, model)
		}
	}
	return out
}

func jsonMapBytes(raw []byte) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// mutateProvider applies direct row edits, standing in for the raw SQL UPDATEs
// the old sqlite-backed test issued to simulate user modifications.
func (f *fakeCatalog) mutateProvider(t *testing.T, id string, mutate func(*templateport.ProviderRecord)) {
	t.Helper()
	for i := range f.providers {
		if f.providers[i].ID == id {
			mutate(&f.providers[i])
			return
		}
	}
	t.Fatalf("mutateProvider: provider %s not found", id)
}

func openAIDefinition() Definition {
	return Definition{
		Name:          "OpenAI",
		Driver:        "openai-responses",
		Icon:          "openai",
		DefaultConfig: map[string]any{"base_url": "https://api.openai.com/v1"},
		Source:        "openai.yaml",
		Models: []ModelDefinition{{
			ModelID: "gpt-test",
			Name:    "GPT Test",
			Type:    "chat",
			Config:  map[string]any{"context_window": 128000},
		}},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestSyncCreatesProvidersAndModels(t *testing.T) {
	ctx := context.Background()
	q := newFakeCatalog()

	def := openAIDefinition()
	def.Models = append(def.Models, ModelDefinition{
		ModelID: "gpt-embed",
		Name:    "GPT Embed",
		Type:    "embedding",
		Config:  map[string]any{"dimensions": 1536},
	})
	if err := SyncProviders(ctx, discardLogger(), q, q, []Definition{def}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(q.providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(q.providers))
	}
	got := q.providers[0]
	if got.Name != "OpenAI" {
		t.Fatalf("provider name = %q, want %q", got.Name, "OpenAI")
	}
	if got.ClientType != "openai-responses" {
		t.Fatalf("provider client_type = %q, want %q", got.ClientType, "openai-responses")
	}
	if got.Icon != "openai" {
		t.Fatalf("provider icon = %+v, want openai", got.Icon)
	}
	if got.Enable {
		t.Fatalf("provider enable = true, want new registry providers disabled")
	}
	cfg := jsonMap(t, got.Config)
	if cfg["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("base_url = %#v, want definition base URL", cfg["base_url"])
	}
	assertRegistrySource(t, got.Metadata, "openai.yaml")

	models := q.modelsByProviderID(got.ID)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	byModelID := make(map[string]fakeModel, len(models))
	for _, m := range models {
		byModelID[m.ModelID] = m
	}
	chat, ok := byModelID["gpt-test"]
	if !ok || chat.Name != "GPT Test" || chat.Type != "chat" {
		t.Fatalf("chat model = %+v, want gpt-test/GPT Test/chat", chat)
	}
	if chat.Enable {
		t.Fatal("chat model enable = true, want new registry models disabled")
	}
	if value := jsonMap(t, chat.Config)["context_window"]; value != float64(128000) {
		t.Fatalf("chat model context_window = %#v, want 128000", value)
	}
	embed, ok := byModelID["gpt-embed"]
	if !ok || embed.Type != "embedding" {
		t.Fatalf("embedding model = %+v, want gpt-embed/embedding", embed)
	}
	if embed.Enable {
		t.Fatal("embedding model enable = true, want new registry models disabled")
	}
}

func TestSyncTwiceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	q := newFakeCatalog()
	logger := discardLogger()

	defs := []Definition{openAIDefinition()}
	if err := SyncProviders(ctx, logger, q, q, defs); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if len(q.providers) != 1 || len(q.models) != 1 {
		t.Fatalf("after first sync providers=%d models=%d, want 1/1", len(q.providers), len(q.models))
	}
	providerID := q.providers[0].ID
	modelRowID := q.models[0].ID

	if err := SyncProviders(ctx, logger, q, q, defs); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	if len(q.providers) != 1 {
		t.Fatalf("provider count after second sync = %d, want 1 (no duplicates)", len(q.providers))
	}
	if len(q.models) != 1 {
		t.Fatalf("model count after second sync = %d, want 1 (no duplicates)", len(q.models))
	}
	if q.providerInserts != 1 {
		t.Fatalf("provider inserts = %d, want exactly 1 across both syncs", q.providerInserts)
	}
	if q.modelInserts != 1 {
		t.Fatalf("model inserts = %d, want exactly 1 across both syncs", q.modelInserts)
	}
	if q.providers[0].ID != providerID {
		t.Fatalf("provider id changed across syncs: %s -> %s", providerID, q.providers[0].ID)
	}
	if q.models[0].ID != modelRowID {
		t.Fatalf("model row id changed across syncs: %s -> %s", modelRowID, q.models[0].ID)
	}
	assertRegistrySource(t, q.providers[0].Metadata, "openai.yaml")
}

func TestSyncSeedsDescriptionThenPreservesUserOverride(t *testing.T) {
	ctx := context.Background()
	q := newFakeCatalog()
	logger := discardLogger()

	def := openAIDefinition()
	def.Models[0].Config["description"] = "Template description"
	if err := SyncProviders(ctx, logger, q, q, []Definition{def}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := jsonMap(t, q.models[0].Config)["description"]; got != "Template description" {
		t.Fatalf("description = %#v, want template description", got)
	}

	q.models[0].Config = []byte(`{"context_window":128000,"description":"Custom description"}`)
	def.Models[0].Config["description"] = "Updated template description"
	if err := SyncProviders(ctx, logger, q, q, []Definition{def}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := jsonMap(t, q.models[0].Config)["description"]; got != "Custom description" {
		t.Fatalf("description = %#v, want preserved custom description", got)
	}

	q.models[0].Config = []byte(`{"context_window":128000,"description":""}`)
	if err := SyncProviders(ctx, logger, q, q, []Definition{def}); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	if got := jsonMap(t, q.models[0].Config)["description"]; got != "" {
		t.Fatalf("description = %#v, want preserved explicit clear", got)
	}
}

func TestSyncUpdatesProviderWhenRegistryNameChanges(t *testing.T) {
	ctx := context.Background()
	q := newFakeCatalog()
	logger := discardLogger()

	initial := openAIDefinition()
	if err := SyncProviders(ctx, logger, q, q, []Definition{initial}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	providers, err := q.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(providers))
	}
	providerID := providers[0].ID

	// Simulate user edits: enabled the provider and customized its config.
	q.mutateProvider(t, providerID, func(p *templateport.ProviderRecord) {
		p.Enable = true
		p.Config = []byte(`{"base_url":"https://custom.example/v1","api_key":"sk-existing","prompt_cache_ttl":"5m"}`)
	})

	renamed := openAIDefinition()
	renamed.Name = "OpenAI Responses"
	renamed.Models[0].Name = "GPT Test Updated"
	renamed.Models[0].Config = map[string]any{"context_window": 256000}
	if err := SyncProviders(ctx, logger, q, q, []Definition{renamed}); err != nil {
		t.Fatalf("renamed sync: %v", err)
	}

	providers, err = q.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers after rename: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count after rename = %d, want 1", len(providers))
	}
	got := providers[0]
	if got.ID != providerID {
		t.Fatalf("provider id = %s, want existing %s", got.ID, providerID)
	}
	if got.Name != "OpenAI Responses" {
		t.Fatalf("provider name = %q, want renamed value", got.Name)
	}
	if !got.Enable {
		t.Fatalf("provider enable = false, want preserved true")
	}
	cfg := jsonMap(t, got.Config)
	if cfg["api_key"] != "sk-existing" {
		t.Fatalf("api_key = %#v, want preserved secret", cfg["api_key"])
	}
	if cfg["base_url"] != "https://custom.example/v1" {
		t.Fatalf("base_url = %#v, want preserved custom value", cfg["base_url"])
	}
	if cfg["prompt_cache_ttl"] != "5m" {
		t.Fatalf("prompt_cache_ttl = %#v, want preserved custom value", cfg["prompt_cache_ttl"])
	}
	assertRegistrySource(t, got.Metadata, "openai.yaml")

	models := q.modelsByProviderID(got.ID)
	if len(models) != 1 || models[0].Name != "GPT Test Updated" {
		t.Fatalf("models = %+v, want updated model", models)
	}
	modelCfg := jsonMap(t, models[0].Config)
	if value := modelCfg["context_window"]; value != float64(256000) {
		t.Fatalf("model context_window = %#v, want 256000", value)
	}
}

func TestSyncMatchesLegacyProviderByBaseURL(t *testing.T) {
	ctx := context.Background()
	q := newFakeCatalog()

	// A pre-registry ("legacy") provider: no registry metadata, user-created,
	// same client type and base URL, with an overlapping model fingerprint.
	legacyProviderID := seedID(1)
	q.providers = append(q.providers, templateport.ProviderRecord{
		ID:         legacyProviderID,
		Name:       "OpenAI Legacy",
		ClientType: "openai-responses",
		Icon:       "openai",
		Enable:     true,
		Config:     []byte(`{"base_url":"https://api.openai.com/v1","api_key":"sk-legacy"}`),
		Metadata:   []byte(`{}`),
	})
	q.models = append(q.models, fakeModel{
		ID:         seedID(2),
		ModelID:    "gpt-test",
		Name:       "GPT Test Legacy",
		ProviderID: legacyProviderID,
		Type:       "chat",
		Enable:     true,
		Config:     []byte(`{"context_window":64000}`),
	})

	def := openAIDefinition()
	def.Name = "OpenAI Responses"
	if err := SyncProviders(ctx, discardLogger(), q, q, []Definition{def}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	providers, err := q.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(providers))
	}
	got := providers[0]
	if got.ID != legacyProviderID {
		t.Fatalf("provider id = %s, want legacy %s", got.ID, legacyProviderID)
	}
	if got.Name != "OpenAI Responses" {
		t.Fatalf("provider name = %q, want registry name", got.Name)
	}
	if !got.Enable {
		t.Fatalf("provider enable = false, want preserved true")
	}
	cfg := jsonMap(t, got.Config)
	if cfg["api_key"] != "sk-legacy" {
		t.Fatalf("api_key = %#v, want preserved legacy secret", cfg["api_key"])
	}
	assertRegistrySource(t, got.Metadata, "openai.yaml")

	models := q.modelsByProviderID(got.ID)
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1 (upserted onto legacy model)", len(models))
	}
	if models[0].Name != "GPT Test" {
		t.Fatalf("model name = %q, want registry name", models[0].Name)
	}
	if !models[0].Enable {
		t.Fatal("model enable = false, want existing user choice preserved")
	}
}

func jsonMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal json map: %v", err)
	}
	return out
}

func assertRegistrySource(t *testing.T, raw []byte, want string) {
	t.Helper()
	metadata := jsonMap(t, raw)
	registryMeta, ok := metadata[registryMetadataKey].(map[string]any)
	if !ok {
		t.Fatalf("registry metadata missing: %#v", metadata)
	}
	if registryMeta["source"] != want {
		t.Fatalf("registry source = %#v, want %q", registryMeta["source"], want)
	}
}
