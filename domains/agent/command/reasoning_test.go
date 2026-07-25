package command

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/i18n"
)

func TestReasoningRegisteredWithAliases(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	if g, ok := h.registry.groups["reasoning"]; !ok || g.DefaultAction != "show" {
		t.Fatalf("/reasoning not registered with show default")
	}
	for _, alias := range []string{"/reasoning", "/reason", "/effort", "/think"} {
		if !h.IsCommand(alias) {
			t.Errorf("%s should be recognized (alias of reasoning)", alias)
		}
		if canonicalResource(strings.TrimPrefix(alias, "/")) != "reasoning" {
			t.Errorf("%s should canonicalize to reasoning", alias)
		}
	}
}

func TestReasoningResultMarksCurrent(t *testing.T) {
	t.Parallel()
	res := reasoningResult(i18n.New("en"), true, "medium")
	if res.Interactive == nil || res.Interactive.Kind != InteractiveChoices || res.Interactive.Choices == nil {
		t.Fatalf("expected a choices interactive, got %+v", res.Interactive)
	}
	assertReasoningMarked(t, res, "medium")
	if !strings.Contains(res.Text, "Current: medium") {
		t.Errorf("missing current line: %s", res.Text)
	}
	assertReasoningMarked(t, reasoningResult(i18n.New("en"), false, "high"), "off")
}

func TestReasoningChoicesIncludeFullBackendEffortLadder(t *testing.T) {
	t.Parallel()
	res := reasoningResult(i18n.New("en"), true, "xhigh")
	assertReasoningMarked(t, res, "xhigh")
	labels := make(map[string]bool)
	for _, c := range res.Interactive.Choices.Choices {
		labels[c.Label] = true
	}
	for _, want := range []string{"off", "none", "low", "medium", "high", "xhigh"} {
		if !labels[want] {
			t.Errorf("reasoning choices missing %q", want)
		}
	}
}

func assertReasoningMarked(t *testing.T, res *Result, want string) {
	t.Helper()
	var marked string
	for _, c := range res.Interactive.Choices.Choices {
		if c.Action == nil || c.Action.Resource != "reasoning" || c.Action.Action != "set" {
			t.Errorf("choice %q has bad action %+v", c.Label, c.Action)
		}
		if c.Selected {
			marked = c.Label
		}
	}
	if marked != want {
		t.Errorf("marked = %q, want %q", marked, want)
	}
}

// TestReasoningChoiceCallbackRoundTrip is the critical check: a tapped level
// button must re-parse to "/reasoning set <level>" so the tap actually applies.
func TestReasoningChoiceCallbackRoundTrip(t *testing.T) {
	t.Parallel()
	for _, lvl := range reasoningChoices {
		data := EncodeListCallback("reasoning", "set", []string{lvl}, 0)
		if len(data) > telegramCallbackLimit {
			t.Fatalf("callback %q exceeds limit", data)
		}
		parsed, ok := DecodeCallback(data)
		if !ok {
			t.Fatalf("decode %q failed", data)
		}
		reparsed, err := Parse(parsed.SyntheticCommand())
		if err != nil {
			t.Fatalf("Parse(%q): %v", parsed.SyntheticCommand(), err)
		}
		if reparsed.Resource != "reasoning" || reparsed.Action != "set" || len(reparsed.Args) != 1 || reparsed.Args[0] != lvl {
			t.Errorf("round-trip = %+v, want reasoning/set/[%s]", reparsed, lvl)
		}
	}
}

func TestUnknownCommandHandling(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	if !h.IsCommandShaped("/wat") || h.IsCommand("/wat") {
		t.Errorf("/wat should be shaped-but-unknown")
	}
	msg := UnknownCommandMessage(i18n.New("en"), "/wat")
	if !strings.Contains(msg, "/wat") || !strings.Contains(msg, "/commands") {
		t.Errorf("unknown message = %q", msg)
	}
	// Paths and bare slashes are not command-shaped.
	for _, p := range []string{"/path/to/file", "/", "/ "} {
		if h.IsCommandShaped(p) {
			t.Errorf("%q should not be command-shaped", p)
		}
	}
	// Known commands and aliases are recognized (so they aren't treated as unknown).
	for _, c := range []string{"/help", "/commands", "/start", "/setting", "/think", "/effort", "/reason", "/model", "/models"} {
		if !h.IsCommand(c) {
			t.Errorf("%s should be a known command", c)
		}
	}
}
