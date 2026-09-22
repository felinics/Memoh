package event

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNoticeMetadataRoundTripUsesOnlyPublicProjection(t *testing.T) {
	ev := StreamEvent{
		Type: RuntimeNotice, Code: " tools_unavailable ", Delta: " Public notice ",
		Metadata: map[string]any{"dep_id": " codex ", "empty": " ", "attempts": 3, "diagnostics": map[string]any{"secret": "SECRET"}},
		Input:    "SECRET", Result: "SECRET", Error: "SECRET",
	}
	n, ok := NoticeFromStream(ev)
	want := Notice{Code: "tools_unavailable", Content: "Public notice", Args: map[string]string{"dep_id": "codex"}}
	if !ok || !reflect.DeepEqual(n, want) {
		t.Fatalf("notice=%#v", n)
	}
	metadata := map[string]any{RuntimeNoticesMetadataKey: []Notice{n, n}}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := NoticesFromMetadata(decoded); !reflect.DeepEqual(got, []Notice{want}) {
		t.Fatalf("reloaded notice=%#v", got)
	}
	for _, raw := range []any{nil, "bad", []string{"bad"}, []map[string]any{{"content": " "}}} {
		if got := NoticesFromMetadata(map[string]any{RuntimeNoticesMetadataKey: raw}); len(got) != 0 {
			t.Fatalf("malformed legacy metadata produced notices: %#v", got)
		}
	}
}
