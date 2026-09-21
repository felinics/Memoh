package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/background"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

type subagentModelQueries struct {
	dbstore.Queries
	models       []sqlc.Model
	providers    map[string]sqlc.Provider
	defaultModel pgtype.UUID
}

func (q *subagentModelQueries) GetSettingsByBotID(context.Context, pgtype.UUID) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{ChatModelID: q.defaultModel}, nil
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

func TestSpawnAgentRejectsOtherProviderBeforeCreatingSession(t *testing.T) {
	for _, name := range []string{"explicit provider", "implicit provider", "same provider name"} {
		t.Run(name, func(t *testing.T) {
			queries, _, currentModelUUID := newSubagentModelCatalog(t)
			queries.models[0].ModelID = "other-model"
			args := map[string]any{"task": "inspect", "model_id": "other-model"}
			if name == "explicit provider" {
				args["provider"] = "provider-a"
			}
			if name == "same provider name" {
				id := queries.models[0].ProviderID.String()
				other := queries.providers[id]
				other.Name = "provider-b"
				queries.providers[id] = other
				args["provider"] = "provider-b"
			}
			agent := &fakeSpawnAgent{}
			provider, _, sessions, _ := newAgentControlProvider(t, agent)
			provider.models = models.NewService(slog.Default(), queries)
			provider.queries = queries
			provider.modelResolver = provider.resolveModel
			session := SessionContext{BotID: "bot-1", SessionID: "parent-1", CurrentModelUUID: currentModelUUID}

			_, err := executeAgentTool(t, provider, session, ToolSpawnAgent().String(), args)
			if err == nil {
				t.Fatal("expected cross-provider model selection to be rejected")
			}
			if len(sessions.sessions) != 0 || len(agent.queries()) != 0 {
				t.Fatal("rejected selection must not create a session or run a model")
			}
		})
	}
}

func TestSpawnAgentRejectsParentProviderChangedDuringTurn(t *testing.T) {
	queries, _, modelBUUID := newSubagentModelCatalog(t)
	agent := &fakeSpawnAgent{}
	provider, _, sessions, _ := newAgentControlProvider(t, agent)
	provider.models = models.NewService(slog.Default(), queries)
	provider.queries = queries
	provider.modelResolver = provider.resolveModel
	session := SessionContext{
		BotID: "bot-1", SessionID: "parent-1", CurrentModelUUID: modelBUUID,
		CurrentModelProviderID: queries.models[1].ProviderID.String(),
	}
	queries.models[1].ProviderID = queries.models[0].ProviderID
	_, err := executeAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{"task": "inspect"})
	if err == nil || len(sessions.sessions) != 0 || len(agent.queries()) != 0 {
		t.Fatalf("parent provider drift must fail before creating a session: %v", err)
	}
}

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
	for _, want := range []string{"worker-model | provider-b", "[current]", "list_models"} {
		if !strings.Contains(spawnDescription, want) {
			t.Fatalf("spawn description missing %q:\n%s", want, spawnDescription)
		}
	}
	if strings.Contains(spawnDescription, "worker-model | provider-a") {
		t.Fatalf("spawn description includes another provider's model: %s", spawnDescription)
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

func TestSpawnAgentResolvesDuplicateModelIDWithinParentProvider(t *testing.T) {
	queries, _, modelBUUID := newSubagentModelCatalog(t)
	agent := &fakeSpawnAgent{}
	provider, _, _, _ := newAgentControlProvider(t, agent)
	provider.models = models.NewService(slog.Default(), queries)
	provider.queries = queries
	provider.modelResolver = provider.resolveModel
	session := SessionContext{BotID: "bot-1", SessionID: "parent-1", UserID: "user-1", CurrentModelUUID: modelBUUID}

	result := asMap(t, mustExecuteAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
		"task":     "inspect",
		"model_id": "worker-model",
	}))
	if result["model_id"] != "worker-model" || result["provider"] != "provider-b" {
		t.Fatalf("unexpected selected model result: %v", result)
	}
}

