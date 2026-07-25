package tool

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/memoh/domains/agent/engine/background"
	modeldomain "github.com/memohai/memoh/domains/model"
	modelcatalog "github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/internal/oauth"
)

type subagentModelQueries struct {
	models         []modelcatalog.Record
	providers      map[string]modelcatalog.ResolvedProvider
	resolvedUserID string
}

func (q *subagentModelQueries) ResolveModelProvider(ctx context.Context, id string) (modelcatalog.ResolvedProvider, error) {
	q.resolvedUserID = oauth.UserIDFromContext(ctx)
	provider, ok := q.providers[id]
	if !ok {
		return modelcatalog.ResolvedProvider{}, pgx.ErrNoRows
	}
	return provider, nil
}

type subagentModelStore struct {
	modelcatalog.Store
	models []modelcatalog.Record
}

func (s subagentModelStore) ListEnabledByType(_ context.Context, modelType modeldomain.ModelType) ([]modelcatalog.Record, error) {
	var out []modelcatalog.Record
	for _, model := range s.models {
		if model.Enable && model.Type == modelType {
			out = append(out, model)
		}
	}
	return out, nil
}

func (s subagentModelStore) GetByID(_ context.Context, id string) (modelcatalog.Record, error) {
	for _, model := range s.models {
		if model.ID == id {
			return model, nil
		}
	}
	return modelcatalog.Record{}, modelcatalog.ErrModelNotFound
}

func newSubagentModelCatalog(t *testing.T) (*subagentModelQueries, string, string) {
	t.Helper()
	providerAID := "00000000-0000-0000-0000-000000000201"
	providerBID := "00000000-0000-0000-0000-000000000202"
	modelAID := "00000000-0000-0000-0000-000000000301"
	modelBID := "00000000-0000-0000-0000-000000000302"
	modelConfigA, _ := json.Marshal(modeldomain.ModelConfig{Description: ptr("Fast coding worker"), Compatibilities: []string{modeldomain.CompatToolCall}})
	modelConfigB, _ := json.Marshal(modeldomain.ModelConfig{Description: ptr("Long-context worker"), Compatibilities: []string{modeldomain.CompatToolCall, modeldomain.CompatVision}})
	q := &subagentModelQueries{
		models: []modelcatalog.Record{
			{ID: modelAID, ModelID: "worker-model", Name: "worker-model", ProviderID: providerAID, Type: modeldomain.ModelTypeChat, Enable: true, Config: modelConfigA},
			{ID: modelBID, ModelID: "worker-model", Name: "worker-model", ProviderID: providerBID, Type: modeldomain.ModelTypeChat, Enable: true, Config: modelConfigB},
		},
		providers: map[string]modelcatalog.ResolvedProvider{
			providerAID: {ID: providerAID, Name: "provider-a", ClientType: modeldomain.ClientTypeOpenAICompletions, Enable: true, APIKey: "test-key"},
			providerBID: {ID: providerBID, Name: "provider-b", ClientType: modeldomain.ClientTypeOpenAICompletions, Enable: true, APIKey: "test-key"},
		},
	}
	return q, modelAID, modelBID
}

func ptr[T any](value T) *T { return &value }

