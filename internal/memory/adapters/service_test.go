package adapters

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
)

type providerBootstrapQueries struct {
	dbstore.Queries
	providers []sqlc.MemoryProvider
}

func (q *providerBootstrapQueries) ListMemoryProviders(context.Context) ([]sqlc.MemoryProvider, error) {
	return q.providers, nil
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

func (*bootstrapProvider) OnBeforeChat(context.Context, BeforeChatRequest) (*BeforeChatResult, error) {
	return nil, nil
}

func (*bootstrapProvider) OnAfterChat(context.Context, AfterChatRequest) error { return nil }

func (*bootstrapProvider) ListTools(context.Context, mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	return nil, nil
}

func (*bootstrapProvider) CallTool(context.Context, mcp.ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return nil, nil
}

func (*bootstrapProvider) Add(context.Context, AddRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) Search(context.Context, SearchRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) GetAll(context.Context, GetAllRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) Update(context.Context, UpdateRequest) (MemoryItem, error) {
	return MemoryItem{}, nil
}

func (*bootstrapProvider) Delete(context.Context, string) (DeleteResponse, error) {
	return DeleteResponse{}, nil
}

func (*bootstrapProvider) DeleteBatch(context.Context, []string) (DeleteResponse, error) {
	return DeleteResponse{}, nil
}

func (*bootstrapProvider) DeleteAll(context.Context, DeleteAllRequest) (DeleteResponse, error) {
	return DeleteResponse{}, nil
}

func (*bootstrapProvider) Compact(context.Context, map[string]any, float64, int) (CompactResult, error) {
	return CompactResult{}, nil
}

func (*bootstrapProvider) Usage(context.Context, map[string]any) (UsageResponse, error) {
	return UsageResponse{}, nil
}

func TestInstantiateAllLoadsConfiguredProvidersIntoRegistry(t *testing.T) {
	t.Parallel()

	providerID := pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true}
	registry := NewRegistry(slog.Default())
	registry.RegisterFactory(string(ProviderMem0), func(_ context.Context, _, _ string, _ map[string]any) (Provider, error) {
		return &bootstrapProvider{providerType: string(ProviderMem0)}, nil
	})
	service := NewService(slog.Default(), &providerBootstrapQueries{
		providers: []sqlc.MemoryProvider{{
			ID:       providerID,
			Name:     "Mem0",
			Provider: string(ProviderMem0),
			Config:   []byte(`{"api_key":"test"}`),
		}},
	}, config.Config{})
	service.SetRegistry(registry)

	loaded, err := service.InstantiateAll(context.Background())
	if err != nil {
		t.Fatalf("InstantiateAll() error = %v", err)
	}
	if loaded != 1 {
		t.Fatalf("loaded providers = %d, want 1", loaded)
	}
	if _, err := registry.Get(context.Background(), providerID.String()); err != nil {
		t.Fatalf("registry missing configured provider after InstantiateAll(): %v", err)
	}
}

type registryTeamContextKey struct{}

func registryTeamResolver(ctx context.Context) (string, error) {
	teamID, _ := ctx.Value(registryTeamContextKey{}).(string)
	if teamID == "" {
		return "", errors.New("team missing")
	}
	return teamID, nil
}

func teamRegistryContext(teamID string) context.Context {
	return context.WithValue(context.Background(), registryTeamContextKey{}, teamID)
}

func TestRegistryIsolatesSameProviderIDByTeam(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default(), registryTeamResolver)
	registry.RegisterFactory(string(ProviderMem0), func(_ context.Context, teamID, _ string, _ map[string]any) (Provider, error) {
		return &bootstrapProvider{providerType: teamID}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderMem0), map[string]any{}, nil
	})

	teamA := teamRegistryContext("team-a")
	teamB := teamRegistryContext("team-b")
	providerA, err := registry.Get(teamA, "shared-provider-id")
	if err != nil {
		t.Fatalf("Get(team-a) error = %v", err)
	}
	providerB, err := registry.Get(teamB, "shared-provider-id")
	if err != nil {
		t.Fatalf("Get(team-b) error = %v", err)
	}
	if providerA == providerB {
		t.Fatal("same provider id reused one instance across teams")
	}
	providerAAgain, err := registry.Get(teamA, "shared-provider-id")
	if err != nil {
		t.Fatalf("second Get(team-a) error = %v", err)
	}
	if providerAAgain != providerA {
		t.Fatal("team-local provider instance was not cached")
	}
}

