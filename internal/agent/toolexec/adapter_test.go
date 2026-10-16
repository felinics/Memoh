package toolexec

import (
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestEncodeOutputRejectsInvalidRawJSON(t *testing.T) {
	out, err := EncodeOutput(json.RawMessage("nope"))
	if err != nil {
		t.Fatalf("EncodeOutput: %v", err)
	}
	if out.Text != "nope" || out.JSON != nil {
		t.Fatalf("output = %+v, want text fallback", out)
	}
	if _, err := json.Marshal(sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "c", ToolName: "t", Result: out})); err != nil {
		t.Fatalf("tool message must stay marshalable: %v", err)
	}
}

// A schema that arrives as data and does not parse is an error for the
// caller to act on; SchemaFromValue keeps degrading for Memoh-written shapes.
func TestResolveSchemaReportsUnparsableDocuments(t *testing.T) {
	bad := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "number", "exclusiveMinimum": true}}}
	if _, err := ResolveSchema(bad); err == nil {
		t.Fatal("ResolveSchema accepted a draft-04 boolean exclusiveMinimum")
	}
	if got := SchemaFromValue(bad); got == nil || got.Type != "object" {
		t.Fatalf("SchemaFromValue fallback = %#v", got)
	}
	good, err := ResolveSchema(map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}})
	if err != nil || good.Properties["a"] == nil {
		t.Fatalf("ResolveSchema(good) = %#v, %v", good, err)
	}
	empty, err := ResolveSchema(nil)
	if err != nil || empty.Type != "object" {
		t.Fatalf("ResolveSchema(nil) = %#v, %v", empty, err)
	}
}
