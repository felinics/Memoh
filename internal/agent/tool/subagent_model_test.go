package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/background"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
)

type subagentModelQueries struct {
	dbstore.Queries
	models    []sqlc.Model
	providers map[string]sqlc.Provider
}

func (q *subagentModelQueries) ListEnabledModelsByType(_ context.Context, modelType string) ([]sqlc.Model, error) {
	var out []sqlc.Model
	for _, model := range q.models {
		if model.Enable && model.Type == modelType {
			out = append(out, model)
		}
	}
	return out, nil
}

func (q *subagentModelQueries) GetProviderByID(_ context.Context, id pgtype.UUID) (sqlc.Provider, error) {
	provider, ok := q.providers[id.String()]
	if !ok {
		return sqlc.Provider{}, pgx.ErrNoRows
	}
	return provider, nil
}

func (q *subagentModelQueries) GetModelByID(_ context.Context, id pgtype.UUID) (sqlc.Model, error) {
	for _, model := range q.models {
		if model.ID == id {
			return model, nil
		}
	}
	return sqlc.Model{}, pgx.ErrNoRows
}

func mustSubagentUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	id, err := dbpkg.ParseUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func newSubagentModelCatalog(t *testing.T) (*subagentModelQueries, string, string) {
	t.Helper()
	providerAID := "00000000-0000-0000-0000-000000000201"
	providerBID := "00000000-0000-0000-0000-000000000202"
	modelAID := "00000000-0000-0000-0000-000000000301"
	modelBID := "00000000-0000-0000-0000-000000000302"
	providerConfig, _ := json.Marshal(map[string]any{"api_key": "test-key"})
	modelConfigA, _ := json.Marshal(models.ModelConfig{Description: ptr("Fast coding worker"), Compatibilities: []string{models.CompatToolCall}})
	modelConfigB, _ := json.Marshal(models.ModelConfig{Description: ptr("Long-context worker"), Compatibilities: []string{models.CompatToolCall, models.CompatVision}})
	q := &subagentModelQueries{
		models: []sqlc.Model{
			{ID: mustSubagentUUID(t, modelAID), ModelID: "worker-model", ProviderID: mustSubagentUUID(t, providerAID), Type: string(models.ModelTypeChat), Enable: true, Config: modelConfigA},
			{ID: mustSubagentUUID(t, modelBID), ModelID: "worker-model", ProviderID: mustSubagentUUID(t, providerBID), Type: string(models.ModelTypeChat), Enable: true, Config: modelConfigB},
		},
		providers: map[string]sqlc.Provider{
			providerAID: {ID: mustSubagentUUID(t, providerAID), Name: "provider-a", ClientType: string(models.ClientTypeOpenAICompletions), Enable: true, Config: providerConfig},
			providerBID: {ID: mustSubagentUUID(t, providerBID), Name: "provider-b", ClientType: string(models.ClientTypeOpenAICompletions), Enable: true, Config: providerConfig},
		},
	}
	return q, modelAID, modelBID
}

func ptr[T any](value T) *T { return &value }

func TestListModelsReturnsEnabledCatalogAndMarksCurrent(t *testing.T) {
	queries, _, currentModelUUID := newSubagentModelCatalog(t)
	modelService := models.NewService(slog.Default(), queries)
	provider := NewSpawnProvider(nil, nil, modelService, queries, nil, background.New(nil))
	provider.SetAgent(&fakeSpawnAgent{})
	session := SessionContext{
		BotID:                "bot-1",
		SessionID:            "session-1",
		CurrentModelUUID:     currentModelUUID,
		CurrentModelID:       "worker-model",
		CurrentModelProvider: "provider-b",
	}

	toolset, err := provider.Tools(context.Background(), session)
	if err != nil {
		t.Fatalf("Tools: %v", err)
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
	provider.models = models.NewService(slog.Default(), queries)
	provider.queries = queries
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
	provider.models = models.NewService(slog.Default(), queries)
	provider.queries = queries
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
