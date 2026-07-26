package core

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/agent/tool"
	"github.com/memohai/memoh/domains/api/bot/setting"
	settingpersistence "github.com/memohai/memoh/domains/api/bot/setting/persistence"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelspkg "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/config"
)

func TestACPToolProvidersIncludeAskUser(t *testing.T) {
	providers := acpToolProviders([]tool.ToolProvider{
		tool.NewAskUserProvider(slog.Default()),
		tool.NewSkillProvider(slog.Default()),
	})

	foundAskUser := false
	for _, provider := range providers {
		if _, ok := provider.(*tool.AskUserProvider); ok {
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

func TestLazyLLMCompactResolvesModelWithRequestBotID(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &lazyLLMTestQueries{
		botID:           botID,
		compactionModel: "22222222-2222-2222-2222-222222222222",
		providerID:      "33333333-3333-3333-3333-333333333333",
	}
	settingsStore := lazyLLMSettingsStore{queries: queries}
	client := &lazyLLMClient{
		modelsService:    modelspkg.NewService(slog.Default(), lazyLLMModelStore{queries: queries}),
		settingsService:  setting.NewService(slog.Default(), settingsStore, settingsStore, nil, nil),
		providerResolver: lazyLLMProviderResolver{queries: queries},
		timeout:          time.Second,
		logger:           slog.Default(),
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

type lazyLLMModelStore struct {
	modelspkg.Store
	queries *lazyLLMTestQueries
}

func (s lazyLLMModelStore) GetByID(_ context.Context, id string) (modelspkg.Record, error) {
	s.queries.configuredLookups++
	if id != s.queries.compactionModel {
		return modelspkg.Record{}, errors.New("unexpected model id")
	}
	return modelspkg.Record{
		ID:         id,
		ModelID:    "compact-model",
		ProviderID: s.queries.providerID,
		Type:       modeldomain.ModelTypeChat,
		Enable:     true,
	}, nil
}

func (lazyLLMModelStore) GetByModelID(context.Context, string) (modelspkg.Record, error) {
	return modelspkg.Record{}, modelspkg.ErrModelNotFound
}

func (s lazyLLMModelStore) ListEnabledByType(context.Context, modeldomain.ModelType) ([]modelspkg.Record, error) {
	s.queries.fallbackLookups++
	return nil, errors.New("fallback model lookup should not be used")
}

type lazyLLMSettingsStore struct {
	queries *lazyLLMTestQueries
}

func (s lazyLLMSettingsStore) Get(_ context.Context, botID string) (settingpersistence.Record, error) {
	s.queries.settingsLookups++
	if botID != s.queries.botID {
		return settingpersistence.Record{}, errors.New("unexpected bot id")
	}
	return settingpersistence.Record{
		CompactionModelID: s.queries.compactionModel,
	}, nil
}

func (lazyLLMSettingsStore) GetBot(context.Context, string) (settingpersistence.BotRecord, error) {
	return settingpersistence.BotRecord{}, errors.New("not implemented")
}

func (lazyLLMSettingsStore) GetOverlay(context.Context, string) (settingpersistence.OverlayRecord, error) {
	return settingpersistence.OverlayRecord{}, errors.New("not implemented")
}

func (lazyLLMSettingsStore) Upsert(context.Context, settingpersistence.UpsertInput) (settingpersistence.Record, error) {
	return settingpersistence.Record{}, errors.New("not implemented")
}

func (lazyLLMSettingsStore) Delete(context.Context, string) error {
	return errors.New("not implemented")
}

func (lazyLLMSettingsStore) ModelExists(context.Context, string) (bool, error) {
	return false, errors.New("not implemented")
}

func (lazyLLMSettingsStore) ListModelIDs(context.Context, string) ([]string, error) {
	return nil, errors.New("not implemented")
}

type lazyLLMTestQueries struct {
	botID             string
	compactionModel   string
	providerID        string
	settingsLookups   int
	fallbackLookups   int
	configuredLookups int
}

type lazyLLMProviderResolver struct {
	queries *lazyLLMTestQueries
}

func (r lazyLLMProviderResolver) ResolveModelProvider(_ context.Context, id string) (modelspkg.ResolvedProvider, error) {
	if id != r.queries.providerID {
		return modelspkg.ResolvedProvider{}, errors.New("unexpected provider id")
	}
	return modelspkg.ResolvedProvider{
		ID:         id,
		Name:       "test-provider",
		ClientType: modeldomain.ClientTypeOpenAIResponses,
		Enable:     true,
		BaseURL:    "http://127.0.0.1",
		APIKey:     "test",
	}, nil
}
