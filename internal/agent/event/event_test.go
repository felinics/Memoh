package event

import (
	"encoding/json"
	"testing"
)

func TestStepEndEventWireShape(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(StreamEvent{
		Type:         StepEnd,
		StepIndex:    2,
		FinishReason: "tool-calls",
		Timing:       &StepTiming{StartedAtMS: 1000, FirstTokenAtMS: 1200, EndedAtMS: 3000},
		Usage:        json.RawMessage(`{"inputTokens":10}`),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["type"] != "step_end" || decoded["stepIndex"] != float64(2) || decoded["finishReason"] != "tool-calls" {
		t.Fatalf("unexpected envelope: %s", data)
	}
	timing, _ := decoded["timing"].(map[string]any)
	if timing["startedAtMs"] != float64(1000) || timing["firstTokenAtMs"] != float64(1200) || timing["endedAtMs"] != float64(3000) {
		t.Fatalf("unexpected timing: %s", data)
	}
	usage, _ := decoded["usage"].(map[string]any)
	if usage["inputTokens"] != float64(10) {
		t.Fatalf("unexpected usage: %s", data)
	}
}

func TestStepStartOmitsEmptyTiming(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(StreamEvent{Type: StepStart})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"type":"step_start"}` {
		t.Fatalf("unexpected wire shape: %s", data)
	}
}
