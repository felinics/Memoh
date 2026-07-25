package gateway

import (
	"strings"
	"testing"
)

func TestSummarizeToolInputFileField(t *testing.T) {
	t.Parallel()

	got := SummarizeToolInput("read", map[string]any{"path": "/var/log/syslog"})
	if got != "/var/log/syslog" {
		t.Fatalf("unexpected: %q", got)
	}
}

func TestSummarizeToolInputCommandFirstLine(t *testing.T) {
	t.Parallel()

	got := SummarizeToolInput("exec", map[string]any{"command": "echo hi\nsleep 10"})
	if got != "echo hi" {
		t.Fatalf("unexpected first line: %q", got)
	}
}

func TestSummarizeToolInputMessageTargetAndBody(t *testing.T) {
	t.Parallel()

	got := SummarizeToolInput("send", map[string]any{
		"target": "chat:123",
		"body":   "Hello there",
	})
	if !strings.Contains(got, "chat:123") || !strings.Contains(got, "Hello there") {
		t.Fatalf("unexpected target/body summary: %q", got)
	}
}

func TestSummarizeToolInputScheduleID(t *testing.T) {
	t.Parallel()

	got := SummarizeToolInput("update_schedule", map[string]any{
		"id":   "sch_42",
		"cron": "0 9 * * *",
	})
	if got != "sch_42 · 0 9 * * *" {
		t.Fatalf("unexpected schedule summary: %q", got)
	}
}

func TestSummarizeToolInputTruncatesLongValues(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 400)
	got := SummarizeToolInput("web_fetch", map[string]any{"url": long})
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected truncation suffix, got %q", got)
	}
	if len([]rune(got)) > 201 {
		t.Fatalf("summary not truncated: rune len=%d", len([]rune(got)))
	}
}

func TestSummarizeToolInputFallbackCompactJSON(t *testing.T) {
	t.Parallel()

	got := SummarizeToolInput("unknown", map[string]any{"alpha": 1, "beta": 2})
	if !strings.Contains(got, "\"alpha\"") || !strings.Contains(got, "\"beta\"") {
		t.Fatalf("expected JSON fallback: %q", got)
	}
}

func TestSummarizeToolResultPrefersError(t *testing.T) {
	t.Parallel()

	got := SummarizeToolResult("read", map[string]any{"error": "ENOENT", "ok": false})
	if !strings.HasPrefix(got, "error: ENOENT") {
		t.Fatalf("unexpected result: %q", got)
	}
}

func TestSummarizeToolResultCombinesSignals(t *testing.T) {
	t.Parallel()

	got := SummarizeToolResult("exec", map[string]any{
		"ok":        true,
		"exit_code": 0,
		"stdout":    "line1\nline2",
	})
	if !strings.Contains(got, "ok=true") {
		t.Fatalf("missing ok signal: %q", got)
	}
	if !strings.Contains(got, "exit=0") {
		t.Fatalf("missing exit_code: %q", got)
	}
	if !strings.Contains(got, "stdout: line1") {
		t.Fatalf("missing stdout first line: %q", got)
	}
}

func TestSummarizeToolResultPlainString(t *testing.T) {
	t.Parallel()

	got := SummarizeToolResult("read", "hello world")
	if got != "hello world" {
		t.Fatalf("unexpected plain result: %q", got)
	}
}

func TestSummarizeToolResultLargeJSONFallback(t *testing.T) {
	t.Parallel()

	items := make([]any, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, map[string]any{"id": i})
	}
	got := SummarizeToolResult("list", map[string]any{"items": items})
	if got == "" {
		t.Fatalf("expected non-empty summary")
	}
	if len([]rune(got)) > 201 {
		t.Fatalf("expected truncated: %d", len([]rune(got)))
	}
}

func TestIsToolResultFailure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result any
		want   bool
	}{
		{"nil", nil, false},
		{"ok_true", map[string]any{"ok": true}, false},
		{"ok_false", map[string]any{"ok": false}, true},
		{"error_present", map[string]any{"error": "bad"}, true},
		{"empty_error", map[string]any{"error": ""}, false},
		{"exit_zero", map[string]any{"exit_code": 0}, false},
		{"exit_nonzero", map[string]any{"exit_code": 2}, true},
		{"plain_string", "hello", false},
	}
	for _, tc := range cases {
		if got := isToolResultFailure(tc.result); got != tc.want {
			t.Fatalf("%s: isToolResultFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}
