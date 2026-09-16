package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/felinics/memoh/internal/channel"
)

type slackRecordedCall struct {
	method string
	ts     string
	text   string
}

// slackAPIRecorder fakes the Slack Web API. Posted messages get sequential
// timestamps; updates and deletes answer with the timestamp they target.
type slackAPIRecorder struct {
	mu     sync.Mutex
	calls  []slackRecordedCall
	lastTS int
}

func (r *slackAPIRecorder) client() *slackapi.Client {
	return slackapi.New("xoxb-test",
		slackapi.OptionAPIURL("https://slack.test/api/"),
		slackapi.OptionHTTPClient(&http.Client{Transport: roundTripFunc(r.roundTrip)}),
	)
}

func (r *slackAPIRecorder) roundTrip(req *http.Request) (*http.Response, error) {
	_ = req.ParseForm()
	method := strings.TrimPrefix(req.URL.Path, "/api/")
	r.mu.Lock()
	ts := req.PostForm.Get("ts")
	if method == "chat.postMessage" {
		r.lastTS++
		ts = fmt.Sprintf("1700000000.%06d", r.lastTS)
	}
	r.calls = append(r.calls, slackRecordedCall{method: method, ts: ts, text: req.PostForm.Get("text")})
	r.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"ok": true, "channel": "C123", "ts": ts})
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

func (r *slackAPIRecorder) snapshot() []slackRecordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]slackRecordedCall(nil), r.calls...)
}

func (r *slackAPIRecorder) waitForCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(r.snapshot()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d calls, got %+v", n, r.snapshot())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func pushSlackStreamEvent(t *testing.T, stream *slackOutboundStream, event channel.StreamEvent) {
	t.Helper()
	prepared, err := channel.PrepareStreamEvent(context.Background(), nil, channel.ChannelConfig{BotID: "bot-test", ChannelType: Type}, event)
	if err != nil {
		t.Fatalf("prepare %s: %v", event.Type, err)
	}
	if err := stream.Push(context.Background(), prepared); err != nil {
		t.Fatalf("push %s: %v", event.Type, err)
	}
}

func slackReadCall(id, path string, result map[string]any) *channel.StreamToolCall {
	tc := &channel.StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}}
	if result != nil {
		tc.Result = result
	}
	return tc
}

func TestSlackStreamReusesOneMessagePerToolBatch(t *testing.T) {
	t.Parallel()

	recorder := &slackAPIRecorder{}
	stream := &slackOutboundStream{
		adapter:              NewSlackAdapter(nil),
		target:               "C123",
		api:                  recorder.client(),
		reuseToolCallMessage: true,
		// Hold intermediate edits back so the calls are deterministic.
		toolStatusInterval: time.Hour,
	}

	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted})
	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: slackReadCall("c1", "/data/a.md", nil)})
	recorder.waitForCalls(t, 3)
	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: slackReadCall("c2", "/data/b.md", nil)})
	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: slackReadCall("c1", "/data/a.md", map[string]any{"ok": true})})
	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: slackReadCall("c2", "/data/b.md", map[string]any{"error": "permission denied"})})
	pushSlackStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: "Done"}}})
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := recorder.snapshot()
	if len(calls) != 5 {
		t.Fatalf("expected placeholder, its removal, status message, status edit and answer, got %+v", calls)
	}
	if calls[0].method != "chat.postMessage" || calls[1].method != "chat.delete" || calls[1].ts != calls[0].ts {
		t.Fatalf("the unused placeholder must be removed before tool calls: %+v", calls)
	}
	if calls[2].method != "chat.postMessage" || !strings.Contains(calls[2].text, "📖 read · running") {
		t.Fatalf("the first call must create the status message: %+v", calls[2])
	}
	if calls[3].method != "chat.update" || calls[3].ts != calls[2].ts {
		t.Fatalf("the batch must end by updating the status message: %+v", calls)
	}
	for _, want := range []string{"📖 read · completed · /data/a.md", "📖 read · failed", "error: permission denied"} {
		if !strings.Contains(calls[3].text, want) {
			t.Fatalf("final status %q is missing %q", calls[3].text, want)
		}
	}
	if calls[4].method != "chat.postMessage" || calls[4].text != "Done" {
		t.Fatalf("the answer must be posted below the status message: %+v", calls[4])
	}
}
