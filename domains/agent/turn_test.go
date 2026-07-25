package agent

import (
	"encoding/json"
	"testing"
)

func TestEventPayloadRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"type":"text_delta","text":"hi"}`)
	e := Event{RunID: "r1", TeamID: "t1", Seq: 1, Kind: "text_delta", Payload: raw}
	if string(e.Payload) != string(raw) {
		t.Fatalf("payload mutated: %s", e.Payload)
	}
	if e.RunID != "r1" || e.TeamID != "t1" || e.Seq != 1 || e.Kind != "text_delta" {
		t.Fatalf("event fields mutated: %+v", e)
	}
}
