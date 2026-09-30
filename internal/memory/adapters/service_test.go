package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
)

type memoryConfigQueries struct {
	dbstore.Queries
	rows        []sqlc.MemoryProvider
	createCalls int
}

func (q *memoryConfigQueries) GetBuiltinMemoryProvider(context.Context) (sqlc.MemoryProvider, error) {
	for _, row := range q.rows {
		if row.Provider == string(ProviderBuiltin) {
			return row, nil
		}
	}
	return sqlc.MemoryProvider{}, pgx.ErrNoRows
}

func (q *memoryConfigQueries) CreateMemoryProvider(_ context.Context, arg sqlc.CreateMemoryProviderParams) (sqlc.MemoryProvider, error) {
	q.createCalls++
	row := sqlc.MemoryProvider{
		ID:        pgtype.UUID{Bytes: [16]byte{0xcc, uint8(q.createCalls)}, Valid: true}, // #nosec G115 -- test fixture counter stays tiny
		Name:      arg.Name,
		Provider:  arg.Provider,
		Config:    arg.Config,
		IsDefault: arg.IsDefault,
	}
	q.rows = append(q.rows, row)
	return row, nil
}

func (q *memoryConfigQueries) UpdateMemoryProvider(_ context.Context, arg sqlc.UpdateMemoryProviderParams) (sqlc.MemoryProvider, error) {
	for i := range q.rows {
		if q.rows[i].ID == arg.ID {
			q.rows[i].Name = arg.Name
			q.rows[i].Config = arg.Config
			return q.rows[i], nil
		}
	}
	return sqlc.MemoryProvider{}, pgx.ErrNoRows
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

func (*bootstrapProvider) Delete(context.Context, string, string) (DeleteResponse, error) {
	return DeleteResponse{}, nil
}

func (*bootstrapProvider) DeleteBatch(context.Context, string, []string) (DeleteResponse, error) {
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

func TestGetConfigWithoutBuiltinRowReadsEmpty(t *testing.T) {
	t.Parallel()
	queries := &memoryConfigQueries{}
	service := NewService(slog.Default(), queries, config.Config{})

	cfg, err := service.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("GetConfig() error = %v", err)
	}
	if cfg.EmbeddingModelID != "" {
		t.Fatalf("embedding model = %q, want empty", cfg.EmbeddingModelID)
	}
	if queries.createCalls != 0 {
		t.Fatalf("GetConfig() created %d rows, want 0", queries.createCalls)
	}
}

func TestUpdateConfigCreatesBuiltinRowOnFirstSave(t *testing.T) {
	t.Parallel()
	queries := &memoryConfigQueries{}
	service := NewService(slog.Default(), queries, config.Config{})
	model := "embedding-model"

	cfg, err := service.UpdateConfig(context.Background(), MemoryConfigUpdateRequest{EmbeddingModelID: &model})
	if err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if cfg.EmbeddingModelID != model {
		t.Fatalf("embedding model = %q, want %q", cfg.EmbeddingModelID, model)
	}
	if queries.createCalls != 1 || len(queries.rows) != 1 {
		t.Fatalf("builtin rows = %d (creates %d), want 1", len(queries.rows), queries.createCalls)
	}
	var stored map[string]any
	if err := json.Unmarshal(queries.rows[0].Config, &stored); err != nil {
		t.Fatalf("stored config: %v", err)
	}
	if stored["memory_mode"] != "graph" || stored["embedding_model_id"] != model {
		t.Fatalf("stored config = %v", stored)
	}
}

func TestUpdateConfigClearsEmbeddingAndKeepsOtherKeys(t *testing.T) {
	t.Parallel()
	queries := &memoryConfigQueries{rows: []sqlc.MemoryProvider{{
		ID:       pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		Name:     "Built-in",
		Provider: string(ProviderBuiltin),
		Config:   []byte(`{"memory_mode":"graph","embedding_model_id":"old","context_target_items":4}`),
	}}}
	service := NewService(slog.Default(), queries, config.Config{})
	empty := ""

	cfg, err := service.UpdateConfig(context.Background(), MemoryConfigUpdateRequest{EmbeddingModelID: &empty})
	if err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}
	if cfg.EmbeddingModelID != "" {
		t.Fatalf("embedding model = %q, want cleared", cfg.EmbeddingModelID)
	}
	if queries.createCalls != 0 {
		t.Fatalf("UpdateConfig() created %d rows, want 0", queries.createCalls)
	}
	var stored map[string]any
	if err := json.Unmarshal(queries.rows[0].Config, &stored); err != nil {
		t.Fatalf("stored config: %v", err)
	}
	if _, ok := stored["embedding_model_id"]; ok {
		t.Fatalf("embedding_model_id still stored: %v", stored)
	}
	if stored["context_target_items"] != float64(4) {
		t.Fatalf("context_target_items = %v, want 4", stored["context_target_items"])
	}
	if queries.rows[0].Name != "Built-in" {
		t.Fatalf("name = %q, want unchanged", queries.rows[0].Name)
	}
}

func TestEnsureBuiltinIDReusesExistingRow(t *testing.T) {
	t.Parallel()
	existing := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	queries := &memoryConfigQueries{rows: []sqlc.MemoryProvider{{
		ID:       existing,
		Name:     "Built-in",
		Provider: string(ProviderBuiltin),
		Config:   []byte(`{}`),
	}}}
	service := NewService(slog.Default(), queries, config.Config{})

	id, err := service.EnsureBuiltinID(context.Background())
	if err != nil {
		t.Fatalf("EnsureBuiltinID() error = %v", err)
	}
	if id != existing.String() {
		t.Fatalf("EnsureBuiltinID() = %q, want %q", id, existing.String())
	}
	if queries.createCalls != 0 {
		t.Fatalf("EnsureBuiltinID() created %d rows, want 0", queries.createCalls)
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
	registry.RegisterFactory(string(ProviderBuiltin), func(_ context.Context, teamID, _ string, _ map[string]any) (Provider, error) {
		return &bootstrapProvider{providerType: teamID}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderBuiltin), map[string]any{}, nil
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
	registry.RegisterFactory(string(ProviderBuiltin), func(_ context.Context, _, _ string, _ map[string]any) (Provider, error) {
		factoryCalls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &bootstrapProvider{providerType: string(ProviderBuiltin)}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderBuiltin), map[string]any{}, nil
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
	registry.RegisterFactory(string(ProviderBuiltin), func(_ context.Context, _, _ string, config map[string]any) (Provider, error) {
		version, _ := config["version"].(string)
		if version == "old" {
			close(oldFactoryStarted)
			<-releaseOldFactory
		}
		return &bootstrapProvider{providerType: version}, nil
	})
	registry.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return string(ProviderBuiltin), map[string]any{"version": "old"}, nil
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
		_, err := registry.Instantiate(context.Background(), "provider-id", string(ProviderBuiltin), map[string]any{"version": "new"})
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
	provider := &bootstrapProvider{providerType: string(ProviderBuiltin), closeCalls: &closeCalls}
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
		providerType: string(ProviderBuiltin),
		closeCalls:   &firstCloseCalls,
	}); err != nil {
		t.Fatalf("RegisterContext(first) error = %v", err)
	}
	if err := registry.RegisterContext(context.Background(), "second", &bootstrapProvider{
		providerType: string(ProviderBuiltin),
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
