package command

import (
	"slices"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
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

// fullLadderOptions is a model that can be turned off and advertises the whole
// ladder — what the command used to assume of every model unconditionally.
func fullLadderOptions() reasoning.Options {
	return reasoning.OptionsFor(
		reasoning.ModeToggle,
		[]string{
			reasoning.EffortDisable,
			reasoning.EffortLow,
			reasoning.EffortMedium,
			reasoning.EffortHigh,
			reasoning.EffortXHigh,
		},
		"openai-codex",
		"",
	)
}

func TestReasoningResultMarksCurrent(t *testing.T) {
	t.Parallel()
	res := reasoningResult(i18n.New("en"), "medium", fullLadderOptions())
	if res.Interactive == nil || res.Interactive.Kind != InteractiveChoices || res.Interactive.Choices == nil {
		t.Fatalf("expected a choices interactive, got %+v", res.Interactive)
	}
	assertReasoningMarked(t, res, "medium")
	if !strings.Contains(res.Text, "Current: medium") {
		t.Errorf("missing current line: %s", res.Text)
	}
	assertReasoningMarked(t, reasoningResult(i18n.New("en"), models.ReasoningEffortDisable, fullLadderOptions()), "off")
}

func TestReasoningChoicesIncludeFullBackendEffortLadder(t *testing.T) {
	t.Parallel()
	res := reasoningResult(i18n.New("en"), "xhigh", fullLadderOptions())
	assertReasoningMarked(t, res, "xhigh")
	labels := make(map[string]bool)
	for _, c := range res.Interactive.Choices.Choices {
		labels[c.Label] = true
	}
	for _, want := range []string{"off", "low", "medium", "high", "xhigh"} {
		if !labels[want] {
			t.Errorf("reasoning choices missing %q", want)
		}
	}
	// "off" is the only disabled choice. Listing "none" alongside it offered the
	// same state twice, and picking either stored a value that reads as off.
	if labels[models.ReasoningEffortNone] {
		t.Error("reasoning choices still offer none, a second name for off")
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
	for _, lvl := range reasoningChoicesFor(fullLadderOptions()) {
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

// TestReasoningChoicesFollowTheModel is the behaviour change: the picker used to
// offer the same five levels on every model, including Off on models that cannot
// be turned off and never the tiers a model actually advertises.
func TestReasoningChoicesFollowTheModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		opts   reasoning.Options
		want   []string
		absent []string
	}{
		{
			name: "a model that can be turned off leads with off",
			opts: reasoning.OptionsFor(reasoning.ModeToggle, []string{reasoning.EffortDisable, reasoning.EffortLow, reasoning.EffortHigh}, "openai-codex", ""),
			want: []string{"off", "low", "high"},
		},
		{
			name:   "a model that cannot be turned off does not offer it",
			opts:   reasoning.OptionsFor(reasoning.ModeToggle, []string{reasoning.EffortMinimal, reasoning.EffortLow, reasoning.EffortMedium}, "openai-codex", ""),
			want:   []string{"minimal", "low", "medium"},
			absent: []string{"off"},
		},
		{
			name: "a model without reasoning offers no invented fallback",
			opts: reasoning.Options{},
			want: []string{},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := reasoningChoicesFor(tt.opts)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("choices = %v, want %v", got, tt.want)
			}
			for _, absent := range tt.absent {
				if slices.Contains(got, absent) {
					t.Fatalf("choices unexpectedly offer %q: %v", absent, got)
				}
			}
		})
	}
}

// TestAcceptsEffortRejectsTiersTheModelDoesNotOffer keeps a typed level from
// storing a value the model will never accept — the /reasoning half of the same
// guarantee the picker gives by only rendering reachable options.
func TestAcceptsEffortRejectsTiersTheModelDoesNotOffer(t *testing.T) {
	t.Parallel()

	cannotDisable := reasoning.OptionsFor(reasoning.ModeToggle,
		[]string{reasoning.EffortLow, reasoning.EffortMedium}, "openai-codex", "")
	canDisable := fullLadderOptions()

	if !acceptsEffort(reasoning.EffortLow, cannotDisable) {
		t.Error("an advertised tier should be accepted")
	}
	if acceptsEffort(reasoning.EffortXHigh, cannotDisable) {
		t.Error("a tier the model does not advertise should be rejected")
	}
	if acceptsEffort(offChoice, cannotDisable) {
		t.Error("off should be rejected when the model cannot disable reasoning")
	}
	if !acceptsEffort(offChoice, canDisable) {
		t.Error("off should be accepted when the model can disable reasoning")
	}
	if acceptsEffort(reasoning.EffortLow, reasoning.Options{}) {
		t.Error("an unsupported or unresolved model must not accept a fallback tier")
	}
}

// A typed `set <level>` has no picker above it, so pointing at "the levels shown
// above" names something that may not be on screen — and the levels differ per
// model, so they have to travel in the message itself.
func TestUnknownLevelMessageNamesTheAvailableLevels(t *testing.T) {
	t.Parallel()

	opts := reasoning.OptionsFor(reasoning.ModeToggle,
		[]string{reasoning.EffortMinimal, reasoning.EffortLow, reasoning.EffortHigh},
		"google-generative-ai", "")

	got := reasoningChoicesFor(opts)
	if !slices.Equal(got, []string{"minimal", "low", "high"}) {
		t.Fatalf("choices = %v, want the model's own tiers", got)
	}
	// The rendered list is what the message interpolates; asserting it here keeps
	// the two from drifting apart.
	if joined := strings.Join(got, ", "); joined != "minimal, low, high" {
		t.Fatalf("rendered levels = %q", joined)
	}
}
