package adapters

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryStatusOmitsAbsentOptionalHealthChecks(t *testing.T) {
	raw, err := json.Marshal(MemoryStatusResponse{
		ProviderType: "builtin",
		MemoryMode:   "graph",
		SourceCount:  3,
	})
	if err != nil {
		t.Fatalf("marshal memory status: %v", err)
	}
	payload := string(raw)
	if strings.Contains(payload, "pgvector") {
		t.Fatalf("graph-only status should omit pgvector health, got %s", payload)
	}
	if strings.Contains(payload, "encoder") {
		t.Fatalf("graph-only status should omit encoder health, got %s", payload)
	}
}

func TestMemoryStatusIncludesConfiguredPgvectorHealth(t *testing.T) {
	raw, err := json.Marshal(MemoryStatusResponse{
		ProviderType: "builtin",
		MemoryMode:   "graph",
		VectorIndex:  "pgvector",
		Pgvector:     &HealthStatus{OK: true},
	})
	if err != nil {
		t.Fatalf("marshal memory status: %v", err)
	}
	payload := string(raw)
	if !strings.Contains(payload, `"pgvector":{"ok":true}`) {
		t.Fatalf("configured pgvector health should be present, got %s", payload)
	}
}

func TestMemoryItemJSONOmitsInternalSourceMessageIDs(t *testing.T) {
	raw, err := json.Marshal(MemoryItem{
		ID:               "memory-1",
		Memory:           "shared fact",
		SourceMessageIDs: []string{"session-private/message-private"},
	})
	if err != nil {
		t.Fatalf("marshal memory item: %v", err)
	}
	if strings.Contains(string(raw), "source_message_ids") || strings.Contains(string(raw), "session-private") {
		t.Fatalf("public memory JSON leaked internal provenance: %s", raw)
	}
}
