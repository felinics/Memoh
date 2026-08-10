package contextfrag

import (
	"encoding/json"
	"testing"
)

func TestContextFragJSONFieldNames(t *testing.T) {
	frag := ContextFrag{ID: "f1", TokenEstimate: 42, ConflictKey: "group.a"}
	data, err := json.Marshal(frag)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, ok := decoded["token_estimate"].(float64); !ok || got != 42 {
		t.Fatalf("token_estimate = %v, want 42", decoded["token_estimate"])
	}
	if got, ok := decoded["conflict_key"].(string); !ok || got != "group.a" {
		t.Fatalf("conflict_key = %v, want %q", decoded["conflict_key"], "group.a")
	}
	if _, ok := decoded["TokenEstimate"]; ok {
		t.Fatal("TokenEstimate leaked under Go field name")
	}
}
