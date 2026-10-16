package feishu

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
