package gateway

import (
	"strings"
	"testing"
)

func TestRenderPartsAsMarkdown(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		parts    []MessagePart
		want     string
		excludes []string
	}{
		{
			name: "plain text",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "hello"},
			},
			want: "hello",
		},
		{
			name: "bold and italic",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "bold", Styles: []MessageTextStyle{MessageStyleBold}},
				{Type: MessagePartText, Text: "italic", Styles: []MessageTextStyle{MessageStyleItalic}},
			},
			want: "**bold**\n\n*italic*",
		},
		{
			name: "strikethrough",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "old", Styles: []MessageTextStyle{MessageStyleStrikethrough}},
			},
			want: "~~old~~",
		},
		{
			name: "underline degrades to visible text",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "under", Styles: []MessageTextStyle{MessageStyleUnderline}},
			},
			want:     "under",
			excludes: []string{"__", "<u>"},
		},
		{
			name: "spoiler degrades to visible text",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "secret", Styles: []MessageTextStyle{MessageStyleSpoiler}},
			},
			want:     "secret",
			excludes: []string{"||", "spoiler"},
		},
		{
			name: "inline code wins over other styles",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "x.y", Styles: []MessageTextStyle{MessageStyleCode, MessageStyleBold}},
			},
			want:     "`x.y`",
			excludes: []string{"**"},
		},
		{
			name: "link masked syntax",
			parts: []MessagePart{
				{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
			},
			want: "[docs](https://example.test)",
		},
		{
			name: "link without text uses url bare",
			parts: []MessagePart{
				{Type: MessagePartLink, URL: "https://example.test"},
			},
			want: "https://example.test",
		},
		{
			name: "link with disallowed scheme falls back to text",
			parts: []MessagePart{
				{Type: MessagePartLink, Text: "evil", URL: "javascript:alert(1)"},
			},
			want:     "evil",
			excludes: []string{"javascript:", "["},
		},
		{
			name: "code block with language",
			parts: []MessagePart{
				{Type: MessagePartCodeBlock, Text: "print(1)", Language: "python"},
			},
			want: "```python\nprint(1)\n```",
		},
		{
			name: "code block allows csharp language",
			parts: []MessagePart{
				{Type: MessagePartCodeBlock, Text: "Console.WriteLine(1);", Language: "c#"},
			},
			want: "```c#\nConsole.WriteLine(1);\n```",
		},
		{
			name: "code block fence expands for inner backticks",
			parts: []MessagePart{
				{Type: MessagePartCodeBlock, Text: "outer ``` end"},
			},
			want: "````\nouter ``` end\n````",
		},
		{
			name: "inline text neutralises link injection",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "click [evil](https://evil.test)"},
			},
			want:     `click \[evil\](https://evil.test)`,
			excludes: []string{"[evil]("},
		},
		{
			name: "styled inline cannot break wrapper",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "x**y", Styles: []MessageTextStyle{MessageStyleBold}},
			},
			want: `**x\*\*y**`,
		},
		{
			name: "link url paren and angle bracket are encoded",
			parts: []MessagePart{
				{Type: MessagePartLink, Text: "wiki", URL: "https://example.test/<a>(b)"},
			},
			want: "[wiki](https://example.test/%3Ca%3E%28b%29)",
		},
		{
			name: "mention emits text only",
			parts: []MessagePart{
				{Type: MessagePartMention, Text: "@alice"},
			},
			want: "@alice",
		},
		{
			name: "emoji uses Emoji field when Text empty",
			parts: []MessagePart{
				{Type: MessagePartEmoji, Emoji: "🎉"},
			},
			want: "🎉",
		},
		{
			name: "heading emits markdown heading",
			parts: []MessagePart{
				{Type: MessagePartHeading, Text: "Title [x]"},
			},
			want: `## Title \[x\]`,
		},
		{
			name: "blockquote quotes each line",
			parts: []MessagePart{
				{Type: MessagePartBlockquote, Text: "alpha [x]\nbeta"},
			},
			want: "> alpha \\[x\\]\n> beta",
		},
		{
			name: "list item emits bullet line",
			parts: []MessagePart{
				{Type: MessagePartListItem, Text: "item [x]"},
			},
			want: `- item \[x\]`,
		},
		{
			name:  "empty parts returns empty",
			parts: nil,
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RenderPartsAsMarkdown(tc.parts)
			if got != tc.want {
				t.Errorf("RenderPartsAsMarkdown()\n  got:  %q\n  want: %q", got, tc.want)
			}
			for _, no := range tc.excludes {
				if strings.Contains(got, no) {
					t.Errorf("expected %q to NOT contain %q", got, no)
				}
			}
		})
	}
}

