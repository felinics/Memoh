package command

import (
	"strings"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/settings"
)

func (h *Handler) buildLanguageGroup() *CommandGroup {
	g := newCommandGroup("language", "Change command UI language")
	g.DefaultAction = "show"
	g.Register(SubCommand{
		Name:  "show",
		Usage: "show - View or set the command UI language",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.settingsService == nil {
				return &Result{Text: cc.T("cmd.settings.unavailable")}, nil
			}
			s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			return commandLanguageResultFor(cc, s.CommandUILanguage, "language", "set"), nil
		},
	})
	g.Register(SubCommand{
		Name:  "set",
		Usage: "set <auto|en|zh|ja> - Set the command UI language",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.settingsService == nil {
				return &Result{Text: cc.T("cmd.settings.unavailable")}, nil
			}
			if len(cc.Args) == 0 {
				return &Result{Text: cc.T("cmd.language.setUsage")}, nil
			}
			v := strings.ToLower(strings.TrimSpace(cc.Args[0]))
			if v != "auto" && !i18n.IsSupported(v) {
				return &Result{Text: cc.T("cmd.settings.unknownLanguage", map[string]any{"value": cc.Args[0]})}, nil
			}
			before, _ := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{CommandUILanguage: v}); err != nil {
				return nil, err
			}
			s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			// Explicit set ("/language zh") confirms the change in plain text — it
			// must NOT re-render the chooser buttons (a "set" is a set, not a
			// re-prompt). The picker is reserved for the no-arg "/language" path
			// (the show handler). Re-localize the confirmation to the newly chosen
			// language and stamp Result.Locale so the renderer chrome matches.
			cc.L = i18n.New(s.CommandUILanguage)
			cc.Locale = cc.L.Locale()
			return &Result{
				Text: formatChangedValueT(cc, cc.T("cmd.settings.fieldCommandLanguage"),
					commandLanguageDisplay(cc, before.CommandUILanguage),
					commandLanguageDisplay(cc, s.CommandUILanguage)),
				Locale: cc.Locale,
			}, nil
		},
	})
	return g
}
