package command

import (
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/settings"
)

func (h *Handler) buildSettingsGroup() *CommandGroup {
	g := newCommandGroup("settings", "View and update bot settings")
	g.DefaultAction = "get"
	g.Register(SubCommand{
		Name:  "get",
		Usage: "get - View current settings",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			return h.settingsResult(cc, s), nil
		},
	})
	g.Register(SubCommand{
		Name:    "update",
		Usage:   "update [--language L] [--acl_default_effect allow|deny] ... - Update settings",
		IsWrite: true,
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if len(cc.Args) == 0 {
				return &Result{Text: cc.T("cmd.settings.updateUsage")}, nil
			}
			req := settings.UpsertRequest{}
			args := cc.Args
			for i := 0; i < len(args); i++ {
				if i+1 >= len(args) {
					return &Result{Text: cc.T("cmd.settings.missingValue", map[string]any{"option": args[i], "usage": cc.T("cmd.settings.updateUsage")})}, nil
				}
				switch args[i] {
				case "--language":
					i++
					req.Language = &args[i]
				case "--acl_default_effect":
					i++
					req.AclDefaultEffect = args[i]
				case "--reasoning_effort":
					i++
					req.ReasoningEffort = &args[i]
				case "--chat_model_id":
					i++
					req.ChatModelID = &args[i]
				default:
					return &Result{Text: cc.T("cmd.settings.unknownOption", map[string]any{"option": args[i], "usage": cc.T("cmd.settings.updateUsage")})}, nil
				}
			}
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, req); err != nil {
				return nil, err
			}
			s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			return h.settingsResult(cc, s), nil
		},
	})
	g.Register(SubCommand{
		Name:  "language",
		Usage: "language [auto|en|zh|ja] - View or set the command UI language",
		// Deliberately NOT IsWrite: the command-UI language is a display
		// preference, not a privileged bot setting, so any member can change it
		// without owner rights. (The general /settings update path stays gated.)
		ResultHandler: func(cc CommandContext) (*Result, error) {
			// No arg → show the picker.
			if len(cc.Args) == 0 {
				s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
				if err != nil {
					return nil, err
				}
				return commandLanguageResult(cc, s.CommandUILanguage), nil
			}
			// Arg → set the language (auto|en|zh|ja).
			v := strings.ToLower(strings.TrimSpace(cc.Args[0]))
			if v != "auto" && !i18n.IsSupported(v) {
				return &Result{Text: cc.T("cmd.settings.unknownLanguage", map[string]any{"value": cc.Args[0]})}, nil
			}
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, settings.UpsertRequest{CommandUILanguage: v}); err != nil {
				return nil, err
			}
			s, err := h.settingsService.GetBot(cc.Ctx, cc.BotID)
			if err != nil {
				return nil, err
			}
			// Re-localize the confirmation card (and its renderer chrome, via
			// Result.Locale) to the newly chosen language immediately.
			cc.L = i18n.New(s.CommandUILanguage)
			cc.Locale = cc.L.Locale()
			res := h.settingsResult(cc, s)
			res.Locale = cc.Locale
			return res, nil
		},
	})
	return g
}

// commandLanguageResult builds the command-UI language picker: one button per
// supported locale (current marked ✓). Tapping re-dispatches
// "/settings language <key>" (an un-gated path), which writes the choice and
// re-renders the settings card in the newly chosen language. The locale keys
// (auto/en/zh/ja) are canonical args and stay untranslated; only the labels are
// localized.
func commandLanguageResult(cc CommandContext, current string) *Result {
	return commandLanguageResultFor(cc, current, "settings", "language")
}

func commandLanguageResultFor(cc CommandContext, current, resource, action string) *Result {
	cur := strings.ToLower(strings.TrimSpace(current))
	if cur == "" {
		cur = "auto"
	}
	options := []struct {
		key   string
		label string
	}{
		{"auto", cc.T("cmd.settings.langAuto")},
		{"en", cc.T("cmd.settings.langEn")},
		{"zh", cc.T("cmd.settings.langZh")},
		{"ja", cc.T("cmd.settings.langJa")},
	}
	choices := make([]ListItem, 0, len(options))
	currentLabel := ""
	for _, o := range options {
		choices = append(choices, ListItem{
			Label:    o.label,
			Selected: cur == o.key,
			Action:   &ItemAction{Resource: resource, Action: action, Args: []string{o.key}},
		})
		if cur == o.key {
			currentLabel = o.label
		}
	}
	title := MdBold(cc.T("cmd.settings.langPickerTitle"))
	// Body includes a "Current: <label>" line so text-channel users see the
	// active choice — on Telegram the ✓ in the choice list carries the same
	// signal but text-channel users have no ✓ to read.
	body := title + "\n" + cc.T("cmd.settings.langCurrent", map[string]any{"label": currentLabel})
	// Four short locale options (auto/en/zh/ja) fit one compact row. Close gets
	// its own row (appended by the renderer).
	return &Result{
		Text:        body,
		Interactive: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{Title: body, Choices: choices, Columns: 4}},
	}
}

