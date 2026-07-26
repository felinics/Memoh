package catalog

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	memorydomain "github.com/memohai/memoh/domains/memory"
	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
	"github.com/memohai/memoh/domains/memory/registry"
)

type providerBootstrapStore struct {
	memport.ProviderStore
	providers []memport.ProviderRecord
}

func (s *providerBootstrapStore) ListProviders(context.Context) ([]memport.ProviderRecord, error) {
	return s.providers, nil
}

type providerAdminStore struct {
	record       memport.ProviderRecord
	createArg    memport.ProviderCreate
	createCalls  int
	updateArg    memport.ProviderUpdate
	deleteID     string
	defaultErr   error
	defaultCalls int
}

func (s *providerAdminStore) CreateProvider(_ context.Context, value memport.ProviderCreate) (memport.ProviderRecord, error) {
	s.createCalls++
	s.createArg = value
	s.record.Name = value.Name
	s.record.Provider = value.Provider
	s.record.Config = append([]byte(nil), value.Config...)
	s.record.IsDefault = value.IsDefault
	return s.record, nil
}

func (s *providerAdminStore) FindProvider(context.Context, string) (memport.ProviderRecord, error) {
	return s.record, nil
}

func (s *providerAdminStore) ListProviders(context.Context) ([]memport.ProviderRecord, error) {
	return []memport.ProviderRecord{s.record}, nil
}

func (s *providerAdminStore) UpdateProvider(_ context.Context, value memport.ProviderUpdate) (memport.ProviderRecord, error) {
	s.updateArg = value
	s.record.Name = value.Name
	s.record.Config = append([]byte(nil), value.Config...)
	return s.record, nil
}

func (s *providerAdminStore) DeleteProvider(_ context.Context, id string) error {
	s.deleteID = id
	return nil
}

func (s *providerAdminStore) FindDefaultProvider(context.Context) (memport.ProviderRecord, error) {
	s.defaultCalls++
	return s.record, s.defaultErr
}

type bootstrapProvider struct {
	providerType string
	closeCalls   *atomic.Int32
}

func (p *bootstrapProvider) Type() string { return p.providerType }

func (p *bootstrapProvider) Close() error {
	if p.closeCalls != nil {
		p.closeCalls.Add(1)
	}
	return nil
}

func (*bootstrapProvider) OnBeforeChat(context.Context, memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	return nil, nil
}

func (*bootstrapProvider) OnAfterChat(context.Context, memprovider.AfterChatRequest) error {
	return nil
}

func (*bootstrapProvider) Add(context.Context, memprovider.AddRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*bootstrapProvider) Search(context.Context, memprovider.SearchRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*bootstrapProvider) GetAll(context.Context, memprovider.GetAllRequest) (memprovider.SearchResponse, error) {
	return memprovider.SearchResponse{}, nil
}

func (*bootstrapProvider) Update(context.Context, memprovider.UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, nil
}

func (*bootstrapProvider) Delete(context.Context, string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*bootstrapProvider) DeleteBatch(context.Context, []string) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*bootstrapProvider) DeleteAll(context.Context, memprovider.DeleteAllRequest) (memprovider.DeleteResponse, error) {
	return memprovider.DeleteResponse{}, nil
}

func (*bootstrapProvider) Compact(context.Context, map[string]any, float64, int) (memprovider.CompactResult, error) {
	return memprovider.CompactResult{}, nil
}

func (*bootstrapProvider) Usage(context.Context, map[string]any) (memprovider.UsageResponse, error) {
	return memprovider.UsageResponse{}, nil
}

func TestInstantiateAllLoadsConfiguredProvidersIntoRegistry(t *testing.T) {
	t.Parallel()

	providerID := "01020300-0000-0000-0000-000000000000"
	reg := registry.NewRegistry(slog.Default())
	reg.RegisterFactory(string(ProviderMem0), func(_ context.Context, _, _ string, _ map[string]any) (memprovider.Instance, error) {
		return &bootstrapProvider{providerType: string(ProviderMem0)}, nil
	})
	service := NewService(slog.Default(), &providerBootstrapStore{
		providers: []memport.ProviderRecord{{
			ID:       providerID,
			Name:     "Mem0",
			Provider: string(ProviderMem0),
			Config:   []byte(`{"api_key":"test"}`),
		}},
	})
	service.SetRegistry(reg)

	loaded, err := service.InstantiateAll(context.Background())
	if err != nil {
		t.Fatalf("InstantiateAll() error = %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded providers = %d, want 1", loaded)
	}
	if _, err := reg.Get(context.Background(), providerID); err != nil {
		t.Fatalf("registry missing configured provider after InstantiateAll(): %v", err)
	}
}

