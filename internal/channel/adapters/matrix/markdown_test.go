package matrix

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestNormalizeMatrixMarkdownTaskList(t *testing.T) {
	input := "- [ ] todo\n- [x] done"
	got := normalizeMatrixMarkdown(input)
	if got != "- ☐ todo\n- ☑ done" {
		t.Fatalf("unexpected normalized markdown: %q", got)
	}
}

func TestNormalizeMatrixMarkdownTablesBecomeCodeBlocks(t *testing.T) {
	input := "| A | B |\n| --- | --- |\n| 1 | 2 |"
	got := normalizeMatrixMarkdown(input)
	if !strings.HasPrefix(got, "```text\n") || !strings.Contains(got, "| 1 | 2 |") || !strings.HasSuffix(got, "\n```") {
		t.Fatalf("unexpected normalized markdown: %q", got)
	}
}

func TestFormatMatrixMessageMarkdownUsesNormalizedBody(t *testing.T) {
	formatted := formatMatrixMessage(channel.Message{
		Text:   "- [x] done\n\n| A |\n| --- |\n| 1 |",
		Format: channel.MessageFormatMarkdown,
	})
	if !strings.Contains(formatted.Body, "☑ done") {
		t.Fatalf("expected task list checkbox in body, got %q", formatted.Body)
	}
	if !strings.Contains(formatted.FormattedBody, "<pre><code") || !strings.Contains(formatted.FormattedBody, "| A |") {
		t.Fatalf("expected table fallback code block in formatted body, got %q", formatted.FormattedBody)
	}
	if strings.Contains(formatted.FormattedBody, "<table") {
		t.Fatalf("expected no html table in formatted body, got %q", formatted.FormattedBody)
	}
}

func TestFormatMatrixMessagePartsRenderBlockPartsAndStyles(t *testing.T) {
	formatted := formatMatrixMessage(channel.Message{
		Format: channel.MessageFormatRich,
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartHeading, Text: "Title <x>"},
			{Type: channel.MessagePartBlockquote, Text: "alpha <x>\nbeta"},
			{Type: channel.MessagePartListItem, Text: "item <x>"},
			{Type: channel.MessagePartText, Text: "under", Styles: []channel.MessageTextStyle{channel.MessageStyleUnderline}},
			{Type: channel.MessagePartText, Text: "secret", Styles: []channel.MessageTextStyle{channel.MessageStyleSpoiler}},
		},
	})
	if formatted.Body != "Title <x>\n\n> alpha <x>\n> beta\n\n- item <x>\n\nunder\n\nsecret" {
		t.Fatalf("unexpected Matrix plain body: %q", formatted.Body)
	}
	for _, want := range []string{
		"<h2>Title &lt;x&gt;</h2>",
		"<blockquote>alpha &lt;x&gt;<br>beta</blockquote>",
		"<ul><li>item &lt;x&gt;</li></ul>",
		"<p><u>under</u></p>",
		`<p><span data-mx-spoiler>secret</span></p>`,
	} {
		if !strings.Contains(formatted.FormattedBody, want) {
			t.Fatalf("Matrix formatted body missing %q in %q", want, formatted.FormattedBody)
		}
	}
}

func TestFormatMatrixMessagePartsAllowCSharpLanguage(t *testing.T) {
	formatted := formatMatrixMessage(channel.Message{
		Format: channel.MessageFormatRich,
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartCodeBlock, Text: "Console.WriteLine(1);", Language: "c#"},
		},
	})
	if !strings.Contains(formatted.FormattedBody, `<pre><code class="language-c#">Console.WriteLine(1);</code></pre>`) {
		t.Fatalf("Matrix formatted body should keep c# language hint, got %q", formatted.FormattedBody)
	}
}
