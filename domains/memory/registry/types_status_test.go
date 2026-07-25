package registry

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
