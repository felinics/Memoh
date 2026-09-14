package textutil

import (
	"encoding/json"
	"testing"
)

func TestStorageJSONPreservesEscapesAndNormalizesBinaryText(t *testing.T) {
	raw := []byte(`{"nested":["a\u0000b","\\u0000","\ud800","\ud83d\ude00"],"number":9007199254740993}`)
	clean, err := StorageJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Nested []string    `json:"nested"`
		Number json.Number `json:"number"`
	}
	if err := json.Unmarshal(clean, &decoded); err != nil {
		t.Fatal(err)
	}
	want := []string{`a\x00b`, `\u0000`, "�", "😀"}
	for i, v := range want {
		if decoded.Nested[i] != v {
			t.Fatalf("value %d = %q, want %q", i, decoded.Nested[i], v)
		}
	}
	if decoded.Number.String() != "9007199254740993" {
		t.Fatal("lost numeric precision")
	}
	again, err := StorageJSON(clean)
	if err != nil || string(again) != string(clean) {
		t.Fatalf("not idempotent: %s %v", again, err)
	}
}

func TestStorageJSONRejectsAmbiguousKeysAndInvalidDocuments(t *testing.T) {
	for _, raw := range []string{`{"a\u0000":1,"a\\x00":2}`, `{} {}`, `{"x":`} {
		if _, err := StorageJSON([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
