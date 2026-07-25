package port

import (
	"testing"
	"unicode/utf8"

	memorydomain "github.com/memohai/memoh/domains/memory"
)

func TestTruncateSnippet_ASCII(t *testing.T) {
	t.Parallel()
	got := TruncateSnippet("hello world", 5)
	if got != "hello..." {
		t.Fatalf("expected %q, got %q", "hello...", got)
	}
}

func TestTruncateSnippet_NoTruncation(t *testing.T) {
	t.Parallel()
	got := TruncateSnippet("short", 100)
	if got != "short" {
		t.Fatalf("expected %q, got %q", "short", got)
	}
}

func TestTruncateSnippet_CJK(t *testing.T) {
	t.Parallel()
	got := TruncateSnippet("你好世界啊", 3)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if got != "你好世..." {
		t.Fatalf("expected %q, got %q", "你好世...", got)
	}
}

func TestTruncateSnippet_Emoji(t *testing.T) {
	t.Parallel()
	got := TruncateSnippet("😀😁😂🤣😃", 2)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if got != "😀😁..." {
		t.Fatalf("expected %q, got %q", "😀😁...", got)
	}
}

func TestTruncateSnippet_TrimWhitespace(t *testing.T) {
	t.Parallel()
	got := TruncateSnippet("  hello  ", 100)
	if got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
}

func TestDeduplicateItems(t *testing.T) {
	t.Parallel()
	items := []memorydomain.Item{
		{ID: "a", Memory: "first"},
		{ID: "b", Memory: "second"},
		{ID: "a", Memory: "duplicate"},
	}
	result := DeduplicateItems(items)
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
}
