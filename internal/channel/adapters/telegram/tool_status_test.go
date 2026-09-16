package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"
	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

type botAPICall struct {
	method string
	params map[string]any
}

func (c botAPICall) param(key string) string {
	switch value := c.params[key].(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		data, _ := json.Marshal(value)
		return string(data)
	}
}

// fakeBotAPI is a minimal Telegram Bot API server. It records every call and
// answers like Telegram does, so the adapter runs through the real client.
type fakeBotAPI struct {
	server *httptest.Server

	mu      sync.Mutex
	calls   []botAPICall
	lastID  int
	deleted map[string]bool
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	api := &fakeBotAPI{lastID: 100, deleted: map[string]bool{}}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeBotAPI) serve(w http.ResponseWriter, r *http.Request) {
	call := botAPICall{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], params: map[string]any{}}
	if body, err := io.ReadAll(r.Body); err == nil && len(body) > 0 {
		_ = json.Unmarshal(body, &call.params)
	}

	a.mu.Lock()
	a.calls = append(a.calls, call)
	var result any = true
	failure := ""
	switch call.method {
	case "getMe":
		result = map[string]any{"id": 42, "is_bot": true, "first_name": "Memoh", "username": "memoh_test_bot"}
	case "sendMessage", "editMessageText":
		messageID := call.param("message_id")
		if call.method == "sendMessage" {
			a.lastID++
			messageID = strconv.Itoa(a.lastID)
		} else if a.deleted[messageID] {
			failure = "Bad Request: message to edit not found"
		}
		id, _ := strconv.Atoi(messageID)
		chatID, _ := strconv.ParseInt(call.param("chat_id"), 10, 64)
		result = map[string]any{
			"message_id": id,
			"date":       1,
			"chat":       map[string]any{"id": chatID, "type": "supergroup"},
			"text":       call.param("text"),
		}
	}
	a.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if failure != "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": http.StatusBadRequest, "description": failure})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// markDeleted makes later edits of the message fail as if a user deleted it.
func (a *fakeBotAPI) markDeleted(messageID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted[messageID] = true
}

// chatCalls returns the calls that change what the chat shows, in order.
func (a *fakeBotAPI) chatCalls() []botAPICall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]botAPICall, 0, len(a.calls))
	for _, call := range a.calls {
		switch call.method {
		case "sendMessage", "editMessageText", "sendMessageDraft":
			out = append(out, call)
		}
	}
	return out
}

func (a *fakeBotAPI) waitFor(t *testing.T, what string, done func([]botAPICall) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done(a.chatCalls()) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; calls:%s", what, describeBotAPICalls(a.chatCalls()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func describeBotAPICalls(calls []botAPICall) string {
	var b strings.Builder
	for i, call := range calls {
		fmt.Fprintf(&b, "\n  %d. %s draft_id=%s message_id=%s text=%q", i, call.method, call.param("draft_id"), call.param("message_id"), call.param("text"))
	}
	return b.String()
}

func indexOfBotAPICall(calls []botAPICall, match func(botAPICall) bool) int {
	for i, call := range calls {
		if match(call) {
			return i
		}
	}
	return -1
}

func lastIndexOfBotAPICall(calls []botAPICall, match func(botAPICall) bool) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if match(calls[i]) {
			return i
		}
	}
	return -1
}

func countBotAPICalls(calls []botAPICall, match func(botAPICall) bool) int {
	n := 0
	for _, call := range calls {
		if match(call) {
			n++
		}
	}
	return n
}

func isToolStatusDraft(call botAPICall) bool {
	return call.method == "sendMessageDraft" && call.param("draft_id") == strconv.Itoa(telegramToolStatusDraftID)
}

