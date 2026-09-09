package contextfrag

import (
	"encoding/json"
	"testing"
)

func TestContextFragJSONFieldNames(t *testing.T) {
	frag := ContextFrag{
		ID:                 "f1",
		TokenEstimate:      42,
		ConflictKey:        "group.a",
		RetentionTier:      RetentionPreferred,
		DropPriority:       12,
		RequiredCapability: "read",
		Render: RenderPolicy{
			GroupID:     "system.tools",
			GroupJoiner: "\n\n",
		},
	}
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
	if got, ok := decoded["retention_tier"].(string); !ok || got != string(RetentionPreferred) {
		t.Fatalf("retention_tier = %v, want %q", decoded["retention_tier"], RetentionPreferred)
	}
	if got, ok := decoded["drop_priority"].(float64); !ok || got != 12 {
		t.Fatalf("drop_priority = %v, want 12", decoded["drop_priority"])
	}
	if got, ok := decoded["required_capability"].(string); !ok || got != "read" {
		t.Fatalf("required_capability = %v, want read", decoded["required_capability"])
	}
	render, ok := decoded["render"].(map[string]any)
	if !ok || render["group_id"] != "system.tools" || render["group_joiner"] != "\n\n" {
		t.Fatalf("render = %#v, want grouped render policy", decoded["render"])
	}
	if _, ok := decoded["TokenEstimate"]; ok {
		t.Fatal("TokenEstimate leaked under Go field name")
	}
}
