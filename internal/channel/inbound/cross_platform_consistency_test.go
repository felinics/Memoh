package inbound

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
)

// TestNonTelegramPlatformsRenderEquivalently is the consistency guarantee for
// every channel that's NOT Telegram. The user complaint that motivated this:
// when a user types /help on WeChat, they should reasonably expect the same
// information that a Discord/Slack/Feishu user gets — not a different (lesser)
// rendering. Telegram is the lone Buttons:true platform and takes its own path;
// the remaining 11 platforms collapse into three capability profiles:
//
//   - Plain-text only: Weixin, WeChat OA, Local-Web (Markdown:false, RichText:false)
//   - Markdown:         Discord, Slack, DingTalk, QQ, WeCom, Misskey, Matrix
//   - RichText only:    Feishu (treated as markdown by the renderer)
//
// All three groups must receive equivalent message TEXT. Inline styling
// (bold, monospace) may render visually different on each, but the typeable
// commands, the per-row content, the trailer guidance — none of that should
// differ in substance.
//
// This test pins the contract: render a representative cross-section of
// commands through each profile and assert that the substantive content
// appears in every output, regardless of capability profile.
func TestNonTelegramPlatformsRenderEquivalently(t *testing.T) {
	// Three capability profiles, all with Buttons:false (only Telegram has Buttons:true).
	profiles := map[string]channel.ChannelCapabilities{
		"plain-text (Weixin / WeChat OA / Local-Web)":                           {Text: true},
		"markdown (Discord / Slack / DingTalk / QQ / WeCom / Misskey / Matrix)": {Text: true, Markdown: true},
		"richtext (Feishu)": {Text: true, RichText: true},
	}

	cases := []struct {
		name        string
		result      *command.Result
		mustContain []string // substrings that must appear in every profile's output
	}{
		{
			name: "/reasoning picker",
			result: &command.Result{
				Text: "🧠 Reasoning\nCurrent: medium",
				Interactive: &command.Interactive{
					Kind: command.InteractiveChoices,
					Choices: &command.ChoicesView{
						Choices: []command.ListItem{
							{Label: "off", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"off"}}},
							{Label: "low", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"low"}}},
							{Label: "high", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"high"}}},
						},
					},
				},
			},
			mustContain: []string{
				"Reasoning",                     // body title (markup stripped on plain, rendered on others)
				"Current: medium",               // current state
				"/reasoning set <off|low|high>", // typeable enum surfaced via pick trailer
			},
		},
		{
			name: "/settings (worst-case 7-way cross-nav)",
			result: &command.Result{
				Text: "⚙️ Bot Settings\n- 推理: medium\n- 心跳: off",
				Interactive: &command.Interactive{
					Kind: command.InteractiveChoices,
					Choices: &command.ChoicesView{
						Choices: []command.ListItem{
							{Action: &command.ItemAction{Resource: "reasoning", Action: "show"}},
							{Action: &command.ItemAction{Resource: "model", Action: "list"}},
							{Action: &command.ItemAction{Resource: "settings", Action: "update", Args: []string{"--acl_default_effect", "deny"}}},
							{Action: &command.ItemAction{Resource: "search", Action: "list"}},
							{Action: &command.ItemAction{Resource: "memory", Action: "list"}},
							{Action: &command.ItemAction{Resource: "settings", Action: "language"}},
						},
					},
				},
			},
			mustContain: []string{
				"Bot Settings", "推理", "心跳", // body content
				"/reasoning show", "/model list", // cross-nav commands
				"/settings update --acl_default_effect deny",
				"/search list", "/memory list",
				"/settings language",
			},
		},
		{
			name: "/usage range view",
			result: &command.Result{
				Text: "Token usage (7d)\n\n- May 27 · input 5.4K · output 42\n- May 28 · input 188.5K · output 3.8K",
				Interactive: &command.Interactive{
					Kind: command.InteractiveRange,
					Range: &command.RangeView{
						Resource: "usage", Action: "summary", Current: "7d",
						Presets: []string{"24h", "7d", "30d", "all"},
					},
				},
			},
			mustContain: []string{
				"Token usage", "May 27", "May 28",
				"/usage summary --range <preset>",
				"24h", "7d", "30d", "all",
			},
		},
		{
			name: "/model provider summary",
			result: &command.Result{
				Text: "Chat Models (564)\n\nCurrent model: DeepSeek V4 Flash\n\nBy provider:\n- DeepSeek (4) ●\n- OpenAI (124)\n- OpenRouter (436)",
				Interactive: &command.Interactive{
					Kind: command.InteractiveModelPicker,
					Picker: &command.ModelPickerView{
						Level:     command.LevelProviders,
						Providers: []command.PickerProvider{{Name: "DeepSeek", Count: 4}, {Name: "OpenAI", Count: 124}, {Name: "OpenRouter", Count: 436}},
					},
				},
			},
			mustContain: []string{
				"DeepSeek", "OpenAI", "OpenRouter", // providers visible in body
				"DeepSeek V4 Flash",           // current model
				"/model list <provider_name>", // typeable next step from trailer
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs := make(map[string]string, len(profiles))
			for profile, caps := range profiles {
				msg := renderResult(tc.result, RenderContext{Caps: caps, T: i18n.New("en")})
				if len(msg.Actions) != 0 {
					t.Errorf("[%s] non-Telegram channel should never receive button actions, got %d", profile, len(msg.Actions))
				}
				outputs[profile] = msg.Text
			}

			// Every required substring must appear in every profile's output. The
			// raw markup may differ (** stays on markdown channels, stripped on
			// plain) — assertions target the content tokens, not the styling.
			for profile, output := range outputs {
				for _, sub := range tc.mustContain {
					if !strings.Contains(output, sub) {
						t.Errorf("[%s] missing required content %q\nFull output:\n%s", profile, sub, output)
					}
				}
			}

			// Cross-platform check: after stripping markdown markup AND
			// collapsing trailing whitespace, the three profiles must produce
			// equivalent plain text. Any divergence here means some content is
			// conditional on caps — which is exactly the drift this test
			// exists to prevent.
			//
			// Inline styling (** and `) is stripped before comparison since
			// those are legitimate cap-driven differences (markdown channels
			// render them, plain channels strip them). Trailing whitespace
			// from per-renderer chrome is normalized so a stray "\n" on one
			// profile doesn't trip the byte-equality check.
			canonical := make(map[string]string)
			for profile, output := range outputs {
				canonical[profile] = strings.TrimRight(channel.StripInlineMarkup(output), " \t\n")
			}
			plain := canonical["plain-text (Weixin / WeChat OA / Local-Web)"]
			for profile, c := range canonical {
				if c != plain {
					t.Errorf("[%s] rendered text diverges from plain-text profile after markup strip.\n--- plain ---\n%s\n--- %s ---\n%s", profile, plain, profile, c)
				}
			}
		})
	}
}
