package backup

import (
	"encoding/json"
	"testing"
)

func TestObservationDTOJSONKeepsLegacyFields(t *testing.T) {
	cursor := DiscussCursor{SessionID: "session", ScopeKey: "default", TeamID: "team"}
	event := SessionEvent{
		ID:        "event",
		BotID:     "bot",
		SessionID: "session",
		EventKind: "message",
		EventData: json.RawMessage(`{}`),
		TeamID:    "team",
	}

	for name, value := range map[string]any{"cursor": cursor, "event": event} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal %s: %v", name, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("unmarshal %s: %v", name, err)
			}
			if string(fields["team_id"]) != `"team"` {
				t.Fatalf("%s team_id = %s", name, fields["team_id"])
			}
		})
	}
}
