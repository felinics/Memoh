package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/felinics/memoh/internal/channel"
)

type discordRecordedRequest struct {
	method  string
	path    string
	content string
}

// discordAPIRecorder fakes the Discord REST API. Created messages get
// sequential IDs; edits answer with the edited message.
type discordAPIRecorder struct {
	mu       sync.Mutex
	requests []discordRecordedRequest
	created  int
}

func (r *discordAPIRecorder) session(t *testing.T) *discordgo.Session {
	t.Helper()
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	session.Client = &http.Client{Transport: roundTripFunc(r.roundTrip)}
	return session
}

func (r *discordAPIRecorder) roundTrip(req *http.Request) (*http.Response, error) {
	var payload struct {
		Content string `json:"content"`
	}
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(body, &payload)
	}
	r.mu.Lock()
	id := path.Base(req.URL.Path)
	if req.Method == http.MethodPost {
		r.created++
		id = fmt.Sprintf("msg-%d", r.created)
	}
	r.requests = append(r.requests, discordRecordedRequest{method: req.Method, path: req.URL.Path, content: payload.Content})
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":%q,"channel_id":"ch-1"}`, id))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func (r *discordAPIRecorder) snapshot() []discordRecordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]discordRecordedRequest(nil), r.requests...)
}

func (r *discordAPIRecorder) waitForRequests(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(r.snapshot()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d requests, got %+v", n, r.snapshot())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newReusingDiscordStream(t *testing.T, recorder *discordAPIRecorder) *discordOutboundStream {
	t.Helper()
	return &discordOutboundStream{
		adapter:              &DiscordAdapter{},
		target:               "ch-1",
		session:              recorder.session(t),
		reuseToolCallMessage: true,
		// Hold intermediate edits back so the requests are deterministic.
		toolStatusInterval: time.Hour,
	}
}

func pushDiscordStreamEvent(t *testing.T, stream *discordOutboundStream, event channel.StreamEvent) {
	t.Helper()
	prepared, err := channel.PrepareStreamEvent(context.Background(), nil, channel.ChannelConfig{BotID: "bot-test", ChannelType: Type}, event)
	if err != nil {
		t.Fatalf("prepare %s: %v", event.Type, err)
	}
	if err := stream.Push(context.Background(), prepared); err != nil {
		t.Fatalf("push %s: %v", event.Type, err)
	}
}

func discordReadCall(id, filePath string, result map[string]any) *channel.StreamToolCall {
	tc := &channel.StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": filePath}}
	if result != nil {
		tc.Result = result
	}
	return tc
}

func TestDiscordStreamReusesOneMessagePerToolBatch(t *testing.T) {
	t.Parallel()

	recorder := &discordAPIRecorder{}
	stream := newReusingDiscordStream(t, recorder)

	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: discordReadCall("c1", "/data/a.md", nil)})
	recorder.waitForRequests(t, 1)
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: discordReadCall("c2", "/data/b.md", nil)})
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: discordReadCall("c1", "/data/a.md", map[string]any{"ok": true})})
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: discordReadCall("c2", "/data/b.md", map[string]any{"error": "permission denied"})})
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: "Done"}}})
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	requests := recorder.snapshot()
	if len(requests) != 3 {
		t.Fatalf("expected one status message, its final edit and the answer, got %+v", requests)
	}
	if requests[0].method != http.MethodPost || !strings.Contains(requests[0].content, "📖 read · running") {
		t.Fatalf("the first call must create the status message: %+v", requests[0])
	}
	if requests[1].method != http.MethodPatch || !strings.HasSuffix(requests[1].path, "/messages/msg-1") {
		t.Fatalf("the batch must end by editing the status message: %+v", requests[1])
	}
	for _, want := range []string{"📖 read · completed · /data/a.md", "📖 read · failed", "error: permission denied"} {
		if !strings.Contains(requests[1].content, want) {
			t.Fatalf("final status %q is missing %q", requests[1].content, want)
		}
	}
	if requests[2].method != http.MethodPost || requests[2].content != "Done" {
		t.Fatalf("the answer must be posted below the status message: %+v", requests[2])
	}
}

func TestDiscordStreamRevisesCallsThatOutliveTheirBatch(t *testing.T) {
	t.Parallel()

	recorder := &discordAPIRecorder{}
	stream := newReusingDiscordStream(t, recorder)

	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: discordReadCall("c1", "/data/a.md", nil)})
	recorder.waitForRequests(t, 1)
	// A parallel call asks for approval while c1 is still running.
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: &channel.StreamToolCall{
		Name:       "exec",
		CallID:     "c2",
		ApprovalID: "approval-1",
		ShortID:    3,
		Input:      map[string]any{"command": "rm -rf /tmp/cache"},
	}})
	pushDiscordStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: discordReadCall("c1", "/data/a.md", map[string]any{"ok": true})})
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	requests := recorder.snapshot()
	if len(requests) != 3 {
		t.Fatalf("expected the status message, the approval prompt and one revision, got %+v", requests)
	}
	if requests[1].method != http.MethodPost || !strings.Contains(requests[1].content, "approval_required") {
		t.Fatalf("the approval prompt must keep its own message: %+v", requests[1])
	}
	if requests[2].method != http.MethodPatch || !strings.HasSuffix(requests[2].path, "/messages/msg-1") ||
		!strings.Contains(requests[2].content, "📖 read · completed · /data/a.md") {
		t.Fatalf("c1 must finish in the status message it started in: %+v", requests[2])
	}
}
