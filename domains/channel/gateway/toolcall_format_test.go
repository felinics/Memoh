package gateway

import (
	"reflect"
	"strings"
	"testing"
)

func TestToolCallEmojiBuiltin(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"read":           "📖",
		"WRITE":          "📝",
		"  edit  ":       "📝",
		"exec":           "💻",
		"web_search":     "🌐",
		"search_memory":  "🧠",
		"list_schedule":  "📅",
		"send":           "💬",
		"get_contacts":   "👥",
		"send_email":     "📧",
		"spawn_agent":    "🤖",
		"send_message":   "🤖",
		"wait":           "⏱️",
		"wait_until":     "⏱️",
		"list_agents":    "🤖",
		"use_skill":      "🧩",
		"generate_image": "🖼️",
		"speak":          "🔊",
	}

	for name, want := range cases {
		if got := ToolCallEmoji(name); got != want {
			t.Fatalf("ToolCallEmoji(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestToolCallEmojiExternalFallback(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "   ", "mcp.filesystem.read", "federation_foo", "unknown_tool"} {
		if got := ToolCallEmoji(name); got != ExternalToolCallEmoji {
			t.Fatalf("ToolCallEmoji(%q) = %q, want external %q", name, got, ExternalToolCallEmoji)
		}
	}
}

func TestBuildToolCallStartPopulatesRunning(t *testing.T) {
	t.Parallel()

	tc := &StreamToolCall{
		Name:   "read",
		CallID: "call_1",
		Input:  map[string]any{"path": "/tmp/foo.txt"},
	}
	p := BuildToolCallStart(tc)
	if p.Status != ToolCallStatusRunning {
		t.Fatalf("unexpected status: %q", p.Status)
	}
	if p.Emoji != "📖" {
		t.Fatalf("unexpected emoji: %q", p.Emoji)
	}
	if p.Header != "/tmp/foo.txt" {
		t.Fatalf("unexpected header: %q", p.Header)
	}
	if p.ResultSummary != "" {
		t.Fatalf("start presentation should not carry a result summary, got %q", p.ResultSummary)
	}
	if p.Footer != "" {
		t.Fatalf("start presentation should not carry a footer, got %q", p.Footer)
	}
}

func TestBuildToolCallEndInfersStatus(t *testing.T) {
	t.Parallel()

	ok := &StreamToolCall{Name: "exec", Input: map[string]any{"command": "ls -la"}, Result: map[string]any{"ok": true, "exit_code": 0}}
	if got := BuildToolCallEnd(ok); got.Status != ToolCallStatusCompleted {
		t.Fatalf("expected completed, got %q", got.Status)
	}

	fail := &StreamToolCall{Name: "exec", Input: map[string]any{"command": "false"}, Result: map[string]any{"exit_code": 2, "stderr": "boom"}}
	if got := BuildToolCallEnd(fail); got.Status != ToolCallStatusFailed {
		t.Fatalf("expected failed, got %q", got.Status)
	}

	errored := &StreamToolCall{Name: "read", Input: map[string]any{"path": "/missing"}, Result: map[string]any{"error": "ENOENT"}}
	if got := BuildToolCallEnd(errored); got.Status != ToolCallStatusFailed {
		t.Fatalf("expected failed on error, got %q", got.Status)
	}
}

func TestBuildToolCallHandlesNil(t *testing.T) {
	t.Parallel()

	if got := BuildToolCallStart(nil); !reflect.DeepEqual(got, ToolCallPresentation{}) {
		t.Fatalf("expected zero-value presentation for nil start, got %+v", got)
	}
	if got := BuildToolCallEnd(nil); !reflect.DeepEqual(got, ToolCallPresentation{}) {
		t.Fatalf("expected zero-value presentation for nil end, got %+v", got)
	}
}

func TestRenderToolCallMessageLayout(t *testing.T) {
	t.Parallel()

	msg := RenderToolCallMessage(ToolCallPresentation{
		Emoji:         "📖",
		ToolName:      "read",
		Status:        ToolCallStatusRunning,
		InputSummary:  "/tmp/foo.txt",
		ResultSummary: "",
	})
	if !strings.HasPrefix(msg, "📖 read · running") {
		t.Fatalf("unexpected header: %q", msg)
	}
	if !strings.Contains(msg, "/tmp/foo.txt") {
		t.Fatalf("expected input summary in body: %q", msg)
	}

	done := RenderToolCallMessage(ToolCallPresentation{
		Emoji:         "💻",
		ToolName:      "exec",
		Status:        ToolCallStatusCompleted,
		InputSummary:  "ls -la",
		ResultSummary: "exit=0 · stdout: total 0",
	})
	lines := strings.Split(done, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header+input+result lines, got %d: %q", len(lines), done)
	}
	if !strings.HasPrefix(lines[0], "💻 exec · completed") {
		t.Fatalf("unexpected header: %q", lines[0])
	}
}

func TestRenderToolCallMessageEmptyWhenNothingKnown(t *testing.T) {
	t.Parallel()

	if got := RenderToolCallMessage(ToolCallPresentation{}); got != "" {
		t.Fatalf("expected empty render, got %q", got)
	}
}

func TestRenderToolCallMessageMarkdownRendersLinks(t *testing.T) {
	t.Parallel()

	p := ToolCallPresentation{
		Emoji:    "🌐",
		ToolName: "web_search",
		Status:   ToolCallStatusCompleted,
		Header:   `2 results for "golang generics"`,
		Body: []ToolCallBlock{
			{
				Type:  ToolCallBlockLink,
				Title: "Tutorial: Getting started with generics",
				URL:   "https://go.dev/doc/tutorial/generics",
				Desc:  "A comprehensive walkthrough",
			},
			{
				Type:  ToolCallBlockLink,
				Title: "Go 1.18 is released",
				URL:   "https://go.dev/blog/go1.18",
			},
		},
	}

	md := RenderToolCallMessageMarkdown(p)
	if !strings.Contains(md, "[Tutorial: Getting started with generics](https://go.dev/doc/tutorial/generics)") {
		t.Fatalf("expected markdown link for first item, got %q", md)
	}
	if !strings.Contains(md, "[Go 1.18 is released](https://go.dev/blog/go1.18)") {
		t.Fatalf("expected markdown link for second item, got %q", md)
	}
	if !strings.Contains(md, "A comprehensive walkthrough") {
		t.Fatalf("expected description to appear in markdown, got %q", md)
	}

	plain := RenderToolCallMessage(p)
	if strings.Contains(plain, "](https://") {
		t.Fatalf("plain render should not contain markdown link syntax, got %q", plain)
	}
	if !strings.Contains(plain, "Tutorial: Getting started with generics") || !strings.Contains(plain, "https://go.dev/doc/tutorial/generics") {
		t.Fatalf("plain render should carry title and url lines, got %q", plain)
	}
}

func TestRenderToolCallMessageMarkdownCodeBlocks(t *testing.T) {
	t.Parallel()

	p := ToolCallPresentation{
		Emoji:    "💻",
		ToolName: "exec",
		Status:   ToolCallStatusCompleted,
		Header:   "$ ls -la",
		Body: []ToolCallBlock{
			{Type: ToolCallBlockCode, Text: "total 0\ndrwxr-xr-x 2 user"},
		},
		Footer: "exit=0",
	}

	md := RenderToolCallMessageMarkdown(p)
	if !strings.Contains(md, "```\ntotal 0\ndrwxr-xr-x 2 user\n```") {
		t.Fatalf("expected fenced code block, got %q", md)
	}

	plain := RenderToolCallMessage(p)
	if strings.Contains(plain, "```") {
		t.Fatalf("plain render should not fence code, got %q", plain)
	}
	if !strings.Contains(plain, "total 0") {
		t.Fatalf("plain render should still include code body, got %q", plain)
	}
}
