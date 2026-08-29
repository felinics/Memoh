package core

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	membuiltin "github.com/felinics/memoh/internal/memory/adapters/builtin"
	modelspkg "github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

func TestACPToolProvidersIncludeAskUser(t *testing.T) {
	providers := acpToolProviders([]agenttools.ToolProvider{
		agenttools.NewAskUserProvider(slog.Default()),
		agenttools.NewSkillProvider(slog.Default()),
	})

	foundAskUser := false
	for _, provider := range providers {
		if _, ok := provider.(*agenttools.AskUserProvider); ok {
			foundAskUser = true
		}
	}
	if !foundAskUser {
		t.Fatal("ask_user should be exposed to ACP")
	}
	if len(providers) != 2 {
		t.Fatalf("filtered providers = %d, want 2", len(providers))
	}
}

func TestAgentLimitsFromConfigUsesCustomValues(t *testing.T) {
	got := agentLimitsFromConfig(config.AgentConfig{
		ToolOutputMaxBytes:  1234,
		ToolOutputMaxLines:  56,
		SystemFilesMaxBytes: 7890,
	})

	if got.ToolOutputMaxBytes != 1234 ||
		got.ToolOutputMaxLines != 56 ||
		got.SystemFilesMaxBytes != 7890 {
		t.Fatalf("agent limits = %#v", got)
	}
}

func TestAgentLoopReselectModeFromConfigDefaultsToActiveWhenUnrecognized(t *testing.T) {
	got := agentLoopReselectModeFromConfig(slog.Default(), config.AgentConfig{ContextLoopReselect: "bogus"})
	if got != native.LoopReselectActive {
		t.Fatalf("mode = %q, want active", got)
	}
}

func TestAgentLoopReselectModeFromConfigHonorsShadow(t *testing.T) {
	got := agentLoopReselectModeFromConfig(slog.Default(), config.AgentConfig{ContextLoopReselect: "shadow"})
	if got != native.LoopReselectShadow {
		t.Fatalf("mode = %q, want shadow", got)
	}
}

func TestProvideAgentWiresLoopReselectMode(t *testing.T) {
	agent := provideAgent(slog.Default(), nil, nil, config.Config{Agent: config.AgentConfig{ContextLoopReselect: "off"}})
	if agent == nil {
		t.Fatal("expected a non-nil agent")
	}
	if got := agent.LoopReselectMode(); got != native.LoopReselectOff {
		t.Fatalf("LoopReselectMode() = %q, want off", got)
	}
}

func TestLazyLLMCompactResolvesModelWithRequestBotID(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &lazyLLMTestQueries{
		botID:           botID,
		compactionModel: mustTestUUID("22222222-2222-2222-2222-222222222222"),
		providerID:      mustTestUUID("33333333-3333-3333-3333-333333333333"),
	}
	client := &lazyLLMClient{
		modelsService:   modelspkg.NewService(slog.Default(), queries),
		settingsService: settings.NewService(slog.Default(), queries, nil, nil),
		queries:         queries,
		timeout:         time.Second,
		logger:          slog.Default(),
	}

	if _, err := client.Compact(context.Background(), memprovider.CompactRequest{BotID: botID}); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if queries.settingsLookups != 1 {
		t.Fatalf("settings lookups = %d, want 1", queries.settingsLookups)
	}
	if queries.configuredLookups == 0 {
		t.Fatal("configured bot model was not resolved")
	}
	if queries.fallbackLookups != 0 {
		t.Fatalf("fallback lookups = %d, want 0", queries.fallbackLookups)
	}
}

func TestConfigureMemoryProviderRegistryLoadsPersistedProviderOnCacheMiss(t *testing.T) {
	queries := &memoryProviderLazyLoadQueries{}
	service := memprovider.NewService(slog.Default(), queries, config.Config{})
	registry := memprovider.NewRegistry(slog.Default())
	registry.RegisterFactory(string(memprovider.ProviderBuiltin), func(context.Context, string, string, map[string]any) (memprovider.Provider, error) {
		return membuiltin.NewBuiltinProvider(slog.Default(), nil), nil
	})

	configureMemoryProviderRegistry(service, registry)

	if _, err := registry.Get(context.Background(), "44444444-4444-4444-4444-444444444444"); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

type memoryProviderLazyLoadQueries struct {
	dbstore.Queries
}

func (*memoryProviderLazyLoadQueries) GetMemoryProviderByID(context.Context, pgtype.UUID) (sqlc.MemoryProvider, error) {
	return sqlc.MemoryProvider{Provider: string(memprovider.ProviderBuiltin)}, nil
}

type lazyLLMTestQueries struct {
	dbstore.Queries
	botID             string
	compactionModel   pgtype.UUID
	providerID        pgtype.UUID
	settingsLookups   int
	fallbackLookups   int
	configuredLookups int
}

func (q *lazyLLMTestQueries) GetSettingsByBotID(_ context.Context, id pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	q.settingsLookups++
	if id.String() != q.botID {
		return sqlc.GetSettingsByBotIDRow{}, errors.New("unexpected bot id")
	}
	return sqlc.GetSettingsByBotIDRow{
		BotID:             id,
		CompactionModelID: q.compactionModel,
	}, nil
}

func (q *lazyLLMTestQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	q.configuredLookups++
	if id.String() != q.compactionModel.String() {
		return sqlc.Model{}, errors.New("unexpected model id")
	}
	return sqlc.Model{
		ID:         id,
		ModelID:    "compact-model",
		ProviderID: q.providerID,
		Type:       string(modelspkg.ModelTypeChat),
		Enable:     true,
	}, nil
}

func (q *lazyLLMTestQueries) ListEnabledModelsByType(context.Context, string) ([]sqlc.Model, error) {
	q.fallbackLookups++
	return nil, errors.New("fallback model lookup should not be used")
}

func (q *lazyLLMTestQueries) GetProviderByID(_ context.Context, id pgtype.UUID) (sqlc.Provider, error) {
	if id.String() != q.providerID.String() {
		return sqlc.Provider{}, errors.New("unexpected provider id")
	}
	return sqlc.Provider{
		ID:         id,
		Name:       "test-provider",
		ClientType: string(modelspkg.ClientTypeOpenAIResponses),
		Enable:     true,
		Config:     []byte(`{"base_url":"http://127.0.0.1","api_key":"test"}`),
	}, nil
}

func (*lazyLLMTestQueries) ListModelsByModelID(_ context.Context, _ string) ([]sqlc.Model, error) {
	return nil, nil
}

func mustTestUUID(s string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		panic(err)
	}
	return id
}