func openReusingTelegramStream(t *testing.T, api *fakeBotAPI, target, conversationType string) *telegramOutboundStream {
	t.Helper()
	adapter := NewTelegramAdapter(nil)
	adapter.streamLimiter = rate.NewLimiter(rate.Inf, 1)
	opened, err := adapter.OpenStream(context.Background(), channel.ChannelConfig{
		ID:          "cfg-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Credentials: map[string]any{"botToken": "123456:TOOLSTATUS", "apiBaseURL": api.server.URL},
	}, target, channel.StreamOptions{
		Metadata:             map[string]any{"conversation_type": conversationType},
		ReuseToolCallMessage: true,
	})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	stream, ok := opened.(*telegramOutboundStream)
	if !ok {
		t.Fatalf("unexpected stream type %T", opened)
	}
	return stream
}

func pushTelegramStreamEvent(t *testing.T, stream *telegramOutboundStream, event channel.StreamEvent) {
	t.Helper()
	if err := stream.Push(context.Background(), mustPreparedTelegramEvent(t, event)); err != nil {
		t.Fatalf("push %s: %v", event.Type, err)
	}
}

func telegramReadStart(id, path string) channel.StreamEvent {
	return channel.StreamEvent{
		Type:     channel.StreamEventToolCallStart,
		ToolCall: &channel.StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}},
	}
}

func telegramReadEnd(id, path string, result map[string]any) channel.StreamEvent {
	return channel.StreamEvent{
		Type:     channel.StreamEventToolCallEnd,
		ToolCall: &channel.StreamToolCall{Name: "read", CallID: id, Input: map[string]any{"path": path}, Result: result},
	}
}

func telegramTextDelta(text string) channel.StreamEvent {
	return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: text, Phase: channel.StreamPhaseText}
}

func telegramFinal(text string) channel.StreamEvent {
	return channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{Message: channel.Message{Text: text}}}
}

func TestTelegramPrivateChatShowsToolCallsAsDraftPreview(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t)
	stream := openReusingTelegramStream(t, api, "777", "private")

	pushTelegramStreamEvent(t, stream, telegramTextDelta("Let me check."))
	pushTelegramStreamEvent(t, stream, telegramReadStart("c1", "/data/a.md"))
	api.waitFor(t, "a status draft with the running call", func(calls []botAPICall) bool {
		return indexOfBotAPICall(calls, func(call botAPICall) bool {
			return isToolStatusDraft(call) && strings.Contains(call.param("text"), "📖 read · running")
		}) >= 0
	})
	pushTelegramStreamEvent(t, stream, telegramReadStart("c2", "/data/b.md"))
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c2", "/data/b.md", map[string]any{"ok": true}))
	api.waitFor(t, "a status draft with both calls completed", func(calls []botAPICall) bool {
		return indexOfBotAPICall(calls, func(call botAPICall) bool {
			return isToolStatusDraft(call) && strings.Count(call.param("text"), "read · completed") == 2
		}) >= 0
	})
	pushTelegramStreamEvent(t, stream, telegramTextDelta("All done."))
	pushTelegramStreamEvent(t, stream, telegramFinal("All done."))
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := api.chatCalls()
	var sent []string
	for _, call := range calls {
		switch call.method {
		case "sendMessage":
			sent = append(sent, call.param("text"))
		case "editMessageText":
			t.Fatalf("private chats must not edit messages for tool calls:%s", describeBotAPICalls(calls))
		}
	}
	if fmt.Sprint(sent) != "[Let me check. All done.]" {
		t.Fatalf("only the assistant text may become messages, got %q", sent)
	}
	isMessage := func(text string) func(botAPICall) bool {
		return func(call botAPICall) bool { return call.method == "sendMessage" && call.param("text") == text }
	}
	firstStatus := indexOfBotAPICall(calls, isToolStatusDraft)
	if firstStatus < indexOfBotAPICall(calls, isMessage("Let me check.")) ||
		lastIndexOfBotAPICall(calls, isToolStatusDraft) > indexOfBotAPICall(calls, isMessage("All done.")) {
		t.Fatalf("status drafts must sit between the preamble and the answer:%s", describeBotAPICalls(calls))
	}
	if calls[firstStatus].param("parse_mode") != tele.ModeHTML {
		t.Fatalf("status drafts must be sent as HTML:%s", describeBotAPICalls(calls))
	}
}

