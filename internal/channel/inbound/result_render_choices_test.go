package inbound

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
)

func TestRenderChoicesView(t *testing.T) {
	res := &command.Result{
		Text: "Reasoning effort\nCurrent: medium\nLevels: low, medium, high",
		Interactive: &command.Interactive{
			Kind: command.InteractiveChoices,
			Choices: &command.ChoicesView{
				Title: "**Reasoning effort**\nCurrent: medium",
				Choices: []command.ListItem{
					{Label: "low", Action: &command.ItemAction{Resource: "effort", Action: "set", Args: []string{"low"}}},
					{Label: "medium", Selected: true, Action: &command.ItemAction{Resource: "effort", Action: "set", Args: []string{"medium"}}},
					{Label: "high", Action: &command.ItemAction{Resource: "effort", Action: "set", Args: []string{"high"}}},
				},
			},
		},
	}
	msg := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{Buttons: true, Markdown: true}, T: i18n.New("en")})

	var hasClose, marked bool
	for _, a := range msg.Actions {
		if a.Label == "✕ Close" {
			hasClose = true
			continue
		}
		if strings.HasPrefix(a.Label, "✓ ") {
			marked = true
			if !strings.Contains(a.Label, "medium") {
				t.Errorf("✓ on wrong choice: %q", a.Label)
			}
		}
		// Every choice button must carry a re-dispatchable callback.
		if a.Value != command.EncodeListCallback("effort", "set", []string{strings.TrimPrefix(a.Label, "✓ ")}, 0) {
			t.Errorf("choice %q callback = %q", a.Label, a.Value)
		}
	}
	if !hasClose {
		t.Error("expected a Close button")
	}
	if !marked {
		t.Error("expected the current choice marked with ✓")
	}

	// Text-only channel: no buttons, markup stripped, fallback text retained.
	plain := renderResult(res, RenderContext{Caps: channel.ChannelCapabilities{}, T: i18n.New("en")})
	if len(plain.Actions) != 0 {
		t.Errorf("text-only channel should have no buttons, got %d", len(plain.Actions))
	}
	if strings.Contains(plain.Text, "`") || strings.Contains(plain.Text, "**") {
		t.Errorf("text-only channel should strip markup, got %q", plain.Text)
	}
}