func TestServiceCRUDMaintainsProviderRegistryLifecycle(t *testing.T) {
	t.Parallel()

	const providerID = "b65fdcc0-d39e-4d38-b62c-050f2c1f4b6b"
	store := &providerAdminStore{record: memport.ProviderRecord{ID: providerID, Provider: string(ProviderMem0)}}
	reg := registry.NewRegistry(slog.Default())
	var instances []*bootstrapProvider
	reg.RegisterFactory(string(ProviderMem0), func(_ context.Context, _, _ string, cfg map[string]any) (memprovider.Instance, error) {
		provider := &bootstrapProvider{providerType: StringFromConfig(cfg, "version"), closeCalls: &atomic.Int32{}}
		instances = append(instances, provider)
		return provider, nil
	})
	service := NewService(slog.Default(), store)
	service.SetRegistry(reg)

	created, err := service.Create(t.Context(), ProviderCreateRequest{
		Name: "  Primary memory  ", Provider: ProviderMem0, Config: map[string]any{"version": "v1"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != providerID || created.Name != "Primary memory" || created.Config["version"] != "v1" {
		t.Fatalf("Create() = %+v", created)
	}
	if store.createArg.Name != "Primary memory" || store.createArg.Provider != string(ProviderMem0) || store.createArg.IsDefault {
		t.Fatalf("CreateProvider input = %+v", store.createArg)
	}
	provider, err := reg.Get(t.Context(), providerID)
	if err != nil || provider.Type() != "v1" {
		t.Fatalf("registry provider after Create() = %v, %v", provider, err)
	}

	updatedName := "  Updated memory  "
	updated, err := service.Update(t.Context(), providerID, ProviderUpdateRequest{
		Name: &updatedName, Config: map[string]any{"version": "v2"},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Name != "Updated memory" || updated.Config["version"] != "v2" {
		t.Fatalf("Update() = %+v", updated)
	}
	if store.updateArg.ID != providerID || store.updateArg.Name != "Updated memory" {
		t.Fatalf("UpdateProvider input = %+v", store.updateArg)
	}
	if got := instances[0].closeCalls.Load(); got != 1 {
		t.Fatalf("old provider Close() calls after Update() = %d, want 1", got)
	}
	provider, err = reg.Get(t.Context(), providerID)
	if err != nil || provider.Type() != "v2" {
		t.Fatalf("registry provider after Update() = %v, %v", provider, err)
	}

	if err := service.Delete(t.Context(), providerID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.deleteID != providerID {
		t.Fatalf("DeleteProvider id = %q, want %q", store.deleteID, providerID)
	}
	if got := instances[1].closeCalls.Load(); got != 1 {
		t.Fatalf("updated provider Close() calls after Delete() = %d, want 1", got)
	}
}

func TestServiceEnsureDefaultPreservesExistingOrCreatesBuiltin(t *testing.T) {
	t.Parallel()

	const providerID = "b65fdcc0-d39e-4d38-b62c-050f2c1f4b6b"
	store := &providerAdminStore{record: memport.ProviderRecord{
		ID: providerID, Name: "Existing", Provider: string(ProviderBuiltin), Config: []byte(`{"memory_mode":"graph"}`), IsDefault: true,
	}}
	service := NewService(slog.Default(), store)

	existing, err := service.EnsureDefault(t.Context())
	if err != nil {
		t.Fatalf("EnsureDefault(existing) error = %v", err)
	}
	if existing.ID != providerID || !existing.IsDefault || store.createCalls != 0 {
		t.Fatalf("EnsureDefault(existing) = %+v; create calls = %d", existing, store.createCalls)
	}

	store.defaultErr = errors.New("no default provider")
	created, err := service.EnsureDefault(t.Context())
	if err != nil {
		t.Fatalf("EnsureDefault(create) error = %v", err)
	}
	if created.Name != "Built-in Memory" || created.Provider != string(ProviderBuiltin) || !created.IsDefault {
		t.Fatalf("EnsureDefault(create) = %+v", created)
	}
	if store.createCalls != 1 || !store.createArg.IsDefault || string(store.createArg.Config) != `{}` {
		t.Fatalf("default CreateProvider input = %+v; calls = %d", store.createArg, store.createCalls)
	}
}