// settingsResult renders the settings card (the same KV detail as before) and,
// for button-capable channels, a set of one-tap controls: an inline ACL toggle
// (re-dispatch /settings update, which re-renders this card in place) and
// drill-downs to the /reasoning and /model pickers. Reuses
// settingsService.UpsertBot — no backend changes.
func (h *Handler) settingsResult(cc CommandContext, s settings.Settings) *Result {
	reasoning := formatReasoningLabel(cc, s)
	// Teach the ACL enum in plain English on this orienting surface.
	aclLine := strings.TrimSpace(s.AclDefaultEffect)
	switch strings.ToLower(aclLine) {
	case "deny":
		aclLine = cc.T("cmd.settings.aclDeny")
	case "allow":
		aclLine = cc.T("cmd.settings.aclAllow")
	}
	card := formatKVTitled(cc.T("cmd.settings.title"), []kv{
		{cc.T("cmd.settings.fieldReasoning"), reasoning},
		{cc.T("cmd.settings.fieldAclDefault"), aclLine},
		{cc.T("cmd.settings.fieldChatModel"), h.resolveModelName(cc, s.ChatModelID)},
		{cc.T("cmd.settings.fieldSearchProvider"), h.resolveSearchProviderName(cc, s.SearchProviderID)},
		{cc.T("cmd.settings.fieldMemoryProvider"), h.resolveMemoryProviderName(cc, s.MemoryProviderID)},
		{cc.T("cmd.settings.fieldCommandLanguage"), commandLanguageDisplay(cc, s.CommandUILanguage)},
	})
	aclNext := "deny"
	aclAction := cc.T("cmd.settings.action.aclAsk")
	if strings.EqualFold(strings.TrimSpace(s.AclDefaultEffect), "deny") {
		aclNext = "allow"
		aclAction = cc.T("cmd.settings.action.aclAllow")
	}
	choices := []ListItem{
		{Label: cc.T("cmd.settings.section.reasoning"), Action: &ItemAction{Resource: "reasoning", Action: "show"}},
		{Label: cc.T("cmd.settings.section.models"), Action: &ItemAction{Resource: "model", Action: "list"}},
		{Label: aclAction, Action: &ItemAction{Resource: "settings", Action: "update", Args: []string{"--acl_default_effect", aclNext}}},
		{Label: cc.T("cmd.settings.section.search"), Action: &ItemAction{Resource: "search", Action: "list"}},
		{Label: cc.T("cmd.settings.section.memory"), Action: &ItemAction{Resource: "memory", Action: "list"}},
		{Label: cc.T("cmd.settings.section.language"), Action: &ItemAction{Resource: "settings", Action: "language"}},
	}
	return &Result{
		Text:        card,
		Interactive: &Interactive{Kind: InteractiveChoices, Choices: &ChoicesView{Title: card, Choices: choices}},
	}
}

// commandLanguageDisplay renders the stored command-UI language setting for the
// settings card: "auto"/en/zh/ja map to their localized labels; anything else is
// shown verbatim.
func commandLanguageDisplay(cc CommandContext, value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return cc.T("cmd.settings.langAuto")
	case "en":
		return cc.T("cmd.settings.langEn")
	case "zh":
		return cc.T("cmd.settings.langZh")
	case "ja":
		return cc.T("cmd.settings.langJa")
	default:
		return value
	}
}

func (h *Handler) getBotSettings(cc CommandContext) (settings.Settings, error) {
	if h.settingsService == nil {
		return settings.Settings{}, errors.New("settings service is not available")
	}
	return h.settingsService.GetBot(cc.Ctx, cc.BotID)
}

// resolveModelName resolves a model UUID to "model_name (provider_name)". The
// "(none)"/"(unavailable)" placeholders are localized via cc.T so they don't leak
// English onto otherwise-localized cards; callers comparing the result against an
// empty model must compare against cc.T("cmd.common.none"), not a literal.
func (h *Handler) resolveModelName(cc CommandContext, modelID string) string {
	if modelID == "" {
		return cc.T("cmd.common.none")
	}
	if h.modelsService == nil {
		return cc.T("cmd.common.unavailable")
	}
	m, err := h.modelsService.GetByID(cc.Ctx, modelID)
	if err != nil {
		return cc.T("cmd.common.unavailable")
	}
	provName := ""
	if h.providersService != nil {
		p, err := h.providersService.Get(cc.Ctx, m.ProviderID)
		if err == nil {
			provName = p.Name
		}
	}
	if provName != "" {
		return fmt.Sprintf("%s (%s)", modelDisplayName(m), provName)
	}
	return modelDisplayName(m)
}

// resolveSearchProviderName resolves a search provider UUID to its name.
func (h *Handler) resolveSearchProviderName(cc CommandContext, id string) string {
	if id == "" {
		return cc.T("cmd.common.none")
	}
	if h.searchProvService == nil {
		return cc.T("cmd.common.unavailable")
	}
	p, err := h.searchProvService.Get(cc.Ctx, id)
	if err != nil {
		return cc.T("cmd.common.unavailable")
	}
	return p.Name
}

// resolveMemoryProviderName resolves a memory provider UUID to its name.
func (h *Handler) resolveMemoryProviderName(cc CommandContext, id string) string {
	if id == "" {
		return cc.T("cmd.common.none")
	}
	if h.memProvService == nil {
		return cc.T("cmd.common.unavailable")
	}
	p, err := h.memProvService.Get(cc.Ctx, id)
	if err != nil {
		return cc.T("cmd.common.unavailable")
	}
	return p.Name
}
