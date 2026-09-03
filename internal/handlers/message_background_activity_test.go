package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestDependencyNotificationActivityRefreshesHistoryWithoutPublishingContent(t *testing.T) {
	msg := messagepkg.Message{
		SessionID: "session-1", Content: json.RawMessage(`{"text":"private message"}`),
		Metadata: map[string]any{"background_task_id": "task-1", "background_task_event": "completed"},
	}
	activity := messageSessionActivity(msg)
	if activity["reason"] != "background_task" || activity["session_id"] != msg.SessionID {
		t.Fatalf("missing conversation history refresh hint: %#v", activity)
	}
	raw, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private message") || strings.Contains(string(raw), "task-1") {
		t.Fatalf("activity exposed notification content: %s", raw)
	}
	msg.Metadata = nil
	if _, exists := messageSessionActivity(msg)["reason"]; exists {
		t.Fatal("ordinary turn activity requested background history refresh")
	}
}