func TestRenderPartsAsPlain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		parts []MessagePart
		want  string
	}{
		{
			name: "plain text",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "hello"},
			},
			want: "hello",
		},
		{
			name: "drops styles",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "title", Styles: []MessageTextStyle{MessageStyleBold}},
			},
			want: "title",
		},
		{
			name: "drops underline and spoiler styles",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "under", Styles: []MessageTextStyle{MessageStyleUnderline}},
				{Type: MessagePartText, Text: "secret", Styles: []MessageTextStyle{MessageStyleSpoiler}},
			},
			want: "under\n\nsecret",
		},
		{
			name: "link emits text + url in parens",
			parts: []MessagePart{
				{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
			},
			want: "docs (https://example.test)",
		},
		{
			name: "link without text emits bare url",
			parts: []MessagePart{
				{Type: MessagePartLink, URL: "https://example.test"},
			},
			want: "https://example.test",
		},
		{
			name: "code block drops fence",
			parts: []MessagePart{
				{Type: MessagePartCodeBlock, Text: "print(1)", Language: "python"},
			},
			want: "print(1)",
		},
		{
			name: "mixed parts join with blank line",
			parts: []MessagePart{
				{Type: MessagePartText, Text: "title", Styles: []MessageTextStyle{MessageStyleBold}},
				{Type: MessagePartCodeBlock, Text: "go test"},
				{Type: MessagePartLink, Text: "see", URL: "https://example.test"},
			},
			want: "title\n\ngo test\n\nsee (https://example.test)",
		},
		{
			name: "block parts preserve readable structure",
			parts: []MessagePart{
				{Type: MessagePartHeading, Text: "Title"},
				{Type: MessagePartBlockquote, Text: "quoted\nsecond"},
				{Type: MessagePartListItem, Text: "item"},
			},
			want: "Title\n\n> quoted\n> second\n\n- item",
		},
		{
			name:  "empty parts returns empty",
			parts: nil,
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RenderPartsAsPlain(tc.parts)
			if got != tc.want {
				t.Errorf("RenderPartsAsPlain()\n  got:  %q\n  want: %q", got, tc.want)
			}
		})
	}
}

