package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestExtractReadableFromJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"json with text", `{"text":"extracted"}`, "extracted"},
		{"json with message", `{"message":"ok"}`, "ok"},
		{"json with content", `{"content":"result"}`, "result"},
		{"invalid json", `{invalid`, `{invalid`},
		{"empty object", `{}`, `{}`},
		{"array of strings", `["first"]`, "first"},
		{"array empty", `[]`, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractReadableFromJSON(tc.in)
			if got != tc.want {
				t.Errorf("extractReadableFromJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRenderFeishuStreamFinalTextUsesParts(t *testing.T) {
	t.Parallel()

	msg := channel.Message{
		Text: "plain fallback",
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: "Hello", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	got := renderFeishuStreamFinalText(msg, "buffered plain text")
	want := "**Hello**\n\n[docs](https://example.test)"
	if got != want {
		t.Fatalf("expected rich parts to drive Feishu stream final\n  got:  %q\n  want: %q", got, want)
	}
}

func TestRenderFeishuStreamFinalTextLongRichPartsFallsBackToPlain(t *testing.T) {
	t.Parallel()

	got := renderFeishuStreamFinalText(channel.Message{
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: strings.Repeat("你", feishuStreamMaxRunes+100), Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
		},
	}, "buffered plain text")
	if strings.Contains(got, "**") {
		t.Fatalf("long rich stream final should fall back to plain text, got prefix %q", got[:20])
	}
	if len([]rune(got)) <= feishuStreamMaxRunes {
		t.Fatalf("render helper should return full plain fallback before patch truncation, got len=%d", len([]rune(got)))
	}
}

func TestRenderFeishuStreamFinalTextUsesAuthoritativeTextBeforeBuffer(t *testing.T) {
	t.Parallel()

	got := renderFeishuStreamFinalText(channel.Message{Text: "plain fallback"}, "buffered plain text")
	if got != "plain fallback" {
		t.Fatalf("expected authoritative final text, got %q", got)
	}
}

func TestFeishuStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const leaked = "feishu-leaked-value-123456"
	redact.SetSecrets("feishu-stream-test", leaked)

	cases := []struct {
		name  string
		event channel.PreparedStreamEvent
		want  []string
	}{
		{
			name:  "coded error shows the copy as it is",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
			want:  []string{"The workspace is unreachable."},
		},
		{
			name:  "uncoded error is redacted and labelled",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "request failed with token " + leaked},
			want:  []string{"Error: request failed with token " + strings.Repeat("*", len(leaked))},
		},
		{
			name:  "blank error sends nothing",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "  "},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				patched []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`)
					return
				}
				if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/im/v1/messages/om_card") {
					var body struct {
						Content string `json:"content"`
					}
					raw, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(raw, &body)
					mu.Lock()
					patched = append(patched, body.Content)
					mu.Unlock()
					_, _ = io.WriteString(w, `{"code":0}`)
					return
				}
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			stream := &feishuOutboundStream{
				adapter:       &FeishuAdapter{},
				client:        lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL)),
				cardMessageID: "om_card",
				lastPatched:   "draft",
			}
			if err := stream.Push(context.Background(), tc.event); err != nil {
				t.Fatalf("push error: %v", err)
			}
			want := make([]string, 0, len(tc.want))
			for _, text := range tc.want {
				content, err := buildFeishuStreamCardContent(text)
				if err != nil {
					t.Fatalf("build card: %v", err)
				}
				want = append(want, content)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(patched) != len(want) || strings.Join(patched, "|") != strings.Join(want, "|") {
				t.Fatalf("patched cards = %q, want %q", patched, want)
			}
		})
	}
}

func TestFeishuStreamSegmentEndKeepsPreToolText(t *testing.T) {
	type cardCall struct {
		method    string
		messageID string
		content   string
	}
	var (
		mu      sync.Mutex
		calls   []cardCall
		created int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`)
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/im/v1/messages"):
			created++
			messageID := "om_card_new"
			calls = append(calls, cardCall{method: r.Method, messageID: messageID, content: body.Content})
			_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"`+messageID+`"}}`)
		case r.Method == http.MethodPatch:
			calls = append(calls, cardCall{method: r.Method, messageID: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], content: body.Content})
			_, _ = io.WriteString(w, `{"code":0}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	stream := &feishuOutboundStream{
		adapter:       &FeishuAdapter{},
		client:        lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL)),
		receiveID:     "oc_chat",
		receiveType:   "chat_id",
		cardMessageID: "om_card",
		lastPatched:   feishuStreamThinkingText,
	}
	ctx := context.Background()
	for _, event := range []channel.PreparedStreamEvent{
		{Type: channel.StreamEventDelta, Delta: "Let me check.", Phase: channel.StreamPhaseText},
		{Type: channel.StreamEventSegmentEnd},
		{Type: channel.StreamEventSegmentEnd},
		{Type: channel.StreamEventDelta, Delta: "Here is the answer.", Phase: channel.StreamPhaseText},
		{Type: channel.StreamEventFinal, Final: &channel.PreparedStreamFinalizePayload{Message: channel.PreparedMessage{Message: channel.Message{Text: "Here is the answer."}}}},
	} {
		if err := stream.Push(ctx, event); err != nil {
			t.Fatalf("push %s: %v", event.Type, err)
		}
	}

	card := func(text string) string {
		content, err := buildFeishuStreamCardContent(text)
		if err != nil {
			t.Fatalf("build card: %v", err)
		}
		return content
	}
	want := []cardCall{
		{method: http.MethodPatch, messageID: "om_card", content: card("Let me check.")},
		{method: http.MethodPost, messageID: "om_card_new", content: card(feishuStreamThinkingText)},
		{method: http.MethodPatch, messageID: "om_card_new", content: card("Here is the answer.")},
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 1 || len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

type feishuRecordedCall struct{ method, path, content string }

type feishuInboundProcessorFunc func(context.Context, channel.ChannelConfig, channel.InboundMessage, channel.StreamReplySender) error

func (fn feishuInboundProcessorFunc) HandleInbound(ctx context.Context, cfg channel.ChannelConfig, msg channel.InboundMessage, sender channel.StreamReplySender) error {
	return fn(ctx, cfg, msg, sender)
}

func newNonStreamingHarness(t *testing.T, failCreate bool) (*FeishuAdapter, channel.ChannelConfig, func() []feishuRecordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []feishuRecordedCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tenant-token","expire":7200}`)
			return
		}
		var body struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, feishuRecordedCall{r.Method, r.URL.Path, body.Content})
		id := "om_msg_" + strconv.Itoa(len(calls))
		mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/cardkit/"):
			t.Errorf("unexpected CardKit call when streaming is disabled: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		case failCreate && r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/im/v1/messages"):
			_, _ = io.WriteString(w, `{"code":99991400,"msg":"rate limit exceeded"}`)
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"code":0,"data":{"message_id":"`+id+`"}}`)
		default:
			_, _ = io.WriteString(w, `{"code":0}`)
		}
	}))
	t.Cleanup(server.Close)
	cfg := channel.ChannelConfig{
		ID: "cfg-nonstream", BotID: "bot-1", ChannelType: Type,
		Credentials: map[string]any{"appId": "cli_test", "appSecret": "sec_test", "enableStreaming": false},
	}
	return &FeishuAdapter{openBaseURL: server.URL}, cfg, func() []feishuRecordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]feishuRecordedCall(nil), calls...)
	}
}

func TestFeishuNonStreamingLifecycle(t *testing.T) {
	t.Parallel()
	const singleReply = "Hello Feishu!\nSecond line without incremental patches."
	textDelta := func(s string) channel.PreparedStreamEvent {
		return channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: s, Phase: channel.StreamPhaseText}
	}
	finalText := func(s string) channel.PreparedStreamEvent {
		return channel.PreparedStreamEvent{Type: channel.StreamEventFinal, Final: &channel.PreparedStreamFinalizePayload{Message: channel.PreparedMessage{Message: channel.Message{Text: s}}}}
	}
	emptyFinal := channel.PreparedStreamEvent{Type: channel.StreamEventFinal, Final: &channel.PreparedStreamFinalizePayload{}}

	for _, tc := range []struct {
		name           string
		opts           channel.StreamOptions
		events         []channel.PreparedStreamEvent
		wantPathSuffix string
		wantCards      []string
	}{
		{"single card reply", channel.StreamOptions{Reply: &channel.ReplyRef{MessageID: "om_inbound_1"}}, []channel.PreparedStreamEvent{{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}, textDelta("Hello Feishu!\n"), textDelta("Second line without incremental patches."), finalText(singleReply)}, "/im/v1/messages/om_inbound_1/reply", []string{singleReply}},
		{"empty Final flushes once", channel.StreamOptions{}, []channel.PreparedStreamEvent{{Type: channel.StreamEventFinal}, textDelta("Buffered before split flush"), emptyFinal, emptyFinal}, "", []string{"Buffered before split flush"}},
		{"multi-segment distinct POSTs", channel.StreamOptions{}, []channel.PreparedStreamEvent{textDelta("First segment."), {Type: channel.StreamEventSegmentEnd}, {Type: channel.StreamEventSegmentEnd}, textDelta("Second segment."), finalText("Second segment.")}, "", []string{"First segment.", "Second segment."}},
		{"reasoning-only sends nothing", channel.StreamOptions{}, []channel.PreparedStreamEvent{{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}, {Type: channel.StreamEventDelta, Delta: "chain-of-thought only", Phase: channel.StreamPhaseReasoning}, emptyFinal}, "", nil},
		{"reset discards draft", channel.StreamOptions{}, []channel.PreparedStreamEvent{textDelta("stale draft"), {Type: channel.StreamEventReset}, textDelta("regenerated reply"), emptyFinal}, "", []string{"regenerated reply"}},
		{"error sends single card", channel.StreamOptions{}, []channel.PreparedStreamEvent{{Type: channel.StreamEventError, Error: "Upstream provider rate limited.", ErrorCode: "provider.rate_limited"}}, "", []string{"Upstream provider rate limited."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter, cfg, getCalls := newNonStreamingHarness(t, false)
			ctx := context.Background()
			stream, err := adapter.OpenStream(ctx, cfg, "chat_id:oc_123", tc.opts)
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			for _, ev := range tc.events {
				if err := stream.Push(ctx, ev); err != nil {
					t.Fatalf("Push(%s): %v", ev.Type, err)
				}
			}
			if err := stream.Close(ctx); err != nil {
				t.Fatalf("Close: %v", err)
			}
			calls := getCalls()
			if len(calls) != len(tc.wantCards) {
				t.Fatalf("got %d calls, want %d: %+v", len(calls), len(tc.wantCards), calls)
			}
			for i, wantText := range tc.wantCards {
				wantCard, err := buildFeishuStreamCardContent(wantText)
				if err != nil {
					t.Fatalf("build card: %v", err)
				}
				if calls[i].method != http.MethodPost || calls[i].content != wantCard || (tc.wantPathSuffix != "" && !strings.HasSuffix(calls[i].path, tc.wantPathSuffix)) {
					t.Fatalf("call %d = %+v, want POST %q content=%q", i, calls[i], tc.wantPathSuffix, wantCard)
				}
			}
		})
	}
}

func TestFeishuNonStreamingLongReplySplitThroughManager(t *testing.T) {
	t.Parallel()
	longChunk := strings.Repeat("甲", 6499) + "。"
	tailChunk := "尾部段落完整保留不丢失：" + strings.Repeat("乙", 2100) + "。"
	delta := func(s string) channel.StreamEvent {
		return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: s, Phase: channel.StreamPhaseText}
	}
	finalMsg := func(s string) channel.StreamEvent {
		return channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: s}}}
	}

	for _, tc := range []struct {
		name        string
		events      []channel.StreamEvent
		wantSubstrs []string
	}{
		{"single-segment >8000 runes", []channel.StreamEvent{{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}, delta(longChunk), delta(tailChunk), finalMsg(longChunk + tailChunk)}, []string{"甲", "尾部段落完整保留不丢失"}},
		{"split before SegmentEnd + post-tool answer", []channel.StreamEvent{{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted}, delta(longChunk), delta("第一段剩余文字。"), {Type: channel.StreamEventSegmentEnd}, delta("工具执行后的最终结论。"), finalMsg("工具执行后的最终结论。")}, []string{"甲", "第一段剩余文字。", "工具执行后的最终结论。"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter, cfg, getCalls := newNonStreamingHarness(t, false)
			done := make(chan error, 1)
			mgr := channel.NewManager(nil, nil, nil, feishuInboundProcessorFunc(func(ctx context.Context, _ channel.ChannelConfig, _ channel.InboundMessage, sender channel.StreamReplySender) (err error) {
				defer func() { done <- err }()
				stream, err := sender.OpenStream(ctx, "chat_id:oc_long", channel.StreamOptions{})
				if err != nil {
					return err
				}
				for _, ev := range tc.events {
					if err = stream.Push(ctx, ev); err != nil {
						return err
					}
				}
				return stream.Close(ctx)
			}))
			mgr.RegisterAdapter(adapter)
			ctx := context.Background()
			t.Cleanup(func() { _ = mgr.Shutdown(ctx) })

			if err := mgr.HandleInbound(ctx, cfg, channel.InboundMessage{Channel: Type, ReplyTarget: "chat_id:oc_long", Message: channel.Message{Text: "ping"}}); err != nil {
				t.Fatalf("HandleInbound: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("stream drive failed: %v", err)
			}
			calls := getCalls()
			if len(calls) != len(tc.wantSubstrs) {
				t.Fatalf("got %d calls, want %d: %+v", len(calls), len(tc.wantSubstrs), calls)
			}
			for i, sub := range tc.wantSubstrs {
				if calls[i].method != http.MethodPost || !strings.Contains(calls[i].content, sub) {
					t.Fatalf("call %d = %+v, want POST containing %q", i, calls[i], sub)
				}
			}
		})
	}
}

func TestFeishuNonStreamingCommitSegmentPropagatesError(t *testing.T) {
	t.Parallel()
	adapter, cfg, _ := newNonStreamingHarness(t, true)
	ctx := context.Background()
	stream, err := adapter.OpenStream(ctx, cfg, "chat_id:oc_err", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := stream.Push(ctx, channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "Important pre-tool preamble", Phase: channel.StreamPhaseText}); err != nil {
		t.Fatalf("Push delta: %v", err)
	}
	if err := stream.Push(ctx, channel.PreparedStreamEvent{Type: channel.StreamEventSegmentEnd}); err == nil {
		t.Fatal("expected SegmentEnd to return error when Feishu card creation fails")
	}
	if got := stream.(*feishuOutboundStream).textBuffer.String(); got != "Important pre-tool preamble" {
		t.Fatalf("textBuffer = %q, want unsent preamble preserved on error", got)
	}
	if err := stream.Push(ctx, channel.PreparedStreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: &channel.StreamToolCall{CallID: "call-1", Name: "bash"}}); err == nil {
		t.Fatal("expected ToolCallStart to return error when commitSegment fails")
	}
}
