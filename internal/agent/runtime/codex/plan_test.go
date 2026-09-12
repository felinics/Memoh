package codex

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/codex/protocol"
	"github.com/felinics/memoh/internal/agent/runtime/external"
)

func TestPlanModeCarriesEffectiveSettingsIndependentlyOfPermissions(t *testing.T) {
	for _, mode := range []string{"plan", "default"} {
		input := external.PromptInput{RuntimeMetadata: map[string]any{"collaboration_mode": mode, "permission_mode": "yolo"}}
		model, effort := "selected-model", protocol.ReasoningEffort("high")
		params := protocol.TurnStartParams{Model: &model, Effort: &effort}
		if err := applyCollaborationMode(&params, input, protocol.Settings{Model: "thread-model"}); err != nil {
			t.Fatal(err)
		}
		got := params.CollaborationMode
		if string(got.Mode) != mode || got.Settings.Model != model || *got.Settings.ReasoningEffort != effort || got.Settings.DeveloperInstructions != nil {
			t.Fatalf("effective settings: %+v", got)
		}
	}
}

func TestCompletedPlanBecomesAssistantTranscript(t *testing.T) {
	turn := newTurnState(t.Context(), external.PromptInput{Sink: &captureSink{}}, "thread", nil, nil, nil, nil, slog.Default())
	defer turn.close()
	turn.setTurnID("current")
	var item protocol.ThreadItem
	if err := json.Unmarshal([]byte(`{"type":"plan","id":"plan-1","text":"1. Inspect\n2. Implement"}`), &item); err != nil {
		t.Fatal(err)
	}
	turn.handleNotification(&protocol.ItemCompletedNotification{ThreadID: "thread", TurnID: "old", Item: item})
	turn.handleNotification(&protocol.ItemCompletedNotification{ThreadID: "thread", TurnID: "current", Item: item})
	turn.handleNotification(&protocol.TurnCompletedNotification{ThreadID: "thread", Turn: protocol.Turn{ID: "current", Status: protocol.TurnStatusCompleted}})
	result, err := turn.result("")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "1. Inspect\n2. Implement" || len(result.Output) == 0 {
		t.Fatalf("plan missing or stale plan duplicated: %+v", result)
	}
}
