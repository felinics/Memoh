package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

func TestMatrixStreamReusesOneMessagePerToolBatch(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []map[string]any
	)
	adapter := NewMatrixAdapter(nil)
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		content := map[string]any{}
		if body, err := io.ReadAll(req.Body); err == nil {
			_ = json.Unmarshal(body, &content)
		}
		mu.Lock()
		events = append(events, content)
		reply := fmt.Sprintf(`{"event_id":"$evt%d"}`, len(events))
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply)), Header: make(http.Header)}, nil
	})}
	sent := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), events...)
	}
	stream := &matrixOutboundStream{
		adapter:              adapter,
		cfg:                  Config{HomeserverURL: "https://matrix.example.com", AccessToken: "tok"},
		target:               "!room:example.com",
		reuseToolCallMessage: true,
		// Hold intermediate edits back so the events are deterministic.
		toolStatusInterval: time.Hour,
	}
	push := func(event channel.StreamEvent) {
		t.Helper()
		if err := stream.Push(context.Background(), mustPreparedMatrixEvent(t, event)); err != nil {
			t.Fatalf("push %s: %v", event.Type, err)
		}
	}
	read := func(id, path string, result map[string]any) *channel.StreamToolCall {
		tc := &channel.StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}}
		if result != nil {
			tc.Result = result
		}
		return tc
	}

	push(channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: read("c1", "/data/a.md", nil)})
	deadline := time.Now().Add(3 * time.Second)
	for len(sent()) < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the status message")
		}
		time.Sleep(2 * time.Millisecond)
	}
	push(channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: read("c2", "/data/b.md", nil)})
	push(channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: read("c1", "/data/a.md", map[string]any{"ok": true})})
	push(channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: read("c2", "/data/b.md", map[string]any{"ok": true})})
	push(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: "Done"}}})
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := sent()
	if len(got) != 3 {
		t.Fatalf("expected the status message, one replacement and the answer, got %+v", got)
	}
	if body, _ := got[0]["body"].(string); !strings.Contains(body, "📖 read · running") || got[0]["m.relates_to"] != nil {
		t.Fatalf("the first call must send the status message: %+v", got[0])
	}
	relation, _ := got[1]["m.relates_to"].(map[string]any)
	newContent, _ := got[1]["m.new_content"].(map[string]any)
	newBody, _ := newContent["body"].(string)
	if relation["rel_type"] != "m.replace" || relation["event_id"] != "$evt1" || strings.Count(newBody, "read · completed") != 2 {
		t.Fatalf("the batch must end by replacing the status message: %+v", got[1])
	}
	if body, _ := got[2]["body"].(string); body != "Done" {
		t.Fatalf("the answer must be sent below the status message: %+v", got[2])
	}
}
