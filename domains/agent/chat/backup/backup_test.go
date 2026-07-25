package backup

import (
	"encoding/json"
	"testing"
)

func TestHistoryDTOJSONKeepsLegacyFieldNamesAndNulls(t *testing.T) {
	session := Session{ID: "session", BotID: "bot"}
	message := Message{
		ID:       "message",
		BotID:    "bot",
		Role:     "user",
		Content:  json.RawMessage(`{"text":"hello"}`),
		Metadata: json.RawMessage(`{}`),
	}
	asset := Asset{RelID: "asset", MessageID: "message"}

	for name, value := range map[string]any{
		"session": session,
		"message": message,
		"asset":   asset,
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal %s: %v", name, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("unmarshal %s: %v", name, err)
			}
			if _, ok := fields["id"]; !ok && name != "asset" {
				t.Fatalf("%s omitted id: %s", name, raw)
			}
		})
	}

	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	for _, key := range []string{
		"session_id", "event_id", "turn_id", "turn_position",
		"turn_message_seq", "turn_superseded_by_turn_id",
		"turn_superseded_at", "turn_superseded_reason",
		"sender_avatar_url",
	} {
		if string(fields[key]) != "null" {
			t.Fatalf("%s = %s, want null", key, fields[key])
		}
	}
}
