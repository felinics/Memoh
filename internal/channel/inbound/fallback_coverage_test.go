package inbound

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
)

// TestNoButtonFallbackCoverage is the regression guard for the no-button
// trailer architecture: every Interactive shape that ships to users must
// either (a) be opted out via BodyEnumeratesChoices, or (b) produce a typeable
// trailer that contains at least one slash command and no callback artifacts.
//
// The fixture covers every Interactive Kind plus every override flag. New
// command surfaces with an Interactive payload that don't fit these shapes
// should add a row here so the gate stays meaningful.
func TestNoButtonFallbackCoverage(t *testing.T) {
	noButton := channel.ChannelCapabilities{Text: true}

	tests := []struct {
		name            string
		result          *command.Result
		expectTrailer   bool     // when false, msg.Text must equal result.Text
		mustContain     []string // substrings the rendered text must contain when trailer expected
		mustNotContain  []string // substrings the rendered text must never contain
		allowEmptyChain bool     // true for BodyEnumeratesChoices/genuinely-empty surfaces
	}{
		{
			name: "list switch-shape (memory)",
			result: &command.Result{
				Text: "Memory (2)\n\n- alice\n- bob",
				Interactive: &command.Interactive{Kind: command.InteractiveList, List: &command.ListView{
					Resource: "memory", Action: "list",
					Items: []command.ListItem{
						{Label: "alice", Action: &command.ItemAction{Resource: "memory", Action: "set", Args: []string{"alice"}}},
						{Label: "bob", Action: &command.ItemAction{Resource: "memory", Action: "set", Args: []string{"bob"}}},
					},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/memory set <name>"},
		},
		{
			name: "list display-only with details override (mcp)",
			result: &command.Result{
				Text: "MCP Connections (1)\n\n- server-a",
				Interactive: &command.Interactive{Kind: command.InteractiveList, List: &command.ListView{
					Resource: "mcp", Action: "list",
					HintVerb: command.HintVerbDetails,
					Items:    []command.ListItem{{Label: "server-a"}},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/mcp get <name>"},
		},
		{
			name: "list display-only with cross-nav extras (email)",
			result: &command.Result{
				Text: "Email Providers",
				Interactive: &command.Interactive{Kind: command.InteractiveList, List: &command.ListView{
					Resource: "email", Action: "providers",
					Items: []command.ListItem{{Label: "smtp.gmail.com"}},
					ExtraActions: []command.ListItem{
						{Action: &command.ItemAction{Resource: "email", Action: "bindings"}},
						{Action: &command.ItemAction{Resource: "email", Action: "outbox"}},
					},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/email bindings", "/email outbox"},
		},
		{
			name: "list display-only no extras (schedule logs — genuinely empty)",
			result: &command.Result{
				Text: "Schedule logs",
				Interactive: &command.Interactive{Kind: command.InteractiveList, List: &command.ListView{
					Resource: "schedule", Action: "logs",
					Items: []command.ListItem{{Label: "10:00"}, {Label: "11:00"}},
				}},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
		{
			name: "choices pick-shape (reasoning)",
			result: &command.Result{
				Text: "🧠 Reasoning\nCurrent: xhigh",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
					Choices: []command.ListItem{
						{Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"off"}}},
						{Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"low"}}},
						{Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"high"}}},
					},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/reasoning set <off|low|high>"},
		},
		{
			name: "choices toggle-shape (boolean flags)",
			result: &command.Result{
				Text: "Settings",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
					Choices: []command.ListItem{
						{Action: &command.ItemAction{Resource: "settings", Action: "update", Args: []string{"--display_enabled", "true"}}},
						{Action: &command.ItemAction{Resource: "settings", Action: "update", Args: []string{"--display_enabled", "false"}}},
					},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/settings update --display_enabled true", "/settings update --display_enabled false"},
		},
		{
			name: "choices heterogeneous (settings worst case)",
			result: &command.Result{
				Text: "Settings card",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
					Choices: []command.ListItem{
						{Action: &command.ItemAction{Resource: "settings", Action: "update", Args: []string{"--display_enabled", "true"}}},
						{Action: &command.ItemAction{Resource: "reasoning", Action: "show"}},
						{Action: &command.ItemAction{Resource: "model", Action: "list"}},
						{Action: &command.ItemAction{Resource: "memory", Action: "list"}},
						{Action: &command.ItemAction{Resource: "search", Action: "list"}},
						{Action: &command.ItemAction{Resource: "settings", Action: "language"}},
					},
				}},
			},
			expectTrailer: true,
			mustContain: []string{
				"/settings update --display_enabled true",
				"/reasoning show",
				"/model list",
				"/memory list",
				"/search list",
				"/settings language",
			},
		},
		{
			name: "choices BodyEnumeratesChoices (/help group)",
			result: &command.Result{
				Text: "Already-exhaustive help block",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
					BodyEnumeratesChoices: true,
					Choices: []command.ListItem{
						{Action: &command.ItemAction{Resource: "schedule", Action: "list"}},
					},
				}},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
		{
			name: "choices single no-arg button (WithButtons empty state)",
			result: &command.Result{
				Text: "No MCP connections yet.",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
					Choices: []command.ListItem{
						{Action: &command.ItemAction{Resource: "help", Action: "mcp"}},
					},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/help mcp"},
		},
		{
			name: "model picker LevelProviders",
			result: &command.Result{
				Text: "Models",
				Interactive: &command.Interactive{Kind: command.InteractiveModelPicker, Picker: &command.ModelPickerView{
					Level:     command.LevelProviders,
					Providers: []command.PickerProvider{{Name: "DeepSeek", Count: 4}},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/model list <provider_name>"},
		},
		{
			name: "model picker LevelModels",
			result: &command.Result{
				Text: "Provider: openai",
				Interactive: &command.Interactive{Kind: command.InteractiveModelPicker, Picker: &command.ModelPickerView{
					Level:  command.LevelModels,
					Models: []command.PickerModel{{Name: "gpt-4o"}},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/model set <name>"},
		},
		{
			name: "range view (/usage summary)",
			result: &command.Result{
				Text: "Usage summary card",
				Interactive: &command.Interactive{Kind: command.InteractiveRange, Range: &command.RangeView{
					Resource: "usage", Action: "summary", Current: "7d",
					Presets: []string{"24h", "7d", "30d", "all"},
				}},
			},
			expectTrailer: true,
			mustContain:   []string{"/usage summary --range <preset>", "24h", "7d", "30d", "all"},
		},
		{
			// Defensive: an Interactive wrapper with Kind set but the matching
			// sub-view nil (e.g. partial Result from a panic-recover path) must
			// not drop the body text or panic. The renderer falls back to
			// Result.Text and the trailer-derivation skips silently.
			name: "mixed-shape Interactive (Kind=List but List=nil)",
			result: &command.Result{
				Text:        "Settings shown to fallback",
				Interactive: &command.Interactive{Kind: command.InteractiveList, List: nil},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
		{
			name: "mixed-shape Interactive (Kind=Choices but Choices=nil)",
			result: &command.Result{
				Text:        "Reasoning body",
				Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: nil},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
		{
			name: "mixed-shape Interactive (Kind=ModelPicker but Picker=nil)",
			result: &command.Result{
				Text:        "Model card body",
				Interactive: &command.Interactive{Kind: command.InteractiveModelPicker, Picker: nil},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
		{
			name: "mixed-shape Interactive (Kind=Range but Range=nil)",
			result: &command.Result{
				Text:        "Usage card body",
				Interactive: &command.Interactive{Kind: command.InteractiveRange, Range: nil},
			},
			expectTrailer:   false,
			allowEmptyChain: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := renderResult(tc.result, RenderContext{Caps: noButton, T: i18n.New("en")})

			// No keyboard ever leaks to no-button channels.
			if len(msg.Actions) != 0 {
				t.Errorf("no-button channel received %d actions, want 0", len(msg.Actions))
			}

			// Callback artifact must never appear in user-facing trailer text.
			if strings.Contains(msg.Text, "--page 0") {
				t.Errorf("trailer leaked --page 0 callback artifact: %q", msg.Text)
			}

			if !tc.expectTrailer {
				// BodyEnumeratesChoices / genuinely-empty: rendered text equals input,
				// minus the markup strip that applyMessageFormat performs.
				expected := channel.StripInlineMarkup(tc.result.Text)
				if msg.Text != expected {
					t.Errorf("expected no trailer (text unchanged), got %q want %q", msg.Text, expected)
				}
				return
			}

			// A typeable command must be present.
			if !strings.Contains(msg.Text, "/") {
				t.Errorf("trailer expected to carry a /command, got %q", msg.Text)
			}

			for _, sub := range tc.mustContain {
				if !strings.Contains(msg.Text, sub) {
					t.Errorf("trailer missing required substring %q in %q", sub, msg.Text)
				}
			}
			for _, sub := range tc.mustNotContain {
				if strings.Contains(msg.Text, sub) {
					t.Errorf("trailer must not contain %q, got %q", sub, msg.Text)
				}
			}

			// Trailer must be appended below original text (separated by blank line).
			if !strings.Contains(msg.Text, channel.StripInlineMarkup(tc.result.Text)) {
				t.Errorf("rendered text dropped the original body. got=%q original=%q", msg.Text, tc.result.Text)
			}
		})
	}
}

// TestTelegramPathSkipsTrailer is the symmetric check: button-capable channels
// must NEVER see the auto-derived trailer in their message body. Telegram
// users see the buttons; appending typing hints alongside would be redundant.
func TestTelegramPathSkipsTrailer(t *testing.T) {
	withButtons := channel.ChannelCapabilities{Text: true, Buttons: true, Markdown: true}

	res := &command.Result{
		Text: "🧠 Reasoning\nCurrent: xhigh",
		Interactive: &command.Interactive{Kind: command.InteractiveChoices, Choices: &command.ChoicesView{
			Title: "Choose a level:",
			Choices: []command.ListItem{
				{Label: "off", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"off"}}},
				{Label: "high", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"high"}}},
			},
		}},
	}
	msg := renderResult(res, RenderContext{Caps: withButtons, T: i18n.New("en")})

	if !strings.Contains(msg.Text, "Choose a level:") {
		t.Errorf("Telegram path should render Choices.Title, got %q", msg.Text)
	}
	if strings.Contains(msg.Text, "Pick with") {
		t.Errorf("Telegram message body leaked the no-button trailer: %q", msg.Text)
	}
	if len(msg.Actions) == 0 {
		t.Errorf("Telegram path should have button actions, got 0")
	}
}
