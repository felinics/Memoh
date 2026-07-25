package command

import (
	"testing"
)

func TestParse_Basic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		resource string
		action   string
		args     []string
	}{
		{"/help", "help", "", nil},
		{"/help model", "help", "model", nil},
		{"/help model set", "help", "model", []string{"set"}},
		{"/subagent list", "subagent", "list", nil},
		{"/subagent get mybot", "subagent", "get", []string{"mybot"}},
		{"/schedule create daily \"0 9 * * *\" Send report", "schedule", "create", []string{"daily", "0 9 * * *", "Send", "report"}},
		{"  /settings  ", "settings", "", nil},
		{"/HELP", "help", "", nil},
		{"/Schedule List", "schedule", "list", nil},
		{"/help@MemohBot", "help", "", nil},
		{"/schedule@BotName list", "schedule", "list", nil},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			parsed, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if parsed.Resource != tt.resource {
				t.Errorf("resource: got %q, want %q", parsed.Resource, tt.resource)
			}
			if parsed.Action != tt.action {
				t.Errorf("action: got %q, want %q", parsed.Action, tt.action)
			}
			if len(parsed.Args) != len(tt.args) {
				t.Fatalf("args length: got %d, want %d", len(parsed.Args), len(tt.args))
			}
			for i, arg := range tt.args {
				if parsed.Args[i] != arg {
					t.Errorf("arg[%d]: got %q, want %q", i, parsed.Args[i], arg)
				}
			}
		})
	}
}

func TestExtractCommandText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"/help", "/help"},
		{" /subagent list", "/subagent list"},
		{"@BotName /help", "/help"},
		{"@_user_1 /schedule list arg1", "/schedule list arg1"},
		{"<@123456> /mcp list", "/mcp list"},
		{"@bot hello", ""},
		{"hello world", ""},
		{"", ""},
		{"some text with no slash", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := ExtractCommandText(tt.input)
			if got != tt.want {
				t.Errorf("ExtractCommandText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()
	tests := []string{
		"",
		"hello",
		"no slash",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(input)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestTokenize_Quotes(t *testing.T) {
	t.Parallel()
	tokens := tokenize(`create daily "0 9 * * *" 'Send report now'`)
	expected := []string{"create", "daily", "0 9 * * *", "Send report now"}
	if len(tokens) != len(expected) {
		t.Fatalf("tokens length: got %d, want %d (%v)", len(tokens), len(expected), tokens)
	}
	for i, tok := range expected {
		if tokens[i] != tok {
			t.Errorf("token[%d]: got %q, want %q", i, tokens[i], tok)
		}
	}
}

func TestParse_Flags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input            string
		resource, action string
		args             []string
		page, prov       int
		selectID         string
		rangeKey         string
	}{
		{"/mcp list", "mcp", "list", nil, 0, -1, "", ""},
		{"/mcp list --page 3", "mcp", "list", nil, 3, -1, "", ""},
		{"/model list --prov 2 --page 1", "model", "list", nil, 1, 2, "", ""},
		{"/model set --id 1f2e3d4c-5b6a-7980-1234-56789abcdef0", "model", "set", nil, 0, -1, "1f2e3d4c-5b6a-7980-1234-56789abcdef0", ""},
		{"/model list openrouter --page 2", "model", "list", []string{"openrouter"}, 2, -1, "", ""},
		{"/model list --page 2 openrouter", "model", "list", []string{"openrouter"}, 2, -1, "", ""},
		{"/usage summary --range 30d", "usage", "summary", nil, 0, -1, "", "30d"},
		{"/usage --range all", "usage", "", nil, 0, -1, "", "all"},
		{"/schedule create daily \"0 9 * * *\" /usage summary --range 7d", "schedule", "create", []string{"daily", "0 9 * * *", "/usage", "summary", "--range", "7d"}, 0, -1, "", ""},
		{"/schedule update daily --command /usage summary --range 7d", "schedule", "update", []string{"daily", "--command", "/usage", "summary", "--range", "7d"}, 0, -1, "", ""},
		// Invalid int-flag values must not leak the flag name OR the value as
		// stray positional args (would pollute provider/name matching downstream).
		{"/model list --prov -1", "model", "list", nil, 0, -1, "", ""},
		{"/heartbeat logs --page -5", "heartbeat", "logs", nil, 0, -1, "", ""},
		{"/model list --prov abc", "model", "list", nil, 0, -1, "", ""},
		// A following --flag is not eaten as the prior flag's value.
		{"/model list --page --prov 2", "model", "list", nil, 0, 2, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			p, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Resource != tt.resource || p.Action != tt.action {
				t.Errorf("resource/action = %q/%q, want %q/%q", p.Resource, p.Action, tt.resource, tt.action)
			}
			if p.Page != tt.page || p.Prov != tt.prov {
				t.Errorf("page/prov = %d/%d, want %d/%d", p.Page, p.Prov, tt.page, tt.prov)
			}
			if p.SelectID != tt.selectID {
				t.Errorf("selectID = %q, want %q", p.SelectID, tt.selectID)
			}
			if p.Range != tt.rangeKey {
				t.Errorf("range = %q, want %q", p.Range, tt.rangeKey)
			}
			if len(p.Args) != len(tt.args) {
				t.Fatalf("args = %v, want %v", p.Args, tt.args)
			}
			for i := range tt.args {
				if p.Args[i] != tt.args[i] {
					t.Errorf("arg[%d] = %q, want %q", i, p.Args[i], tt.args[i])
				}
			}
		})
	}
}
