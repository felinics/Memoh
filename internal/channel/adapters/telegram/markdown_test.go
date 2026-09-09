package telegram

import (
	"strings"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
)

func TestMarkdownToTelegramHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		absent   []string
	}{
		{
			name:     "bold",
			input:    "this is **bold** text",
			contains: []string{"<b>bold</b>"},
			absent:   []string{"**"},
		},
		{
			name:     "backticked model id with special chars renders as safe code",
			input:    "Current model: `deepseek/deepseek-v4-flash:free_x` and `a<b*c`",
			contains: []string{"<code>deepseek/deepseek-v4-flash:free_x</code>", "<code>a&lt;b*c</code>"},
			absent:   []string{"<i>", "<b>", "&lt;b&gt;"},
		},
		{
			name:     "italic",
			input:    "this is *italic* text",
			contains: []string{"<i>italic</i>"},
		},
		{
			name:     "bold and italic",
			input:    "**bold** and *italic*",
			contains: []string{"<b>bold</b>", "<i>italic</i>"},
		},
		{
			name:     "strikethrough",
			input:    "this is ~~deleted~~ text",
			contains: []string{"<s>deleted</s>"},
			absent:   []string{"~~"},
		},
		{
			name:     "inline code",
			input:    "use `fmt.Println` here",
			contains: []string{"<code>fmt.Println</code>"},
			absent:   []string{"`fmt.Println`"},
		},
		{
			name:     "link",
			input:    "visit [Google](https://google.com)",
			contains: []string{`<a href="https://google.com">Google</a>`},
		},
		{
			name:     "heading",
			input:    "# Title\nsome text",
			contains: []string{"<b>Title</b>"},
			absent:   []string{"# Title"},
		},
		{
			name:     "unordered list",
			input:    "- first\n- second\n+ third",
			contains: []string{"• first", "• second", "• third"},
		},
		{
			name:     "fenced code block",
			input:    "```go\nfmt.Println(\"hello\")\n```",
			contains: []string{`<pre><code class="language-go">`, "fmt.Println", "</code></pre>"},
		},
		{
			name:     "fenced code block without language",
			input:    "```\nplain code\n```",
			contains: []string{"<pre>plain code</pre>"},
		},
		{
			name:     "html entities escaped",
			input:    "a < b && c > d",
			contains: []string{"&lt;", "&amp;&amp;", "&gt;"},
			absent:   []string{"< b", "> d"},
		},
		{
			name:     "code block preserves content",
			input:    "```\n**not bold** <tag>\n```",
			contains: []string{"**not bold**", "&lt;tag&gt;"},
			absent:   []string{"<b>", "<tag>"},
		},
		{
			name:     "inline code preserves content",
			input:    "use `**not bold**` inline",
			contains: []string{"<code>**not bold**</code>"},
			absent:   []string{"<b>"},
		},
		{
			name:     "blockquote",
			input:    "> quoted line\n> another line",
			contains: []string{"<blockquote>"},
		},
		{
			name:     "empty input",
			input:    "",
			contains: nil,
		},
		{
			name:     "plain text no conversion",
			input:    "just plain text here",
			contains: []string{"just plain text here"},
		},
		{
			name:     "link with ampersand in url",
			input:    "[search](https://example.com?a=1&b=2)",
			contains: []string{`<a href="https://example.com?a=1&amp;b=2">search</a>`},
		},
		{
			name:     "bold inside link",
			input:    "**[click here](https://example.com)**",
			contains: []string{"<b>", `<a href="https://example.com">click here</a>`, "</b>"},
		},
		{
			name:     "mixed formatting",
			input:    "# Summary\n\n**Hello** world, visit [docs](https://docs.io).\n\n- item one\n- item two\n\n```python\nprint(\"hi\")\n```",
			contains: []string{"<b>Summary</b>", "<b>Hello</b>", `<a href="https://docs.io">docs</a>`, "• item one", `class="language-python"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := markdownToTelegramHTML(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(result, want) {
					t.Errorf("expected result to contain %q, got:\n%s", want, result)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(result, absent) {
					t.Errorf("expected result NOT to contain %q, got:\n%s", absent, result)
				}
			}
		})
	}
}

func TestFormatTelegramOutput(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		format       channel.MessageFormat
		wantMode     string
		wantContains string
	}{
		{
			name:         "markdown format returns html mode",
			text:         "**bold**",
			format:       channel.MessageFormatMarkdown,
			wantMode:     tele.ModeHTML,
			wantContains: "<b>bold</b>",
		},
		{
			name:     "plain format returns empty mode",
			text:     "hello",
			format:   channel.MessageFormatPlain,
			wantMode: "",
		},
		{
			name:     "empty text returns empty mode",
			text:     "",
			format:   channel.MessageFormatMarkdown,
			wantMode: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, mode := formatTelegramOutput(tt.text, tt.format)
			if mode != tt.wantMode {
				t.Errorf("expected mode %q, got %q", tt.wantMode, mode)
			}
			if tt.wantContains != "" && !strings.Contains(text, tt.wantContains) {
				t.Errorf("expected text to contain %q, got %q", tt.wantContains, text)
			}
		})
	}
}

func TestSplitCodeBlocks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // expected number of segments
	}{
		{name: "no code blocks", input: "hello world", want: 1},
		{name: "one code block", input: "before```code```after", want: 3},
		{name: "two code blocks", input: "a```b```c```d```e", want: 5},
		{name: "unclosed code block", input: "before```unclosed", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := splitCodeBlocks(tt.input)
			if len(segments) != tt.want {
				t.Errorf("expected %d segments, got %d: %v", tt.want, len(segments), segments)
			}
		})
	}
}

func TestTelegramEscapeHTML(t *testing.T) {
	input := `a < b & c > d`
	result := telegramEscapeHTML(input)
	if result != "a &lt; b &amp; c &gt; d" {
		t.Errorf("unexpected escape result: %s", result)
	}
}