func TestMessagePlainTextUsesCanonicalPlainParts(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Parts: []MessagePart{
			{Type: MessagePartHeading, Text: "Title"},
			{Type: MessagePartBlockquote, Text: "quoted\nsecond"},
			{Type: MessagePartListItem, Text: "item"},
			{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	want := "Title\n\n> quoted\n> second\n\n- item\n\ndocs (https://example.test)"
	if got := msg.PlainText(); got != want {
		t.Fatalf("PlainText()\n  got:  %q\n  want: %q", got, want)
	}
}

func TestMessagePlainTextCombinesTextAndParts(t *testing.T) {
	t.Parallel()

	msg := Message{
		Text: "intro",
		Parts: []MessagePart{
			{Type: MessagePartHeading, Text: "Title"},
			{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	want := "intro\n\nTitle\n\ndocs (https://example.test)"
	if got := msg.PlainText(); got != want {
		t.Fatalf("PlainText()\n  got:  %q\n  want: %q", got, want)
	}
}

func TestCoerceFormatForCaps_DegradesPartsToMarkdown(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Parts: []MessagePart{
			{Type: MessagePartText, Text: "title", Styles: []MessageTextStyle{MessageStyleBold}},
			{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	mdOnlyCaps := ChannelCapabilities{Text: true, Markdown: true}

	got := coerceFormatForCaps(msg, mdOnlyCaps)
	if got.Format != MessageFormatMarkdown {
		t.Errorf("expected MessageFormatMarkdown, got %q", got.Format)
	}
	if len(got.Parts) != 0 {
		t.Errorf("expected Parts cleared, got %d", len(got.Parts))
	}
	want := "**title**\n\n[docs](https://example.test)"
	if got.Text != want {
		t.Errorf("Text mismatch\n  got:  %q\n  want: %q", got.Text, want)
	}
}

func TestCoerceFormatForCaps_DegradesPartsToPlain(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Parts: []MessagePart{
			{Type: MessagePartText, Text: "title", Styles: []MessageTextStyle{MessageStyleBold}},
			{Type: MessagePartLink, Text: "docs", URL: "https://example.test"},
		},
	}
	plainCaps := ChannelCapabilities{Text: true}

	got := coerceFormatForCaps(msg, plainCaps)
	if got.Format != MessageFormatPlain {
		t.Errorf("expected MessageFormatPlain, got %q", got.Format)
	}
	if len(got.Parts) != 0 {
		t.Errorf("expected Parts cleared, got %d", len(got.Parts))
	}
	want := "title\n\ndocs (https://example.test)"
	if got.Text != want {
		t.Errorf("Text mismatch\n  got:  %q\n  want: %q", got.Text, want)
	}
}

func TestCoerceFormatForCaps_PreservesPartsOnRichChannel(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Parts: []MessagePart{
			{Type: MessagePartText, Text: "title", Styles: []MessageTextStyle{MessageStyleBold}},
		},
	}
	richCaps := ChannelCapabilities{Text: true, Markdown: true, RichText: true}

	got := coerceFormatForCaps(msg, richCaps)
	if got.Format != MessageFormatRich {
		t.Errorf("expected MessageFormatRich preserved, got %q", got.Format)
	}
	if len(got.Parts) != 1 {
		t.Errorf("expected Parts preserved, got %d", len(got.Parts))
	}
}

func TestCoerceFormatForCaps_MarkdownBodyBecomesTextPartOnRichOnlyChannel(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatMarkdown,
		Text:   `Hello [docs](https://example.test) <at user_id="all"></at> **bold**`,
	}
	richOnlyCaps := ChannelCapabilities{Text: true, RichText: true}

	got := coerceFormatForCaps(msg, richOnlyCaps)
	if got.Format != MessageFormatRich {
		t.Fatalf("Format = %q, want rich", got.Format)
	}
	if got.Text != "" {
		t.Fatalf("Text should be cleared after rich conversion, got %q", got.Text)
	}
	if len(got.Parts) != 1 {
		t.Fatalf("Parts len = %d, want 1", len(got.Parts))
	}
	if got.Parts[0].Type != MessagePartText || got.Parts[0].Text != msg.Text {
		t.Fatalf("unexpected text part: %#v", got.Parts[0])
	}
}

func TestCoerceFormatForCaps_DegradesURLActionsToMarkdownLinks(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatMarkdown,
		Text:   "see details",
		Actions: []Action{
			{Label: "Open docs", URL: "https://example.test/docs"},
		},
	}
	caps := ChannelCapabilities{Text: true, Markdown: true}

	got := coerceFormatForCaps(msg, caps)
	if len(got.Actions) != 0 {
		t.Fatalf("Actions should be cleared after URL degradation, got %#v", got.Actions)
	}
	if got.Format != MessageFormatMarkdown {
		t.Fatalf("Format = %q, want markdown", got.Format)
	}
	if got.Text != "see details\n\n[Open docs](https://example.test/docs)" {
		t.Fatalf("unexpected text: %q", got.Text)
	}
}

func TestCoerceFormatForCaps_DegradesURLActionsToRichPartsKeepsText(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Text:   "see details",
		Actions: []Action{
			{Label: "Open docs", URL: "https://example.test/docs"},
		},
	}
	caps := ChannelCapabilities{Text: true, RichText: true}

	got := coerceFormatForCaps(msg, caps)
	if len(got.Actions) != 0 {
		t.Fatalf("Actions should be cleared after URL degradation, got %#v", got.Actions)
	}
	if got.Text != "" {
		t.Fatalf("Text should be moved into parts on rich degradation, got %q", got.Text)
	}
	if got.Format != MessageFormatRich {
		t.Fatalf("Format = %q, want rich", got.Format)
	}
	if len(got.Parts) != 2 {
		t.Fatalf("Parts len = %d, want 2: %#v", len(got.Parts), got.Parts)
	}
	if got.Parts[0].Type != MessagePartText || got.Parts[0].Text != "see details" {
		t.Fatalf("first part should preserve original text, got %#v", got.Parts[0])
	}
	if got.Parts[1].Type != MessagePartLink || got.Parts[1].Text != "Open docs" || got.Parts[1].URL != "https://example.test/docs" {
		t.Fatalf("second part should be degraded URL action link, got %#v", got.Parts[1])
	}
}

func TestCoerceFormatForCaps_TextOnlyRichWithURLActionDegradesToPlain(t *testing.T) {
	t.Parallel()

	msg := Message{
		Format: MessageFormatRich,
		Text:   "see details",
		Actions: []Action{
			{Label: "Open docs", URL: "https://example.test/docs"},
		},
	}
	caps := ChannelCapabilities{Text: true}

	got := coerceFormatForCaps(msg, caps)
	if got.Format != MessageFormatPlain {
		t.Fatalf("Format = %q, want plain", got.Format)
	}
	if got.Text != "see details\n\nOpen docs (https://example.test/docs)" {
		t.Fatalf("unexpected text: %q", got.Text)
	}
	if len(got.Parts) != 0 || len(got.Actions) != 0 {
		t.Fatalf("expected parts/actions cleared, got parts=%#v actions=%#v", got.Parts, got.Actions)
	}
}

func TestCoerceFormatForCaps_KeepsCallbackActionsUnsupported(t *testing.T) {
	t.Parallel()

	msg := Message{
		Text: "choose",
		Actions: []Action{
			{Label: "Approve", Value: "approve:1"},
		},
	}
	caps := ChannelCapabilities{Text: true, Markdown: true}

	got := coerceFormatForCaps(msg, caps)
	if len(got.Actions) != 1 || got.Actions[0].Value != "approve:1" {
		t.Fatalf("callback action should remain unsupported, got %#v", got.Actions)
	}
}
