package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/reasoning"
)

type providerModelsReasoningQueries struct {
	dbstore.Queries
	provider sqlc.Provider
	models   []sqlc.Model
}

func (q *providerModelsReasoningQueries) GetProviderByID(_ context.Context, _ pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func (q *providerModelsReasoningQueries) ListModelsByProviderID(_ context.Context, _ pgtype.UUID) ([]sqlc.Model, error) {
	return q.models, nil
}

// A handler with no providers service stands in for the case where a provider
// row cannot be read: the model's own tiers still resolve, only the wire policy
// is missing. Omitting the field there would send clients back to deriving it.
func newReasoningTestHandler() *ModelsHandler {
	return &ModelsHandler{logger: slog.Default()}
}

func chatModel(cfg models.ModelConfig) models.GetResponse {
	return models.GetResponse{
		ID:      "00000000-0000-0000-0000-000000000001",
		ModelID: "test-model",
		Model: models.Model{
			ModelID: "test-model",
			Type:    models.ModelTypeChat,
			Config:  cfg,
		},
	}
}

func TestWithReasoningFillsResolvedOptions(t *testing.T) {
	t.Parallel()

	list := newReasoningTestHandler().withReasoning(context.Background(), []models.GetResponse{
		chatModel(models.ModelConfig{
			ThinkingMode:     models.ThinkingModeToggle,
			ReasoningEfforts: []string{models.ReasoningEffortDisable, models.ReasoningEffortLow, models.ReasoningEffortHigh},
		}),
	})

	got := list[0].Reasoning
	if got == nil {
		t.Fatal("chat model should carry resolved reasoning options")
	}
	if !got.Supported || !got.CanDisable {
		t.Fatalf("expected a supported, disableable model: %+v", got)
	}
	// The disable token declares a capability; it must not come back as a tier.
	for _, e := range got.Efforts {
		if e == models.ReasoningEffortDisable {
			t.Fatalf("disable leaked into the tier list: %v", got.Efforts)
		}
	}
	if got.DefaultEffort == "" {
		t.Fatal("a supported model should carry a default effort")
	}
}

func TestWithReasoningMarksModelsThatCannotThink(t *testing.T) {
	t.Parallel()

	list := newReasoningTestHandler().withReasoning(context.Background(), []models.GetResponse{
		chatModel(models.ModelConfig{ThinkingMode: models.ThinkingModeNone}),
	})

	got := list[0].Reasoning
	if got == nil {
		t.Fatal("the field should be present and honest, not omitted")
	}
	if got.Supported || got.CanDisable || len(got.Efforts) > 0 {
		t.Fatalf("a model with no thinking concept should offer nothing: %+v", got)
	}
}

func TestWithReasoningSkipsNonChatModels(t *testing.T) {
	t.Parallel()

	embedding := chatModel(models.ModelConfig{})
	embedding.Type = models.ModelTypeEmbedding

	list := newReasoningTestHandler().withReasoning(context.Background(), []models.GetResponse{embedding})
	if list[0].Reasoning != nil {
		t.Fatalf("embedding models have no reasoning control: %+v", list[0].Reasoning)
	}
}

func TestProviderModelListIncludesResolvedReasoning(t *testing.T) {
	t.Parallel()

	const providerID = "00000000-0000-0000-0000-000000000010"
	providerUUID, err := db.ParseUUID(providerID)
	if err != nil {
		t.Fatal(err)
	}
	modelUUID, err := db.ParseUUID("00000000-0000-0000-0000-000000000011")
	if err != nil {
		t.Fatal(err)
	}
	queries := &providerModelsReasoningQueries{
		provider: sqlc.Provider{
			ID:         providerUUID,
			Name:       "Google",
			ClientType: string(models.ClientTypeGoogleGenerativeAI),
			Enable:     true,
			Config:     []byte(`{}`),
			Metadata:   []byte(`{}`),
		},
		models: []sqlc.Model{{
			ID:         modelUUID,
			ProviderID: providerUUID,
			ModelID:    "gemini-2.5-flash-lite",
			Type:       string(models.ModelTypeChat),
			Enable:     true,
			Config: []byte(`{
				"compatibilities":["reasoning"],
				"thinking_mode":"toggle",
				"reasoning_dialect":"budget",
				"reasoning_off_support":"accepted",
				"reasoning_efforts":["low","medium","high"],
				"thinking_budget_min":512,
				"thinking_budget_max":24576
			}`),
		}},
	}
	handler := NewProvidersHandler(
		slog.Default(),
		providers.NewService(slog.Default(), queries, ""),
		models.NewService(slog.Default(), queries),
	)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/providers/"+providerID+"/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/providers/:id/models")
	c.SetParamNames("id")
	c.SetParamValues(providerID)

	if err := handler.ListModelsByProvider(c); err != nil {
		t.Fatalf("list provider models: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got []models.GetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].Reasoning == nil {
		t.Fatalf("provider model list omitted reasoning projection: %+v", got)
	}
	if !got[0].Reasoning.Supported || !got[0].Reasoning.CanDisable {
		t.Fatalf("unexpected reasoning projection: %+v", got[0].Reasoning)
	}
}

// The wire contract clients read. Field names are snake_case like the rest of the
// API, and a model that cannot think still sends the object so a client can tell
// "no thinking" from "the server did not say".
func TestReasoningOptionsSerializeAsSnakeCase(t *testing.T) {
	t.Parallel()

	list := newReasoningTestHandler().withReasoning(context.Background(), []models.GetResponse{
		chatModel(models.ModelConfig{
			ThinkingMode:     models.ThinkingModeToggle,
			ReasoningEfforts: []string{models.ReasoningEffortLow, models.ReasoningEffortHigh},
		}),
	})

	encoded, err := json.Marshal(list[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"reasoning"`, `"supported"`, `"can_disable"`, `"efforts"`, `"default_effort"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("payload missing %s: %s", want, encoded)
		}
	}
}

// The registry guard covers YAML templates, which is where the dialect is written
// — but not where it is read from at call time. A model reaches the wire through
// the import path, and a dialect lost anywhere along it means a Gemini 2.5 row
// resolved as 3.x: thinkingLevel on a wire that wants thinkingBudget, i.e. a 400
// on every request. That gap is exactly how the field came to be missing from
// RemoteModel in the first place.
func TestImportedModelConfigCarriesTheWireDeclarations(t *testing.T) {
	t.Parallel()

	budgetMin, budgetMax := 128, 32768
	remote := providers.RemoteModel{
		ID:                  "gemini-2.5-pro",
		Type:                string(models.ModelTypeChat),
		ReasoningEfforts:    []string{models.ReasoningEffortLow, models.ReasoningEffortHigh},
		ThinkingMode:        models.ThinkingModeToggle,
		ReasoningDialect:    reasoning.DialectBudget,
		ReasoningOffSupport: reasoning.OffSupportRejected,
		ThinkingBudgetMin:   &budgetMin,
		ThinkingBudgetMax:   &budgetMax,
	}

	// The real assembly ImportModels uses. Mirroring it here instead would pass
	// even if the production path dropped a field, which is the mistake this test
	// exists to catch.
	cfg := modelConfigFromRemote(remote, []string{models.CompatReasoning})

	if cfg.ReasoningDialect != reasoning.DialectBudget {
		t.Errorf("dialect = %q, want %q", cfg.ReasoningDialect, reasoning.DialectBudget)
	}
	if cfg.ReasoningOffSupport != reasoning.OffSupportRejected {
		t.Errorf("off support = %q, want %q", cfg.ReasoningOffSupport, reasoning.OffSupportRejected)
	}
	if cfg.ThinkingBudgetMin == nil || *cfg.ThinkingBudgetMin != budgetMin {
		t.Errorf("budget min = %v, want %d", cfg.ThinkingBudgetMin, budgetMin)
	}
	if cfg.ThinkingBudgetMax == nil || *cfg.ThinkingBudgetMax != budgetMax {
		t.Errorf("budget max = %v, want %d", cfg.ThinkingBudgetMax, budgetMax)
	}

	// A declared rejection must survive into what a client is offered, or the
	// picker would show an off switch the wire answers with a 400.
	m := models.Model{Type: models.ModelTypeChat, Config: cfg}
	if opts := m.ReasoningOptions("google-generative-ai"); opts.CanDisable {
		t.Error("a model declaring it rejects an explicit off must not offer one")
	}
}
