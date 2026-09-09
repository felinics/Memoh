package command

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/settings"
)

// TestReasoningResultZhLocalizesProseNotTokens proves the migration is
// key-based, not string-replacement: under zh the surrounding prose is Chinese,
// but the level button labels and their dispatched Args stay canonical tokens.
func TestReasoningResultZhLocalizesProseNotTokens(t *testing.T) {
	t.Parallel()
	res := reasoningResult(i18n.New("zh"), "xhigh", fullLadderOptions())
	title := res.Interactive.Choices.Title
	if !strings.Contains(title, "🧠 推理") {
		t.Errorf("zh header missing, title=%q", title)
	}
	if !strings.Contains(title, "当前：xhigh") {
		t.Errorf("zh current line missing, title=%q", title)
	}
	for _, c := range res.Interactive.Choices.Choices {
		// Labels are canonical tokens, never translated.
		switch c.Label {
		case "off", "none", "low", "medium", "high", "xhigh":
		default:
			t.Errorf("reasoning level label %q should stay a canonical token", c.Label)
		}
		if c.Action == nil || c.Action.Action != "set" || len(c.Action.Args) != 1 {
			t.Fatalf("choice %q has bad action %+v", c.Label, c.Action)
		}
		if c.Action.Args[0] != c.Label {
			t.Errorf("choice %q dispatches Args %v; token must equal label", c.Label, c.Action.Args)
		}
	}
}

func TestSettingsResultZhLocalizesLabelsNotArgs(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	cc := CommandContext{L: i18n.New("zh")}
	res := h.settingsResult(cc, settings.Settings{AclDefaultEffect: "allow", CommandUILanguage: "auto"})

	if !strings.Contains(res.Text, "⚙️ 机器人设置") {
		t.Errorf("zh settings title missing:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "命令界面语言") {
		t.Errorf("zh Command Language field missing:\n%s", res.Text)
	}
	// auto renders as the localized "自动".
	if !strings.Contains(res.Text, "自动") {
		t.Errorf("auto command language should display 自动:\n%s", res.Text)
	}

	var hasZhReasoningBtn, hasLanguageBtn bool
	var aclArgs []string
	for _, c := range res.Interactive.Choices.Choices {
		switch c.Label {
		case "推理 ▸":
			hasZhReasoningBtn = true
		case "语言 ▸":
			hasLanguageBtn = true
		}
		if c.Action != nil && len(c.Action.Args) >= 1 && c.Action.Args[0] == "--acl_default_effect" {
			aclArgs = c.Action.Args
		}
	}
	if !hasZhReasoningBtn {
		t.Error("expected zh '推理 ▸' button")
	}
	if !hasLanguageBtn {
		t.Error("expected '语言 ▸' button")
	}
	// The toggle's canonical args are not translated.
	if len(aclArgs) != 2 || aclArgs[0] != "--acl_default_effect" || aclArgs[1] != "deny" {
		t.Errorf("acl toggle args = %v, want [--acl_default_effect deny]", aclArgs)
	}
}

func TestSettingsLanguageIsNotWriteGated(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil)
	g, ok := h.registry.groups["settings"]
	if !ok {
		t.Fatal("settings group not registered")
	}
	lang, ok := g.commands["language"]
	if !ok {
		t.Fatal("settings language sub-command not registered")
	}
	// Command-UI language is a display preference: any member may change it, so
	// the sub-command must not be owner-gated.
	if lang.IsWrite {
		t.Error("/settings language must NOT be IsWrite (it should not require owner rights)")
	}
	// The general update path stays gated (regression guard for the split).
	if upd, ok := g.commands["update"]; !ok || !upd.IsWrite {
		t.Error("/settings update should remain IsWrite")
	}
}

func TestCommandLanguageResultMarksCurrentTokensCanonical(t *testing.T) {
	t.Parallel()
	res := commandLanguageResult(CommandContext{L: i18n.New("zh")}, "zh")
	if res.Interactive == nil || res.Interactive.Choices == nil {
		t.Fatal("expected choices")
	}
	var markedKey string
	keys := map[string]string{"自动": "auto", "English": "en", "中文": "zh", "日本語": "ja"}
	for _, c := range res.Interactive.Choices.Choices {
		wantKey, ok := keys[c.Label]
		if !ok {
			t.Errorf("unexpected language label %q", c.Label)
			continue
		}
		if c.Action == nil || len(c.Action.Args) != 1 || c.Action.Resource != "settings" || c.Action.Action != "language" || c.Action.Args[0] != wantKey {
			t.Errorf("label %q dispatches %+v, want settings/language [%s]", c.Label, c.Action, wantKey)
		}
		if c.Selected {
			markedKey = wantKey
		}
	}
	if markedKey != "zh" {
		t.Errorf("current zh should be marked, got %q", markedKey)
	}
}
