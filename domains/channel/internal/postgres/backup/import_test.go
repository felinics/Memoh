package backup

import (
	"encoding/json"
	"testing"
)

func TestNewRequiresPool(t *testing.T) {
	if _, err := New(nil, exclusiveBotLock{}); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}

func TestDeterministicEventIDIsStable(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	first := deterministicID(botID, "event", "old-event")
	if second := deterministicID(botID, "event", "old-event"); second != first {
		t.Fatalf("deterministic id changed: %s != %s", first, second)
	}
	if other := deterministicID(botID, "event", "other-event"); other == first {
		t.Fatal("different source events received the same id")
	}
}

func TestSanitizeRestoredEventData(t *testing.T) {
	stripped := sanitizeRestoredEventData([]byte(`{"event_cursor":424242,"message_id":"m1","received_at_ms":1000}`))
	var payload map[string]any
	if err := json.Unmarshal(stripped, &payload); err != nil {
		t.Fatalf("decode sanitized payload: %v", err)
	}
	if _, ok := payload["event_cursor"]; ok {
		t.Fatal("instance-local cursor must be stripped from restored payloads")
	}
	if payload["message_id"] != "m1" || payload["received_at_ms"] != float64(1000) {
		t.Fatalf("other fields must survive, got %v", payload)
	}
}

func TestSanitizeRestoredEventDataPassthrough(t *testing.T) {
	plain := []byte(`{"message_id":"m1"}`)
	if got := string(sanitizeRestoredEventData(plain)); got != string(plain) {
		t.Fatalf("payload without cursor must pass through, got %s", got)
	}
	malformed := []byte(`not json`)
	if got := string(sanitizeRestoredEventData(malformed)); got != string(malformed) {
		t.Fatalf("malformed payload must pass through, got %s", got)
	}
}
