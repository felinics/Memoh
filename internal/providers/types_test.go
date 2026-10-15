package providers

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/felinics/memoh/internal/models"
)

// import-models reads each catalog model's config as POST /models reads one: a
// version 1 shape sent by an older client upgrades instead of losing its
// capabilities, and the result is normalized so a refresh compares it with
// the stored row like for like.
func TestCatalogModelReadsConfigInAnySchemaVersion(t *testing.T) {
	t.Parallel()

	var req ImportModelsRequest
	raw := `{"models":[
		{"model_id":"old","config":{"compatibilities":["tool-call","reasoning"],"thinking_mode":"toggle","reasoning_efforts":["low","high"]}},
		{"model_id":"new","config":{"schema_version":2,"tool_call":true,"reasoning":{"efforts":["","low","low"]}}},
		{"model_id":"bare"}
	]}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(req.Models))
	}
	old := req.Models[0].Config
	if old.SchemaVersion != models.ModelConfigSchemaVersion || !old.ToolCall || old.Reasoning == nil || !slices.Equal(old.Reasoning.Efforts, []string{"low", "high"}) {
		t.Fatalf("version 1 config read as %+v, want its capabilities upgraded", old)
	}
	current := req.Models[1].Config
	if current.Reasoning == nil || !slices.Equal(current.Reasoning.Efforts, []string{"low"}) {
		t.Fatalf("version 2 config read as %+v, want its efforts normalized", current)
	}
	if req.Models[2].Config.ToolCall || req.Models[2].Config.Reasoning != nil {
		t.Fatalf("missing config read as %+v, want empty", req.Models[2].Config)
	}

	if err := json.Unmarshal([]byte(`{"models":[{"model_id":"bad","config":{"schema_version":2,"tool_call":"yes"}}]}`), &req); err == nil {
		t.Fatal("a malformed config must fail to decode, as it does for POST /models")
	}
}
