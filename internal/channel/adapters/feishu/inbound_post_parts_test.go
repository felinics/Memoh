package feishu

import (
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestExtractFeishuPostParts_StyleArrayIsRespected(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "shout", "style": []any{"bold", "italic"}},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 {
		t.Fatalf("got %+v", parts)
	}
	styles := parts[0].Styles
	if len(styles) != 2 || styles[0] != channel.MessageStyleBold || styles[1] != channel.MessageStyleItalic {
		t.Fatalf("expected bold+italic styles, got %+v", styles)
	}
}

func TestExtractFeishuPostParts_LineThroughMapsToStrike(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "gone", "style": []any{"lineThrough"}},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 || len(parts[0].Styles) != 1 || parts[0].Styles[0] != channel.MessageStyleStrikethrough {
		t.Fatalf("expected strikethrough, got %+v", parts)
	}
}

func TestExtractFeishuPostParts_LinkTag(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "a", "text": "Memoh", "href": "https://example.com"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 || parts[0].Type != channel.MessagePartLink {
		t.Fatalf("expected link part, got %+v", parts)
	}
	if parts[0].URL != "https://example.com" || parts[0].Text != "Memoh" {
		t.Fatalf("link content wrong: %+v", parts[0])
	}
}

func TestExtractFeishuPostParts_AtTag(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "at", "user_id": "ou_xyz", "text": "@Alice"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 || parts[0].Type != channel.MessagePartMention {
		t.Fatalf("expected mention, got %+v", parts)
	}
	if parts[0].ChannelIdentityID != "ou_xyz" || parts[0].Text != "@Alice" {
		t.Fatalf("mention content wrong: %+v", parts[0])
	}
}

func TestExtractFeishuPostParts_CodeBlock(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "code_block", "text": "fn main(){}", "language": "rust"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 || parts[0].Type != channel.MessagePartCodeBlock {
		t.Fatalf("expected code_block, got %+v", parts)
	}
	if parts[0].Language != "rust" || parts[0].Text != "fn main(){}" {
		t.Fatalf("code_block content wrong: %+v", parts[0])
	}
}

func TestExtractFeishuPostParts_MixedLineBreaksWithNewline(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "line1"},
			},
			[]any{
				map[string]any{"tag": "text", "text": "line2"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts (text, newline, text), got %+v", parts)
	}
	if parts[0].Text != "line1" {
		t.Fatalf("part 0 wrong: %+v", parts[0])
	}
	if parts[1].Type != channel.MessagePartText || parts[1].Text != "\n" {
		t.Fatalf("expected newline separator, got %+v", parts[1])
	}
	if parts[2].Text != "line2" {
		t.Fatalf("part 2 wrong: %+v", parts[2])
	}
}

func TestExtractFeishuPostParts_KeepsTextFromUnknownTag(t *testing.T) {
	t.Parallel()
	// When a styled element promotes the post to "rich", adaptBody picks Parts
	// over Text. Any text-bearing unknown tag must still surface its body or
	// the LLM loses that content entirely.
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "intro", "style": []any{"bold"}},
				map[string]any{"tag": "unknown_future_tag", "text": "important detail"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts (bold + unknown's text), got %+v", parts)
	}
	if parts[1].Type != channel.MessagePartText || parts[1].Text != "important detail" {
		t.Fatalf("expected unknown tag's text preserved, got %+v", parts[1])
	}
}

func TestExtractFeishuPostParts_ImagesAndFilesSkipped(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "see", "style": []any{"bold"}},
				map[string]any{"tag": "img", "image_key": "img_x"},
				map[string]any{"tag": "file", "file_key": "f_y"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if len(parts) != 1 || parts[0].Text != "see" || parts[0].Type != channel.MessagePartText {
		t.Fatalf("img/file tags should be skipped (extracted as attachments elsewhere), got %+v", parts)
	}
}

func TestExtractFeishuPostParts_NoContentReturnsNil(t *testing.T) {
	t.Parallel()
	if got := extractFeishuPostParts(nil); got != nil {
		t.Fatalf("expected nil for nil content, got %+v", got)
	}
	if got := extractFeishuPostParts(map[string]any{}); got != nil {
		t.Fatalf("expected nil for empty content, got %+v", got)
	}
}

func TestExtractFeishuPostParts_PlainOnlyReturnsNil(t *testing.T) {
	t.Parallel()
	content := map[string]any{
		"content": []any{
			[]any{
				map[string]any{"tag": "text", "text": "just text"},
			},
		},
	}
	parts := extractFeishuPostParts(content)
	if parts != nil {
		t.Fatalf("expected nil when only unstyled plain text (caller falls back to Text), got %+v", parts)
	}
}
