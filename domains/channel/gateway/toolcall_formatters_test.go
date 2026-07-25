package gateway

import (
	"strings"
	"testing"
)

func hasTextBlock(body []ToolCallBlock, needle string) bool {
	for _, b := range body {
		if b.Type != ToolCallBlockText {
			continue
		}
		if strings.Contains(b.Text, needle) {
			return true
		}
	}
	return false
}

func hasLinkBlock(body []ToolCallBlock, url string) *ToolCallBlock {
	for i := range body {
		if body[i].Type != ToolCallBlockLink {
			continue
		}
		if body[i].URL == url {
			return &body[i]
		}
	}
	return nil
}

func TestFormatListIncludesEntries(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "list",
		Input: map[string]any{"path": "/var/log"},
		Result: map[string]any{
			"total_count": float64(12),
			"entries": []any{
				map[string]any{"path": "syslog", "is_dir": false, "size": float64(2300)},
				map[string]any{"path": "auth.log", "is_dir": false, "size": float64(1100000)},
				map[string]any{"path": "nginx", "is_dir": true},
			},
		},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, "/var/log") || !strings.Contains(p.Header, "12 entries") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !hasTextBlock(p.Body, "syslog") {
		t.Fatalf("expected syslog entry in body, got %+v", p.Body)
	}
	if !hasTextBlock(p.Body, "nginx/") {
		t.Fatalf("expected nginx/ directory entry in body, got %+v", p.Body)
	}
	if !hasTextBlock(p.Body, "…and 9 more") {
		t.Fatalf("expected ellipsis footer for remaining items, got %+v", p.Body)
	}
	if p.InputSummary != "" || p.ResultSummary != "" {
		t.Fatalf("formatter output must not leak InputSummary/ResultSummary raw JSON, got in=%q res=%q", p.InputSummary, p.ResultSummary)
	}
	rendered := RenderToolCallMessage(p)
	if strings.Contains(rendered, "\"entries\"") || strings.Contains(rendered, "{\"") {
		t.Fatalf("rendered output leaked raw JSON result:\n%s", rendered)
	}
	if strings.Contains(rendered, "ask_user") || strings.Contains(rendered, "waiting") {
		t.Fatalf("rendered output exposed tool lifecycle:\n%s", rendered)
	}
}

func TestFormatAskUserRendersQuestionsWithoutMetadata(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:    "ask_user",
		ShortID: 7,
		Input: map[string]any{
			"user_input_id": "input-1",
			"short_id":      7,
			"status":        "pending",
			"payload": map[string]any{
				"questions": []any{
					map[string]any{
						"id":   "q1",
						"text": "Pick one",
						"kind": "single_select",
						"options": []any{
							map[string]any{"id": "q1.o1", "label": "Alpha", "description": "First choice"},
							map[string]any{"id": "q1.o2", "label": "Beta"},
						},
					},
				},
			},
		},
	}

	p := BuildToolCallStart(tc)
	if p.Status != ToolCallStatusWaiting || p.Header != "Pick one" {
		t.Fatalf("presentation = %#v", p)
	}
	rendered := RenderToolCallMessage(p)
	for _, want := range []string{"Pick one", "1) Alpha - First choice", "2) Beta", "Reply with an option number or its text.", "skip"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, rendered)
		}
	}
	for _, leaked := range []string{"/respond", "Memoh Web"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("rendered output leaked command/web fallback %q:\n%s", leaked, rendered)
		}
	}
	for _, leaked := range []string{"user_input_id", "short_id", "status\":", "payload\":"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("rendered output leaked %q:\n%s", leaked, rendered)
		}
	}
}

func TestFormatAskUserOnlyRendersFirstQuestionForTextFallback(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{Name: "ask_user", Input: map[string]any{
		"payload": map[string]any{"questions": []any{
			map[string]any{"text": "First question", "kind": "single_select", "options": []any{
				map[string]any{"label": "Alpha"}, map[string]any{"label": "Beta"},
			}},
			map[string]any{"text": "Second question", "kind": "text"},
		}},
	}}
	rendered := RenderToolCallMessage(BuildToolCallStart(tc))
	if !strings.Contains(rendered, "1/2") || !strings.Contains(rendered, "First question") || strings.Contains(rendered, "Second question") {
		t.Fatalf("plain-text prompt must show only the current question:\n%s", rendered)
	}
}

