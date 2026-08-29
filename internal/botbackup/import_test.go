package botbackup

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

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

func TestRestoredSessionEventParamsDropsInstanceLocalCursor(t *testing.T) {
	params := restoredSessionEventParams(pgtype.UUID{}, pgtype.UUID{}, sqlc.BotSessionEvent{
		EventKind:    "message",
		EventData:    []byte(`{"event_cursor":424242,"message_id":"m1"}`),
		ReceivedAtMs: 1700000000000,
	})

	var payload map[string]any
	if err := json.Unmarshal(params.EventData, &payload); err != nil {
		t.Fatalf("decode restored event data: %v", err)
	}
	if _, ok := payload["event_cursor"]; ok {
		t.Fatal("restored events must not carry the source deployment's cursor")
	}
	if payload["message_id"] != "m1" || params.ReceivedAtMs != 1700000000000 {
		t.Fatalf("restore must preserve source-time identity, got %v / %d", payload, params.ReceivedAtMs)
	}
}

func TestRestoredDiscussCursorParamsDropsEventWatermark(t *testing.T) {
	params := restoredDiscussCursorParams(pgtype.UUID{}, sqlc.BotSessionDiscussCursor{
		ScopeKey:            "route:r1",
		Source:              "telegram",
		ConsumedCursor:      1700000000000,
		ConsumedEventCursor: 999999,
	})

	if params.ConsumedEventCursor != 0 {
		t.Fatalf("restored event watermark = %d, want 0", params.ConsumedEventCursor)
	}
	if params.ConsumedCursor != 1700000000000 || params.ScopeKey != "route:r1" {
		t.Fatalf("source-time watermark and scope must survive, got %+v", params)
	}
}
