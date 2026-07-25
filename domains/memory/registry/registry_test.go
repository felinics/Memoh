package registry

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

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
func (*bootstrapProvider) Add(context.Context, AddRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) Search(context.Context, SearchRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) GetAll(context.Context, GetAllRequest) (SearchResponse, error) {
	return SearchResponse{}, nil
}

func (*bootstrapProvider) Update(context.Context, UpdateRequest) (memorydomain.Item, error) {
	return memorydomain.Item{}, nil
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
	reg := NewRegistry(slog.Default(), registryTeamResolver)
	reg.RegisterFactory(ProviderMem0, func(_ context.Context, teamID, _ string, _ map[string]any) (Instance, error) {
		return &bootstrapProvider{providerType: teamID}, nil
	})
	reg.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return ProviderMem0, map[string]any{}, nil
	})

	teamA := teamRegistryContext("team-a")
	teamB := teamRegistryContext("team-b")
	providerA, err := reg.Get(teamA, "shared-provider-id")
	if err != nil {
		t.Fatalf("Get(team-a) error = %v", err)
	}
	providerB, err := reg.Get(teamB, "shared-provider-id")
	if err != nil {
		t.Fatalf("Get(team-b) error = %v", err)
	}
	if providerA == providerB {
		t.Fatal("same provider id reused one instance across teams")
	}
	providerAAgain, err := reg.Get(teamA, "shared-provider-id")
	if err != nil {
		t.Fatalf("second Get(team-a) error = %v", err)
	}
	if providerAAgain != providerA {
		t.Fatal("team-local provider instance was not cached")
	}
}

func TestRegistryConcurrentMissInstantiatesOnce(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(slog.Default())
	var factoryCalls atomic.Int32
	reg.RegisterFactory(ProviderMem0, func(_ context.Context, _, _ string, _ map[string]any) (Instance, error) {
		factoryCalls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &bootstrapProvider{providerType: ProviderMem0}, nil
	})
	reg.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return ProviderMem0, map[string]any{}, nil
	})

	const workers = 24
	providers := make([]Instance, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func() {
			defer wg.Done()
			providers[i], errs[i] = reg.Get(context.Background(), "provider-id")
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
	reg := NewRegistry(slog.Default())
	oldFactoryStarted := make(chan struct{})
	releaseOldFactory := make(chan struct{})
	reg.RegisterFactory(ProviderMem0, func(_ context.Context, _, _ string, config map[string]any) (Instance, error) {
		version, _ := config["version"].(string)
		if version == "old" {
			close(oldFactoryStarted)
			<-releaseOldFactory
		}
		return &bootstrapProvider{providerType: version}, nil
	})
	reg.SetConfigLoader(func(_ context.Context, _ string) (string, map[string]any, error) {
		return ProviderMem0, map[string]any{"version": "old"}, nil
	})

	oldResult := make(chan Instance, 1)
	oldErr := make(chan error, 1)
	go func() {
		provider, err := reg.Get(context.Background(), "provider-id")
		oldResult <- provider
		oldErr <- err
	}()
	<-oldFactoryStarted

	updateErr := make(chan error, 1)
	go func() {
		if err := reg.Remove(context.Background(), "provider-id"); err != nil {
			updateErr <- err
			return
		}
		_, err := reg.Instantiate(context.Background(), "provider-id", ProviderMem0, map[string]any{"version": "new"})
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

	provider, err := reg.Get(context.Background(), "provider-id")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if got := provider.Type(); got != "new" {
		t.Fatalf("provider after update = %q, want new", got)
	}
}

func TestRegistryFailsClosedWithoutTeam(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(slog.Default(), registryTeamResolver)
	if _, err := reg.Get(context.Background(), "provider-id"); err == nil {
		t.Fatal("Get() without team context succeeded")
	}
}

func TestRegistryRemoveClosesProvider(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(slog.Default())
	var closeCalls atomic.Int32
	provider := &bootstrapProvider{providerType: ProviderMem0, closeCalls: &closeCalls}
	if err := reg.RegisterContext(context.Background(), "provider-id", provider); err != nil {
		t.Fatalf("RegisterContext() error = %v", err)
	}

	if err := reg.Remove(context.Background(), "provider-id"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("Close() calls after Remove() = %d, want 1", got)
	}
	if err := reg.Remove(context.Background(), "provider-id"); err != nil {
		t.Fatalf("second Remove() error = %v", err)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("Close() calls after second Remove() = %d, want 1", got)
	}
}

func TestRegistryCloseClosesAllProvidersOnce(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(slog.Default())
	var firstCloseCalls atomic.Int32
	var secondCloseCalls atomic.Int32
	if err := reg.RegisterContext(context.Background(), "first", &bootstrapProvider{
		providerType: ProviderMem0,
		closeCalls:   &firstCloseCalls,
	}); err != nil {
		t.Fatalf("RegisterContext(first) error = %v", err)
	}
	if err := reg.RegisterContext(context.Background(), "second", &bootstrapProvider{
		providerType: ProviderMem0,
		closeCalls:   &secondCloseCalls,
	}); err != nil {
		t.Fatalf("RegisterContext(second) error = %v", err)
	}

	if err := reg.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := reg.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := firstCloseCalls.Load(); got != 1 {
		t.Fatalf("first provider Close() calls = %d, want 1", got)
	}
	if got := secondCloseCalls.Load(); got != 1 {
		t.Fatalf("second provider Close() calls = %d, want 1", got)
	}
}