func TestSubagentPinsDefaultParentModelAcrossFollowUps(t *testing.T) {
	queries, modelAUUID, modelBUUID := newSubagentModelCatalog(t)
	queries.models[0].ProviderID = queries.models[1].ProviderID
	queries.models[0].ModelID = "other-model"
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
	session.CurrentModelID = "other-model"
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

func TestSubagentRejectsProviderChangeAcrossFollowUps(t *testing.T) {
	for _, change := range []string{"parent model", "pinned model provider"} {
		t.Run(change, func(t *testing.T) {
			queries, modelAUUID, modelBUUID := newSubagentModelCatalog(t)
			worker := queries.models[1]
			worker.ID = mustSubagentUUID(t, "00000000-0000-0000-0000-000000000303")
			worker.ModelID = "worker-only"
			queries.models = append(queries.models, worker)
			agent := &fakeSpawnAgent{}
			provider, _, _, _ := newAgentControlProvider(t, agent)
			provider.models = models.NewService(slog.Default(), queries)
			provider.queries = queries
			provider.modelResolver = provider.resolveModel
			session := SessionContext{BotID: "bot-1", SessionID: "parent-1", CurrentModelUUID: modelBUUID}
			mustExecuteAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
				"id": "worker", "task": "first", "model_id": "worker-only",
			})
			if change == "parent model" {
				session.CurrentModelUUID = modelAUUID
			} else {
				queries.models[2].ProviderID = queries.models[0].ProviderID
				otherID := queries.models[0].ProviderID.String()
				other := queries.providers[otherID]
				other.Name = "provider-b"
				queries.providers[otherID] = other
			}
			result, err := executeAgentTool(t, provider, session, ToolSendMessage().String(), map[string]any{
				"id": "worker", "message": "second",
			})
			if err == nil || len(agent.queries()) != 1 {
				t.Fatalf("provider change must fail before model execution: result=%v err=%v", result, err)
			}
		})
	}
}

func TestSpawnAgentProviderScopeUsesCurrentModelOrBotDefault(t *testing.T) {
	for _, tc := range []struct {
		name         string
		current      string
		requested    string
		wantProvider string
	}{
		{name: "default model", wantProvider: "provider-b"},
		{name: "explicit model within default provider", requested: "worker-model", wantProvider: "provider-b"},
		{name: "current overrides bot default", current: "00000000-0000-0000-0000-000000000301", requested: "worker-model", wantProvider: "provider-a"},
		{name: "invalid current fails closed", current: "00000000-0000-0000-0000-000000000999", requested: "worker-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries, _, modelBUUID := newSubagentModelCatalog(t)
			queries.defaultModel = mustSubagentUUID(t, modelBUUID)
			agent := &fakeSpawnAgent{}
			provider, _, sessions, _ := newAgentControlProvider(t, agent)
			provider.models = models.NewService(slog.Default(), queries)
			provider.queries = queries
			provider.settings = settings.NewService(slog.Default(), queries, nil, nil)
			provider.modelResolver = provider.resolveModel
			session := SessionContext{BotID: "00000000-0000-0000-0000-000000000401", SessionID: "parent-1", CurrentModelUUID: tc.current}
			result, err := executeAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
				"task": "inspect", "model_id": tc.requested,
			})
			if tc.wantProvider == "" {
				if err == nil || len(sessions.sessions) != 0 || len(agent.queries()) != 0 {
					t.Fatalf("invalid parent must fail before creating a session: result=%v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := asMap(t, result)["provider"]; got != tc.wantProvider {
				t.Fatalf("provider = %v, want %s", got, tc.wantProvider)
			}
		})
	}
}

func TestQueuedSubagentRechecksProviderBeforeExecution(t *testing.T) {
	queries, _, modelBUUID := newSubagentModelCatalog(t)
	worker := queries.models[1]
	worker.ID = mustSubagentUUID(t, "00000000-0000-0000-0000-000000000303")
	worker.ModelID = "worker-only"
	queries.models = append(queries.models, worker)
	block := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(block) })
	t.Cleanup(unblock)
	agent := &fakeSpawnAgent{block: block}
	provider, manager, _, _ := newAgentControlProvider(t, agent)
	provider.models = models.NewService(slog.Default(), queries)
	provider.queries = queries
	provider.modelResolver = provider.resolveModel
	session := SessionContext{
		BotID: "bot-1", SessionID: "parent-1", CurrentModelUUID: modelBUUID,
		CurrentModelProviderID: queries.models[1].ProviderID.String(),
	}
	mustExecuteAgentTool(t, provider, session, ToolSpawnAgent().String(), map[string]any{
		"id": "worker", "task": "first", "model_id": "worker-only", "run_in_background": true,
	})
	waitUntil(t, time.Second, func() bool { return len(agent.queries()) == 1 })
	queued := asMap(t, mustExecuteAgentTool(t, provider, session, ToolSendMessage().String(), map[string]any{
		"id": "worker", "message": "second",
	}))
	if queued["status"] != string(background.TaskQueued) {
		t.Fatalf("expected queued follow-up: %v", queued)
	}
	queries.models[2].ProviderID = queries.models[0].ProviderID
	otherID := queries.models[0].ProviderID.String()
	other := queries.providers[otherID]
	other.Name = "provider-b"
	queries.providers[otherID] = other
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, _, err := manager.WaitForSessionTask(ctx, session.BotID, session.SessionID, queued["task_id"].(string), 0)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != background.TaskFailed || len(agent.queries()) != 1 {
		t.Fatalf("queued provider change must fail without executing: %+v", snapshot)
	}
}
