package payload

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/felinics/memoh/internal/agent/background"
)

// TestBackgroundTaskHasTopLevelSessionID pins the wire shape the SSE
// per-session handler routes on. Removing `session_id` from the helper now
// breaks this test loudly.
func TestBackgroundTaskHasTopLevelSessionID(t *testing.T) {
	t.Parallel()

	evt := background.TaskEvent{
		Event:     background.TaskEventStarted,
		TaskID:    "task-1",
		SessionID: "sess-1",
	}
	data, err := json.Marshal(BackgroundTask(evt))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertTopLevelKeys(t, decoded, []string{"event", "session_id", "task"})
	if decoded["session_id"] != "sess-1" {
		t.Fatalf("session_id = %v, want sess-1", decoded["session_id"])
	}
}

func assertTopLevelKeys(t *testing.T, payload map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(payload))
	for k := range payload {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("top-level keys = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("top-level keys = %v, want %v", got, want)
		}
	}
}
