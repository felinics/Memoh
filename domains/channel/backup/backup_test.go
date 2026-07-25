package backup

import (
	"encoding/json"
	"reflect"
	"strings"
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

// TestDiscussCursorCarriesNoEventCursor preserves the invariant that upstream
// #716 asserted through restoredDiscussCursorParams: a restored discuss cursor
// must never carry the source deployment's event watermark. This layout keeps
// that structurally true, so the guard is that the field stays absent.
func TestDiscussCursorCarriesNoEventCursor(t *testing.T) {
	typ := reflect.TypeOf(DiscussCursor{})
	for i := range typ.NumField() {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "eventcursor") {
			t.Fatalf("DiscussCursor.%s reintroduces the source deployment's event watermark; "+
				"restored cursors must gate in the source-time domain only", typ.Field(i).Name)
		}
	}
}