func TestFormatAskUserUsesRequestedLocale(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{Name: "ask_user", Locale: "zh", Input: map[string]any{
		"payload": map[string]any{"questions": []any{map[string]any{
			"text": "选一个", "kind": "single_select", "options": []any{map[string]any{"label": "甲"}, map[string]any{"label": "乙"}},
		}}},
	}}
	rendered := RenderToolCallMessage(BuildToolCallStart(tc))
	for _, want := range []string{"回复选项序号或选项文字。", "发送“跳过”跳过本题。"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("localized prompt missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Reply with") || strings.Contains(rendered, "Send \"skip\"") {
		t.Fatalf("localized prompt leaked English chrome:\n%s", rendered)
	}
}

func TestFormatAskUserCompletionIsSilent(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{Name: "ask_user", Input: map[string]any{
		"payload": map[string]any{"questions": []any{map[string]any{"text": "Old question", "kind": "text"}}},
	}, Result: map[string]any{"status": "submitted"}}
	if rendered := RenderToolCallMessage(BuildToolCallEnd(tc)); rendered != "" {
		t.Fatalf("completed ask_user repeated its prompt: %q", rendered)
	}
}

func TestFormatExecForeground(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:   "exec",
		Input:  map[string]any{"command": "ls -la /tmp"},
		Result: map[string]any{"exit_code": float64(0), "stdout": "total 16\nfoo bar"},
	}
	p := BuildToolCallEnd(tc)
	if !strings.HasPrefix(p.Header, "$ ls -la /tmp") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if p.Footer != "exit=0" {
		t.Fatalf("unexpected footer: %q", p.Footer)
	}
	if !hasTextBlock(p.Body, "stdout: total 16") {
		t.Fatalf("expected stdout block, got %+v", p.Body)
	}
}

func TestFormatExecBackground(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "exec",
		Input: map[string]any{"command": "long_running.sh"},
		Result: map[string]any{
			"status":      "background_started",
			"task_id":     "bg_abc",
			"output_file": "/tmp/bg_abc.log",
		},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Footer, "background_started") ||
		!strings.Contains(p.Footer, "task_id=bg_abc") ||
		!strings.Contains(p.Footer, "/tmp/bg_abc.log") {
		t.Fatalf("unexpected footer: %q", p.Footer)
	}
}

func TestFormatWebSearchEmitsLinks(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "web_search",
		Input: map[string]any{"query": "golang generics tutorial"},
		Result: map[string]any{
			"results": []any{
				map[string]any{
					"title":       "Tutorial: Getting started with generics",
					"url":         "https://go.dev/doc/tutorial/generics",
					"description": "A comprehensive walkthrough",
				},
				map[string]any{
					"title": "Go 1.18 is released",
					"url":   "https://go.dev/blog/go1.18",
				},
			},
		},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, "2 results") || !strings.Contains(p.Header, `"golang generics tutorial"`) {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	link := hasLinkBlock(p.Body, "https://go.dev/doc/tutorial/generics")
	if link == nil {
		t.Fatalf("expected link block for tutorial, got %+v", p.Body)
		return
	}
	if link.Title != "Tutorial: Getting started with generics" {
		t.Fatalf("unexpected title: %q", link.Title)
	}
	if link.Desc != "A comprehensive walkthrough" {
		t.Fatalf("unexpected desc: %q", link.Desc)
	}
}

func TestFormatSendCarriesTargetAndBody(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name: "send",
		Input: map[string]any{
			"target":   "chat:123",
			"platform": "telegram",
			"body":     "Hello there",
		},
		Result: map[string]any{"delivered": "delivered", "message_id": "msg_456"},
	}
	p := BuildToolCallEnd(tc)
	if p.Header != "→ chat:123 (telegram)" {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !hasTextBlock(p.Body, "Hello there") {
		t.Fatalf("expected body text, got %+v", p.Body)
	}
	if !strings.Contains(p.Footer, "message_id=msg_456") {
		t.Fatalf("unexpected footer: %q", p.Footer)
	}
}

func TestFormatSendEmailCarriesSubject(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name: "send_email",
		Input: map[string]any{
			"to":      "alice@example.com",
			"subject": "Meeting notes",
		},
		Result: map[string]any{"status": "sent", "message_id": "mid1"},
	}
	p := BuildToolCallEnd(tc)
	if p.Header != "→ alice@example.com" {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !hasTextBlock(p.Body, "Subject: Meeting notes") {
		t.Fatalf("expected subject block, got %+v", p.Body)
	}
	if !strings.Contains(p.Footer, "status=sent") || !strings.Contains(p.Footer, "message_id=mid1") {
		t.Fatalf("unexpected footer: %q", p.Footer)
	}
}

