package command

import (
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/i18n"
)

func TestLanguageShortcutRegisteredAndUngated(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	g, ok := h.registry.groups["language"]
	if !ok {
		t.Fatal("/language group not registered")
	}
	if g.DefaultAction != "show" {
		t.Fatalf("/language default = %q, want show", g.DefaultAction)
	}
	if g.commands["set"].IsWrite {
		t.Fatal("/language set should not be owner-gated")
	}
	if !h.IsCommand("/language") {
		t.Fatal("/language should be recognized")
	}
	var foundMenuCommand bool
	for _, c := range MenuCommands(i18n.New("zh")) {
		if c.Command == "language" {
			foundMenuCommand = true
			if c.Description != "切换命令界面语言" {
				t.Fatalf("/language menu description = %q", c.Description)
			}
		}
	}
	if !foundMenuCommand {
		t.Fatal("/language should be included in native command menu")
	}
}

func TestLanguageShortcutPickerDispatchesLanguageSet(t *testing.T) {
	t.Parallel()
	res := commandLanguageResultFor(CommandContext{L: i18n.New("zh")}, "zh", "language", "set")
	if res == nil || !strings.Contains(res.Text, i18n.New("zh").T("cmd.settings.langPickerTitle")) {
		t.Fatalf("localized result = %+v", res)
	}
	// The picker packs its short locale options in one compact row (Columns:4) so
	// Telegram renders compact buttons instead of stretching one full-width
	// button per row. (Explicit "/language <arg>" no longer renders this picker
	// at all — it returns a plain text confirmation; see the set handler.)
	if res.Interactive == nil || res.Interactive.Choices == nil || res.Interactive.Choices.Columns != 4 {
		t.Fatalf("/language picker should pack options 4-per-row, got %+v", res.Interactive)
	}
	for _, choice := range res.Interactive.Choices.Choices {
		if choice.Action == nil || choice.Action.Resource != "language" || choice.Action.Action != "set" {
			t.Fatalf("/language picker choice dispatches %+v", choice.Action)
		}
	}
}
