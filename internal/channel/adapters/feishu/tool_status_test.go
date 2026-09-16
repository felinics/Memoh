package feishu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/felinics/memoh/internal/channel"
)

type feishuTransport func(*http.Request) (*http.Response, error)

func (f feishuTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type feishuRecordedCall struct {
	method string
	path   string
	body   string
}

func TestFeishuStreamReusesOneCardPerToolBatch(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		calls   []feishuRecordedCall
		created int
	)
	respond := func(body string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	client := lark.NewClient("cli_test", "secret_test",
		lark.WithOpenBaseUrl("https://feishu.test"),
		lark.WithHttpClient(&http.Client{Transport: feishuTransport(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/tenant_access_token/internal") {
				return respond(`{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`), nil
			}
			body := ""
			if req.Body != nil {
				data, _ := io.ReadAll(req.Body)
				body = string(data)
			}
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, feishuRecordedCall{method: req.Method, path: req.URL.Path, body: body})
			if req.Method == http.MethodPost {
				created++
				return respond(fmt.Sprintf(`{"code":0,"msg":"success","data":{"message_id":"om_%d"}}`, created)), nil
			}
			return respond(`{"code":0,"msg":"success","data":{}}`), nil
		})}),
	)
	recorded := func() []feishuRecordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]feishuRecordedCall(nil), calls...)
	}
	stream := &feishuOutboundStream{
		adapter:              &FeishuAdapter{},
		target:               "chat_id:oc_test",
		client:               client,
		receiveID:            "oc_test",
		receiveType:          "chat_id",
		patchInterval:        feishuStreamPatchInterval,
		reuseToolCallMessage: true,
		// Hold intermediate patches back so the calls are deterministic.
		toolStatusInterval: time.Hour,
	}
	ctx := context.Background()
	push := func(event channel.StreamEvent) {
		t.Helper()
		prepared, err := channel.PrepareStreamEvent(ctx, nil, channel.ChannelConfig{BotID: "bot-test", ChannelType: Type}, event)
		if err != nil {
			t.Fatalf("prepare %s: %v", event.Type, err)
		}
		if err := stream.Push(ctx, prepared); err != nil {
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
	for len(recorded()) < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the status card")
		}
		time.Sleep(2 * time.Millisecond)
	}
	push(channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: read("c2", "/data/b.md", nil)})
	push(channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: read("c1", "/data/a.md", map[string]any{"ok": true})})
	push(channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: read("c2", "/data/b.md", map[string]any{"ok": true})})
	push(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: "Done"}}})
	if err := stream.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := recorded()
	if len(got) != 4 {
		t.Fatalf("expected the status card, its patch, and the answer card with its patch, got %+v", got)
	}
	if got[0].method != http.MethodPost || !strings.Contains(got[0].body, "📖 read · running") {
		t.Fatalf("the first call must create the status card: %+v", got[0])
	}
	if got[1].method != http.MethodPatch || !strings.HasSuffix(got[1].path, "/messages/om_1") || strings.Count(got[1].body, "read · completed") != 2 {
		t.Fatalf("the batch must end by patching the status card: %+v", got[1])
	}
	if got[2].method != http.MethodPost || got[3].method != http.MethodPatch || !strings.HasSuffix(got[3].path, "/messages/om_2") || !strings.Contains(got[3].body, "Done") {
		t.Fatalf("the answer must get its own card below the status card: %+v", got[2:])
	}
}
