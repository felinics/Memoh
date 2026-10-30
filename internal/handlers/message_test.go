package handlers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	session "github.com/felinics/memoh/internal/chat/thread"
)

type testFlusher struct{}

func (*testFlusher) Flush() {}

func TestParseBeforeParam(t *testing.T) {
	t.Parallel()

	if _, ok := parseBeforeParam(""); ok {
		t.Fatalf("expected empty before value to be ignored")
	}
	parsed, ok := parseBeforeParam("1735689600000")
	if !ok {
		t.Fatalf("expected epoch millis before value to parse")
	}
	if parsed.UnixMilli() != 1735689600000 {
		t.Fatalf("expected parsed epoch millis 1735689600000, got %d", parsed.UnixMilli())
	}
}

func TestIsUserFacingSessionType(t *testing.T) {
	t.Parallel()

	for _, typ := range []string{session.TypeChat, session.TypeDiscuss, session.TypeACPAgent} {
		if !session.IsUserFacingType(typ) {
			t.Fatalf("expected %q to be user-facing", typ)
		}
	}
	for _, typ := range []string{session.TypeSchedule, session.TypeSubagent, ""} {
		if session.IsUserFacingType(typ) {
			t.Fatalf("expected %q to be filtered out of user-facing surfaces", typ)
		}
	}
}

func TestWriteSSEJSON(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	flusher := &testFlusher{}

	if err := writeSSEJSON(&output, flusher, map[string]any{"type": "ping"}); err != nil {
		t.Fatalf("writeSSEJSON failed: %v", err)
	}
	raw := output.String()
	if !strings.HasPrefix(raw, "data: ") {
		t.Fatalf("expected SSE data prefix, got %q", raw)
	}
	if !strings.HasSuffix(raw, "\n\n") {
		t.Fatalf("expected SSE payload suffix, got %q", raw)
	}
	payloadText := strings.TrimSuffix(strings.TrimPrefix(raw, "data: "), "\n\n")
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatalf("decode SSE payload failed: %v", err)
	}
	if payload["type"] != "ping" {
		t.Fatalf("expected payload type ping, got %#v", payload["type"])
	}
}
