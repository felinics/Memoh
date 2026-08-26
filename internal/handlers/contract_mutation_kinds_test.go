package handlers

import (
	"encoding/json"
	"os"
	"testing"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

// TestGeneratedContractsCarryEveryMutationKind pins the generated OpenAPI enum
// to the Go mutation vocabulary in both directions, so adding or removing a
// MutationKind without regenerating the contracts fails here instead of
// shipping a stale SDK union.
func TestGeneratedContractsCarryEveryMutationKind(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../spec/swagger.json")
	if err != nil {
		t.Fatalf("read generated swagger document: %v", err)
	}
	var doc struct {
		Definitions map[string]struct {
			Enum []string `json:"enum"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode swagger document: %v", err)
	}
	definition, ok := doc.Definitions["contextfrag.MutationKind"]
	if !ok || len(definition.Enum) == 0 {
		t.Fatal("generated swagger document is missing the contextfrag.MutationKind enum")
	}
	generated := make(map[string]bool, len(definition.Enum))
	for _, value := range definition.Enum {
		generated[value] = true
	}
	declared := make(map[string]bool)
	for _, kind := range contextfrag.AllMutationKinds() {
		declared[string(kind)] = true
		if !generated[string(kind)] {
			t.Errorf("generated enum is missing %q; run mise run swagger-generate and generate-sdk", kind)
		}
	}
	for value := range generated {
		if !declared[value] {
			t.Errorf("generated enum carries %q which the Go vocabulary no longer declares", value)
		}
	}
}