func TestFormatSearchMemoryPrintsScoreAndTotal(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "search_memory",
		Input: map[string]any{"query": "previous trip to Tokyo"},
		Result: map[string]any{
			"total": float64(10),
			"results": []any{
				map[string]any{"text": "Alice prefers sushi over ramen", "score": float64(0.91)},
				map[string]any{"text": "Bob mentioned a trip to Tokyo in March", "score": float64(0.87)},
			},
		},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, "2 / 10 results") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !hasTextBlock(p.Body, "(0.91)") {
		t.Fatalf("expected score annotation, got %+v", p.Body)
	}
}

func TestFormatSearchMessagesTruncatesPreview(t *testing.T) {
	t.Parallel()

	msgs := make([]any, 0, 6)
	for i := 0; i < 6; i++ {
		msgs = append(msgs, map[string]any{
			"role":       "user",
			"text":       "I need a kanban board",
			"created_at": "2026-04-22 10:00",
		})
	}
	tc := &StreamToolCall{
		Name:   "search_messages",
		Input:  map[string]any{"keyword": "kanban"},
		Result: map[string]any{"messages": msgs},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, `keyword="kanban"`) || !strings.Contains(p.Header, "6 messages") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !strings.Contains(p.Footer, "…and 3 more") {
		t.Fatalf("expected ellipsis footer, got %q", p.Footer)
	}
}

func TestFormatAgentControlResult(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name: "spawn_agent",
		Result: map[string]any{
			"agent_id":   "agent_1",
			"session_id": "sess_1",
			"task_id":    "bg_bot1_1234",
			"status":     "completed",
			"text":       "analyzed repo structure",
		},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, "agent_1") || !strings.Contains(p.Header, "completed") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if !hasTextBlock(p.Body, "task bg_bot1_1234") {
		t.Fatalf("expected task id in body, got %+v", p.Body)
	}
	if !hasTextBlock(p.Body, "analyzed repo structure") {
		t.Fatalf("expected report in body, got %+v", p.Body)
	}
}

func TestFormatWebFetchShowsLink(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "web_fetch",
		Input: map[string]any{"url": "https://example.com/article"},
		Result: map[string]any{
			"title":  "Example Article Title",
			"format": "markdown",
			"length": float64(3421),
		},
	}
	p := BuildToolCallEnd(tc)
	link := hasLinkBlock(p.Body, "https://example.com/article")
	if link == nil {
		t.Fatalf("expected link block, got %+v", p.Body)
		return
	}
	if link.Title != "Example Article Title" {
		t.Fatalf("unexpected link title: %q", link.Title)
	}
	if !strings.Contains(p.Footer, "markdown") || !strings.Contains(p.Footer, "3421 chars") {
		t.Fatalf("unexpected footer: %q", p.Footer)
	}
}

func TestFormatCreateScheduleSuccess(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name: "create_schedule",
		Input: map[string]any{
			"name":    "Daily report",
			"pattern": "0 9 * * *",
		},
		Result: map[string]any{"id": "sch_42"},
	}
	p := BuildToolCallEnd(tc)
	if !strings.Contains(p.Header, "[sch_42]") || !strings.Contains(p.Header, "Daily report") {
		t.Fatalf("unexpected header: %q", p.Header)
	}
}

func TestFormatFailureEmitsErrorFooter(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:   "read",
		Input:  map[string]any{"path": "/etc/shadow"},
		Result: map[string]any{"error": "permission denied"},
	}
	p := BuildToolCallEnd(tc)
	if p.Status != ToolCallStatusFailed {
		t.Fatalf("expected failed status, got %q", p.Status)
	}
	if !strings.Contains(p.Footer, "permission denied") {
		t.Fatalf("expected error footer, got %q", p.Footer)
	}
}

func TestFormatExecFailedExitCode(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:  "exec",
		Input: map[string]any{"command": "false"},
		Result: map[string]any{
			"exit_code": float64(2),
			"stderr":    "boom",
		},
	}
	p := BuildToolCallEnd(tc)
	if p.Status != ToolCallStatusFailed {
		t.Fatalf("expected failed status, got %q", p.Status)
	}
	if !strings.Contains(p.Footer, "error") || !strings.Contains(p.Footer, "boom") {
		t.Fatalf("expected stderr-based error footer, got %q", p.Footer)
	}
}

func TestExternalToolFallsBackToGenericSummary(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:   "mcp.custom.do_thing",
		Input:  map[string]any{"foo": "bar"},
		Result: map[string]any{"ok": true},
	}
	p := BuildToolCallEnd(tc)
	if p.Emoji != ExternalToolCallEmoji {
		t.Fatalf("expected external emoji, got %q", p.Emoji)
	}
	if p.Status != ToolCallStatusCompleted {
		t.Fatalf("expected completed status, got %q", p.Status)
	}
	if p.InputSummary == "" {
		t.Fatalf("expected generic input summary to be populated")
	}
}