func TestListModelsReturnsEnabledCatalogAndMarksCurrent(t *testing.T) {
	queries, _, currentModelUUID := newSubagentModelCatalog(t)
	modelService := modelcatalog.NewService(slog.Default(), subagentModelStore{models: queries.models})
	provider := NewSpawnProvider(nil, nil, modelService, queries, nil, background.New(nil))
	provider.SetAgent(&fakeSpawnAgent{})
	session := SessionContext{
		BotID:                "bot-1",
		SessionID:            "session-1",
		UserID:               "user-1",
		CurrentModelUUID:     currentModelUUID,
		CurrentModelID:       "worker-model",
		CurrentModelProvider: "provider-b",
	}

	toolset, err := provider.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if queries.resolvedUserID != session.UserID {
		t.Fatalf("resolved user id = %q, want %q", queries.resolvedUserID, session.UserID)
	}
	var spawnDescription string
	for _, tool := range toolset {
		if tool.Name == ToolSpawnAgent().String() {
			spawnDescription = tool.Description
		}
	}
	for _, want := range []string{"worker-model | provider-a", "worker-model | provider-b", "[current]", "list_models"} {
		if !strings.Contains(spawnDescription, want) {
			t.Fatalf("spawn description missing %q:\n%s", want, spawnDescription)
		}
	}

	result, err := executeAgentTool(t, provider, session, ToolListModels().String(), map[string]any{})
	if err != nil {
		t.Fatalf("list_models: %v", err)
	}
	output := asMap(t, result)
	items := output["models"].([]map[string]any)
	if len(items) != 2 || items[0]["provider"] != "provider-b" || items[0]["current"] != true {
		t.Fatalf("expected current model first, got %v", output)
	}
	if output["current_model_id"] != "worker-model" || output["current_provider"] != "provider-b" {
		t.Fatalf("unexpected current model metadata: %v", output)
	}
}

func TestSpawnAgentRequiresProviderForAmbiguousModelID(t *testing.T) {
	queries, _, _ := newSubagentModelCatalog(t)
	agent := &fakeSpawnAgent{}
	provider, _, sessions, _ := newAgentControlProvider(t, agent)
	provider.models = modelcatalog.NewService(slog.Default(), subagentModelStore{models: queries.models})
	provider.resolver = queries
	provider.modelResolver = provider.resolveModel
	session := SessionContext{BotID: "bot-1", SessionID: "parent-1", UserID: "user-1"}

	_, err := executeAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
		"task":     "inspect",
		"model_id": "worker-model",
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "provider-a") || !strings.Contains(err.Error(), "provider-b") {
		t.Fatalf("expected provider ambiguity error, got %v", err)
	}
	if len(sessions.sessions) != 0 {
		t.Fatalf("model validation must happen before child session creation, got %d sessions", len(sessions.sessions))
	}

	result := asMap(t, mustExecuteAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
		"task":     "inspect",
		"model_id": "worker-model",
		"provider": "provider-b",
	}))
	if result["model_id"] != "worker-model" || result["provider"] != "provider-b" {
		t.Fatalf("unexpected selected model result: %v", result)
	}
}

func TestSubagentPinsDefaultParentModelAcrossFollowUps(t *testing.T) {
	queries, modelAUUID, modelBUUID := newSubagentModelCatalog(t)
	agent := &fakeSpawnAgent{}
	provider, _, _, _ := newAgentControlProvider(t, agent)
	provider.models = modelcatalog.NewService(slog.Default(), subagentModelStore{models: queries.models})
	provider.resolver = queries
	provider.modelResolver = provider.resolveModel
	session := SessionContext{
		BotID:                "bot-1",
		SessionID:            "parent-1",
		UserID:               "user-1",
		CurrentModelUUID:     modelBUUID,
		CurrentModelID:       "worker-model",
		CurrentModelProvider: "provider-b",
	}

	mustExecuteAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
		"id":   "worker",
		"task": "first",
	})
	session.CurrentModelUUID = modelAUUID
	session.CurrentModelProvider = "provider-a"
	mustExecuteAgentTool(t, provider, session, ToolSendMessage().String(), map[string]any{
		"id":      "worker",
		"message": "second",
	})

	first, ok := agent.callAt(0)
	if !ok {
		t.Fatal("expected first subagent call")
	}
	second, ok := agent.callAt(1)
	if !ok {
		t.Fatal("expected follow-up subagent call")
	}
	if first.ModelUUID != modelBUUID || second.ModelUUID != modelBUUID || second.ModelProvider != "provider-b" {
		t.Fatalf("expected pinned provider-b model, first=%+v second=%+v", first, second)
	}
}