func TestTelegramPrivateChatRefreshesToolDraftWhileToolRuns(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t)
	stream := openReusingTelegramStream(t, api, "778", "private")
	// A draft expires after 30 seconds, so a long tool call refreshes it.
	stream.toolStatusKeepAlive = 20 * time.Millisecond

	pushTelegramStreamEvent(t, stream, telegramReadStart("c1", "/data/big.log"))
	api.waitFor(t, "repeated status drafts", func(calls []botAPICall) bool {
		return countBotAPICalls(calls, isToolStatusDraft) >= 3
	})
	pushTelegramStreamEvent(t, stream, telegramFinal("Here is what I have so far."))
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	refreshes := countBotAPICalls(api.chatCalls(), isToolStatusDraft)
	time.Sleep(4 * stream.toolStatusKeepAlive)
	calls := api.chatCalls()
	if got := countBotAPICalls(calls, isToolStatusDraft); got != refreshes {
		t.Fatalf("status drafts must stop with the batch: %d before, %d after", refreshes, got)
	}
	answer := indexOfBotAPICall(calls, func(call botAPICall) bool { return call.method == "sendMessage" })
	if answer < 0 || lastIndexOfBotAPICall(calls, isToolStatusDraft) > answer {
		t.Fatalf("no status draft may follow the answer:%s", describeBotAPICalls(calls))
	}
}

func TestTelegramGroupChatEditsOneToolStatusMessage(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t)
	stream := openReusingTelegramStream(t, api, "-100200", "group")
	// Hold intermediate edits back so the calls are deterministic.
	stream.toolStatusInterval = time.Hour

	pushTelegramStreamEvent(t, stream, telegramReadStart("c1", "/data/a.md"))
	api.waitFor(t, "the status message", func(calls []botAPICall) bool { return len(calls) == 1 })
	pushTelegramStreamEvent(t, stream, telegramReadStart("c2", "/data/b.md"))
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c2", "/data/b.md", map[string]any{"error": "permission denied"}))
	pushTelegramStreamEvent(t, stream, telegramTextDelta("Done"))
	pushTelegramStreamEvent(t, stream, telegramFinal("Done"))
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := api.chatCalls()
	if len(calls) != 4 {
		t.Fatalf("expected the status message, its final edit and the streamed answer:%s", describeBotAPICalls(calls))
	}
	status, final := calls[0], calls[1]
	if status.method != "sendMessage" || !strings.Contains(status.param("text"), "📖 read · running") || status.param("parse_mode") != tele.ModeHTML {
		t.Fatalf("the first call must create the status message:%s", describeBotAPICalls(calls))
	}
	if final.method != "editMessageText" || final.param("message_id") != "101" {
		t.Fatalf("the batch must end by editing the status message:%s", describeBotAPICalls(calls))
	}
	for _, want := range []string{"📖 read · completed · /data/a.md", "📖 read · failed", "error: permission denied"} {
		if !strings.Contains(final.param("text"), want) {
			t.Fatalf("final status is missing %q:%s", want, describeBotAPICalls(calls))
		}
	}
	if calls[2].method != "sendMessage" || !strings.HasPrefix(calls[2].param("text"), "Done") ||
		calls[3].method != "editMessageText" || calls[3].param("text") != "Done" {
		t.Fatalf("the answer must stream below the status message:%s", describeBotAPICalls(calls))
	}
}