func TestRegistryConcurrentMissInstantiatesOnce(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default())
	var factoryCalls atomic.Int32
	registry.RegisterFactory(string(ProviderMem0), func(_ context.Context, _, _ string, _ map[string]any) (Provider, error) {
		factoryCalls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &bootstrapProvider{providerType: string(ProviderMem0)}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderMem0), map[string]any{}, nil
	})

	const workers = 24
	providers := make([]Provider, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			providers[i], errs[i] = registry.Get(context.Background(), "provider-id")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Get() worker %d error = %v", i, err)
		}
		if providers[i] != providers[0] {
			t.Fatalf("worker %d received a different provider instance", i)
		}
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("factory calls = %d, want 1", got)
	}
}

func TestRegistryUpdateCannotBeOverwrittenByInflightLoad(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default())
	oldFactoryStarted := make(chan struct{})
	releaseOldFactory := make(chan struct{})
	registry.RegisterFactory(string(ProviderMem0), func(_ context.Context, _, _ string, config map[string]any) (Provider, error) {
		version, _ := config["version"].(string)
		if version == "old" {
			close(oldFactoryStarted)
			<-releaseOldFactory
		}
		return &bootstrapProvider{providerType: version}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderMem0), map[string]any{"version": "old"}, nil
	})

	oldResult := make(chan Provider, 1)
	oldErr := make(chan error, 1)
	go func() {
		provider, err := registry.Get(context.Background(), "provider-id")
		oldResult <- provider
		oldErr <- err
	}()
	<-oldFactoryStarted

	updateErr := make(chan error, 1)
	go func() {
		if err := registry.Remove(context.Background(), "provider-id"); err != nil {
			updateErr <- err
			return
		}
		_, err := registry.Instantiate(context.Background(), "provider-id", string(ProviderMem0), map[string]any{"version": "new"})
		updateErr <- err
	}()
	close(releaseOldFactory)
	if err := <-oldErr; err != nil {
		t.Fatalf("in-flight Get() error = %v", err)
	}
	if provider := <-oldResult; provider == nil {
		t.Fatal("in-flight Get() returned nil provider")
	}
	if err := <-updateErr; err != nil {
		t.Fatalf("update registry error = %v", err)
	}

	provider, err := registry.Get(context.Background(), "provider-id")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if got := provider.Type(); got != "new" {
		t.Fatalf("provider after update = %q, want new", got)
	}
}

func TestRegistryFailsClosedWithoutTeam(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default(), registryTeamResolver)
	if _, err := registry.Get(context.Background(), "provider-id"); err == nil {
		t.Fatal("Get() without team context succeeded")
	}
}

func TestRegistryRemoveClosesProvider(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default())
	var closeCalls atomic.Int32
	provider := &bootstrapProvider{providerType: string(ProviderMem0), closeCalls: &closeCalls}
	if err := registry.RegisterContext(context.Background(), "provider-id", provider); err != nil {
		t.Fatalf("RegisterContext() error = %v", err)
	}

	if err := registry.Remove(context.Background(), "provider-id"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("Close() calls after Remove() = %d, want 1", got)
	}
	if err := registry.Remove(context.Background(), "provider-id"); err != nil {
		t.Fatalf("second Remove() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("Close() calls after second Remove() = %d, want 1", got)
	}
}

func TestRegistryCloseClosesAllProvidersOnce(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(slog.Default())
	var firstCloseCalls atomic.Int32
	var secondCloseCalls atomic.Int32
	if err := registry.RegisterContext(context.Background(), "first", &bootstrapProvider{
		providerType: string(ProviderMem0),
		closeCalls:   &firstCloseCalls,
	}); err != nil {
		t.Fatalf("RegisterContext(first) error = %v", err)
	}
	if err := registry.RegisterContext(context.Background(), "second", &bootstrapProvider{
		providerType: string(ProviderMem0),
		closeCalls:   &secondCloseCalls,
	}); err != nil {
		t.Fatalf("RegisterContext(second) error = %v", err)
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := firstCloseCalls.Load(); got != 1 {
		t.Fatalf("first provider Close() calls = %d, want 1", got)
	}
	if got := secondCloseCalls.Load(); got != 1 {
		t.Fatalf("second provider Close() calls = %d, want 1", got)
	}
}
