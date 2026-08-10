package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/models"
)

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
