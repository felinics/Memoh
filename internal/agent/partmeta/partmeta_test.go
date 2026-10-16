package partmeta

import (
	"reflect"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

func TestSetAndObjectRoundTrip(t *testing.T) {
	meta := Set(nil, KeyApproval, map[string]any{"approval_id": "a1", "short_id": 3, "can_approve": true})
	meta = meta.Merge(sdk.NewProviderMetadata("anthropic", map[string]string{"signature": "sig"}))
	obj, ok := Object(meta, KeyApproval)
	if !ok || obj["approval_id"] != "a1" || obj["short_id"] != float64(3) || obj["can_approve"] != true {
		t.Fatalf("approval annotation: %#v ok=%v", obj, ok)
	}
	if !Has(meta, KeyApproval) || Has(meta, KeyUserInput) {
		t.Fatal("Has misreported the recorded keys")
	}
	if meta.Get("anthropic", "signature") != "sig" {
		t.Fatal("provider namespace was disturbed")
	}
}

func TestFoldUnfoldPreservesStoredShape(t *testing.T) {
	stored := map[string]any{
		"approval":           map[string]any{"approval_id": "a1", "short_id": float64(3)},
		"execution_location": map[string]any{"kind": "workspace", "name": "default"},
		"anthropic":          map[string]any{"signature": "sig"},
		"openai":             map[string]any{"item": "rs_1", "nested": map[string]any{"a": float64(1)}},
	}
	meta := Fold(stored)
	if _, ok := meta[Namespace]["approval"]; !ok {
		t.Fatalf("approval not claimed for %s: %#v", Namespace, meta)
	}
	if _, ok := meta["execution_location"]; ok {
		t.Fatal("an owned key with all-string values must still fold into the memoh namespace")
	}
	if meta.Get("anthropic", "signature") != "sig" || meta.Get("openai", "item") != "rs_1" {
		t.Fatalf("provider namespaces: %#v", meta)
	}
	if meta.Get("openai", "nested") != `{"a":1}` {
		t.Fatalf("non-string provider value must encode like sdk.StringValues: %q", meta.Get("openai", "nested"))
	}
	if obj, ok := Object(meta, KeyExecutionLocation); !ok || obj["kind"] != "workspace" {
		t.Fatalf("execution location: %#v", obj)
	}

	unfolded := Unfold(meta)
	want := map[string]any{
		"approval":           map[string]any{"approval_id": "a1", "short_id": float64(3)},
		"execution_location": map[string]any{"kind": "workspace", "name": "default"},
		"anthropic":          map[string]any{"signature": "sig"},
		"openai":             map[string]any{"item": "rs_1", "nested": `{"a":1}`},
	}
	if !reflect.DeepEqual(unfolded, want) {
		t.Fatalf("unfolded = %#v\nwant %#v", unfolded, want)
	}
}

func TestLookupReadsLegacyNamespaceShape(t *testing.T) {
	meta := sdk.ProviderMetadata{"execution_location": {"kind": "workspace", "name": "default"}}
	obj, ok := Object(meta, KeyExecutionLocation)
	if !ok || obj["name"] != "default" {
		t.Fatalf("legacy namespace shape unreadable: %#v ok=%v", obj, ok)
	}
	if Fold(nil) != nil || Unfold(nil) != nil {
		t.Fatal("empty metadata must stay nil")
	}
}