func TestTelegramGroupChatKeepsApprovalCardsAndRevisesEarlierStatus(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t)
	stream := openReusingTelegramStream(t, api, "-100201", "group")
	stream.toolStatusInterval = time.Hour

	pushTelegramStreamEvent(t, stream, telegramReadStart("c1", "/data/a.md"))
	api.waitFor(t, "the status message", func(calls []botAPICall) bool { return len(calls) == 1 })
	// A parallel call asks for approval while c1 is still running.
	pushTelegramStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallStart, ToolCall: &channel.StreamToolCall{
		Name:       "exec",
		CallID:     "c2",
		ApprovalID: "approval-1",
		ShortID:    3,
		Input:      map[string]any{"command": "rm -rf /tmp/cache"},
		Actions: []channel.Action{
			{Type: "tool_approval", Label: "Approve", Value: "approve:approval-1"},
			{Type: "tool_approval", Label: "Reject", Value: "reject:approval-1"},
		},
	}})
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	pushTelegramStreamEvent(t, stream, channel.StreamEvent{Type: channel.StreamEventToolCallEnd, ToolCall: &channel.StreamToolCall{
		Name:   "exec",
		CallID: "c2",
		Input:  map[string]any{"command": "rm -rf /tmp/cache"},
		Result: map[string]any{"exit_code": 0, "stdout": "removed"},
	}})
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := api.chatCalls()
	if len(calls) != 4 {
		t.Fatalf("expected the status message, the approval card, a status revision and the card result:%s", describeBotAPICalls(calls))
	}
	if card := calls[1]; card.method != "sendMessage" || !strings.Contains(card.param("reply_markup"), "approve:approval-1") {
		t.Fatalf("the approval prompt must be its own card with buttons:%s", describeBotAPICalls(calls))
	}
	if revision := calls[2]; revision.method != "editMessageText" || revision.param("message_id") != "101" ||
		!strings.Contains(revision.param("text"), "📖 read · completed · /data/a.md") {
		t.Fatalf("c1 must finish in the status message it started in:%s", describeBotAPICalls(calls))
	}
	if result := calls[3]; result.method != "editMessageText" || result.param("message_id") != "102" ||
		result.param("reply_markup") != "" || !strings.Contains(result.param("text"), "completed") {
		t.Fatalf("the approval card must show the result without buttons:%s", describeBotAPICalls(calls))
	}
}

func TestTelegramGroupChatReplacesDeletedToolStatusMessage(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t)
	stream := openReusingTelegramStream(t, api, "-100202", "group")
	stream.toolStatusInterval = time.Hour

	pushTelegramStreamEvent(t, stream, telegramReadStart("c1", "/data/a.md"))
	api.waitFor(t, "the status message", func(calls []botAPICall) bool { return len(calls) == 1 })
	api.markDeleted("101")
	pushTelegramStreamEvent(t, stream, telegramReadEnd("c1", "/data/a.md", map[string]any{"ok": true}))
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	calls := api.chatCalls()
	if len(calls) != 3 || calls[1].method != "editMessageText" || calls[2].method != "sendMessage" ||
		!strings.Contains(calls[2].param("text"), "📖 read · completed · /data/a.md") {
		t.Fatalf("a deleted status message must be replaced by a new one:%s", describeBotAPICalls(calls))
	}
}

func TestRenderTelegramToolStatusFitsOneMessage(t *testing.T) {
	t.Parallel()

	calls := make([]channel.ToolCallPresentation, 0, 200)
	for i := range 200 {
		calls = append(calls, channel.BuildToolCallEnd(&channel.StreamToolCall{
			Name:   "exec",
			CallID: fmt.Sprintf("c%d", i),
			Input:  map[string]any{"command": fmt.Sprintf("cat <file-%d> && echo <&>", i)},
			Result: map[string]any{"exit_code": 1, "stderr": strings.Repeat("<&> ", 40)},
		}))
	}
	text, parseMode := renderTelegramToolStatus(channel.ToolCallStatusSnapshot{Calls: calls})
	if parseMode != tele.ModeHTML {
		t.Fatalf("parse mode = %q, want HTML", parseMode)
	}
	if n := utf8.RuneCountInString(text); n > telegramMaxMessageLength {
		t.Fatalf("rendered status has %d runes, over the %d limit", n, telegramMaxMessageLength)
	}
	if !strings.Contains(text, "earlier tool calls") {
		t.Fatalf("a batch too long for one message must hide its oldest calls: %q", text)
	}
}
