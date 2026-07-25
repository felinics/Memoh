package gateway

import "testing"

func TestContainsMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{"empty", "", false},
		{"plain", "hello world", false},
		{"bold", "this is **bold** text", true},
		{"italic", "this is *italic* text", true},
		{"code", "use `fmt.Println`", true},
		{"fenced_code", "```go\nfmt.Println()\n```", true},
		{"heading", "# Title", true},
		{"link", "[click](https://example.com)", true},
		{"unordered_list", "- item one", true},
		{"ordered_list", "1. first item", true},
		{"strikethrough", "this is ~~deleted~~ text", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ContainsMarkdown(tt.text)
			if got != tt.want {
				t.Errorf("ContainsMarkdown(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
