package inbound

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
)

// TestRenderChromeLocalizedButTokensPreserved is the core "not string-replacement"
// guarantee: under zh the renderer's own chrome (Close/Prev/Next) renders in
// Chinese, while the callback data carrying command tokens (e.g. the reasoning
// level "xhigh") is byte-identical to the English render. Translation touches
// display, never the canonical args.
func TestRenderChromeLocalizedButTokensPreserved(t *testing.T) {
	res := &command.Result{
		Text: "推理",
		Interactive: &command.Interactive{
			Kind: command.InteractiveChoices,
			Choices: &command.ChoicesView{
				Title: "推理",
				Choices: []command.ListItem{
					{Label: "xhigh", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"xhigh"}}},
				},
			},
		},
	}
	zh := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true, Markdown: true}, T: i18n.New("zh")})

	var closeLabel, choiceValue string
	for _, a := range zh.Actions {
		if a.Value == command.DismissCallback() {
			closeLabel = a.Label
			continue
		}
		choiceValue = a.Value
	}
	if closeLabel != "✕ 关闭" {
		t.Errorf("zh Close chrome = %q, want %q", closeLabel, "✕ 关闭")
	}
	// The command token round-trips unchanged regardless of locale.
	wantValue := command.EncodeListCallback("reasoning", "set", []string{"xhigh"}, 0)
	if choiceValue != wantValue {
		t.Errorf("choice callback = %q, want %q (token must not be translated)", choiceValue, wantValue)
	}
}

func TestRenderListNavChromeZh(t *testing.T) {
	res := listResult(50, 0, 12) // 5 pages → has Next + Close
	zh := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("zh")})
	var hasZhNext, hasZhClose bool
	for _, a := range zh.Actions {
		switch a.Label {
		case "下一页 ▶":
			hasZhNext = true
		case "✕ 关闭":
			hasZhClose = true
		case "Next ▶", "✕ Close":
			t.Errorf("found English chrome %q under zh locale", a.Label)
		}
	}
	if !hasZhNext || !hasZhClose {
		t.Errorf("zh list chrome missing: next=%v close=%v", hasZhNext, hasZhClose)
	}
}

func TestRenderRangePresetAllLocalizedTokenPreserved(t *testing.T) {
	res := &command.Result{
		Text: "用量",
		Interactive: &command.Interactive{
			Kind:  command.InteractiveRange,
			Range: &command.RangeView{Resource: "usage", Action: "summary", Current: "7d", Presets: []string{"7d", "all"}},
		},
	}
	zh := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true}, T: i18n.New("zh")})
	var allLabel, allValue string
	for _, a := range zh.Actions {
		if a.Value == command.EncodeRangeCallback("usage", "summary", "all") {
			allLabel = a.Label
			allValue = a.Value
		}
	}
	if allLabel != "全部" {
		t.Errorf("zh 'all' preset label = %q, want %q", allLabel, "全部")
	}
	if allValue != command.EncodeRangeCallback("usage", "summary", "all") {
		t.Errorf("'all' preset callback token must be preserved, got %q", allValue)
	}
}

// TestNoButtonChannelKeepsCopyableFallback verifies the degrade path: a
// button-less channel drops the keyboard but inherits a typeable-command
// trailer auto-derived from the Interactive payload, so a WeChat-class user
// learns how to set reasoning by typing.
func TestNoButtonChannelKeepsCopyableFallback(t *testing.T) {
	res := reasoningFallbackResultZh()
	plain := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{}, T: i18n.New("zh")})
	if len(plain.Actions) != 0 {
		t.Errorf("button-less channel should have no actions, got %d", len(plain.Actions))
	}
	if !strings.Contains(plain.Text, "/reasoning set") {
		t.Errorf("fallback should auto-derive a copyable /reasoning set command, got %q", plain.Text)
	}
	if !strings.Contains(plain.Text, "xhigh") {
		t.Errorf("trailer should enumerate the available reasoning levels, got %q", plain.Text)
	}
	if strings.Contains(plain.Text, "`") || strings.Contains(plain.Text, "**") {
		t.Errorf("plain channel should strip markup, got %q", plain.Text)
	}
}

// TestChromeFollowsResultLocaleNotStaleRenderContext is the regression guard for
// the in-place language-switch bug: when a command changes command_ui_language,
// the command layer stamps the NEW locale on Result.Locale, but the channel
// passes the PRE-command (stale) localizer in RenderContext.T. Chrome must follow
// Result.Locale (the body's locale), not the stale RenderContext.T, so the whole
// reply stays in one language.
func TestChromeFollowsResultLocaleNotStaleRenderContext(t *testing.T) {
	res := &command.Result{
		Text:   "推理",
		Locale: "zh", // command rendered its body in zh (e.g. just switched to zh)
		Interactive: &command.Interactive{
			Kind: command.InteractiveChoices,
			Choices: &command.ChoicesView{
				Title:   "推理",
				Choices: []command.ListItem{{Label: "xhigh", Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{"xhigh"}}}},
			},
		},
	}
	// Stale RenderContext localizer is en (the pre-switch locale).
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true, Markdown: true}, T: i18n.New("en")})
	var closeLabel string
	for _, a := range msg.Actions {
		if a.Value == command.DismissCallback() {
			closeLabel = a.Label
		}
	}
	if closeLabel != "✕ 关闭" {
		t.Errorf("chrome should follow Result.Locale=zh, got Close=%q (stale en RenderContext.T leaked)", closeLabel)
	}
}

// reasoningFallbackResultZh mirrors the shape the command layer produces for
// /reasoning after Phase 2 of the no-button trailer refactor: a localized
// header in Text plus a Choices view carrying every level. The renderer
// auto-appends the typeable-command trailer for button-less channels.
func reasoningFallbackResultZh() *command.Result {
	t := i18n.New("zh")
	header := command.MdBold(t.T("cmd.reasoning.header")) + "\n" +
		t.T("cmd.reasoning.current", map[string]any{"level": "xhigh"})
	levels := []string{"off", "none", "low", "medium", "high", "xhigh"}
	choices := make([]command.ListItem, 0, len(levels))
	for _, lvl := range levels {
		choices = append(choices, command.ListItem{
			Label:  lvl,
			Action: &command.ItemAction{Resource: "reasoning", Action: "set", Args: []string{lvl}},
		})
	}
	return &command.Result{
		Text:   header,
		Locale: "zh",
		Interactive: &command.Interactive{
			Kind:    command.InteractiveChoices,
			Choices: &command.ChoicesView{Title: header, Choices: choices},
		},
	}
}
