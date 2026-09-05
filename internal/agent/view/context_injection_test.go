package view

import (
	"encoding/json"
	"testing"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestConvertMessagesToUITurnsProjectsContextInjection(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 3, 1, 2, 5, 0, time.UTC)
	turns := convertTestMessagesToUITurns([]messagepkg.Message{{
		ID:        "user-1",
		Role:      "user",
		TurnID:    "turn-1",
		Content:   json.RawMessage(`{"role":"user","content":"hello"}`),
		CreatedAt: createdAt,
	}, {
		ID:        "assistant-1",
		Role:      "assistant",
		TurnID:    "turn-1",
		Content:   json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"working"}]}`),
		CreatedAt: createdAt.Add(time.Second),
	}, {
		ID:          "user-2",
		Role:        "user",
		TurnID:      "turn-1",
		Content:     json.RawMessage(`{"role":"user","content":"<message>stop</message>"}`),
		RawMetadata: json.RawMessage(`{"context_injection":{"kind":"steering"}}`),
		CreatedAt:   createdAt.Add(2 * time.Second),
	}})
	if len(turns) != 3 {
		t.Fatalf("turns = %#v", turns)
	}
	if turns[0].ContextInjection != nil {
		t.Fatalf("request user turn marked: %#v", turns[0].ContextInjection)
	}
	if turns[2].Role != "user" || turns[2].ContextInjection == nil || turns[2].ContextInjection.Kind != "steering" {
		t.Fatalf("injected turn = %#v", turns[2])
	}
}
