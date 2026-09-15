package handlers

import (
	"encoding/json"
	"testing"
)

func TestAppLogEventPreservesEmptyOutputLine(t *testing.T) {
	data, err := json.Marshal(AppStreamEvent{Type: "log", Stream: "stdout", Data: ""})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if value, ok := event["data"].(string); !ok || value != "" {
		t.Fatalf("empty log line must remain a string for SSE clients: %s", data)
	}
}
