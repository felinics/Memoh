package command

import (
	"fmt"
	"strings"

	"github.com/memohai/memoh/internal/i18n"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/settings"
)

// reasoningChoices are the selectable reasoning levels. "off" disables thinking;
// the rest enable it at that effort. Only "off" represents the disabled state —
// listing "none" here too offered the same state under a second name.
var reasoningChoices = []string{
	"off",
	models.ReasoningEffortLow,
	models.ReasoningEffortMedium,
	models.ReasoningEffortHigh,
	models.ReasoningEffortXHigh,
}

// validEffort accepts the tiers that turn reasoning on. The disabled state is
// handled by its own branch before this is consulted.
func validEffort(v string) bool {
	switch v {
	case models.ReasoningEffortLow, models.ReasoningEffortMedium, models.ReasoningEffortHigh, models.ReasoningEffortXHigh:
		return true
	}
	return false
}

// buildReasoningGroup registers /reasoning — a first-class sibling of /model.
// Aliases /reason, /effort, /think all resolve here (see resourceAliases). It
// shows the current reasoning level and lets the user pick the reasoning effort
// in one tap, reusing settingsService.UpsertBot (no backend changes).
func (h *Handler) buildReasoningGroup() *CommandGroup {
	g := newCommandGroup("reasoning", "View or set reasoning level")
	g.DefaultAction = "show"
	g.Register(SubCommand{
		Name:  "show",
		Usage: "show - Show the reasoning level and pick a new one",
		ResultHandler: func(cc CommandContext) (*Result, error) {
			s, err := h.getBotSettings(cc)
			if err != nil {
				return nil, err
			}
			return reasoningResult(cc.L, s.ReasoningEffort), nil
		},
	})
	g.Register(SubCommand{
		Name:    "set",
		Usage:   "set <off|none|low|medium|high|xhigh> - Set the reasoning level",
		IsWrite: true,
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if len(cc.Args) < 1 {
				return &Result{Text: cc.T("cmd.reasoning.setUsage")}, nil
			}
			level := strings.ToLower(strings.TrimSpace(cc.Args[0]))
			if h.settingsService == nil {
				return &Result{Text: cc.T("cmd.reasoning.unavailable")}, nil
			}
			req := settings.UpsertRequest{}
			switch {
			case level == "off":
				// "off" is the user-facing token; storage represents it as the
				// "disable" effort now that bots have no separate on/off flag.
				disable := models.ReasoningEffortDisable
				req.ReasoningEffort = &disable
			case validEffort(level):
				req.ReasoningEffort = &level
			default:
				return &Result{Text: cc.T("cmd.reasoning.unknownLevel", map[string]any{"level": fmt.Sprintf("%q", cc.Args[0])})}, nil
			}
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, req); err != nil {
				return nil, err
			}
			s, err := h.getBotSettings(cc)
			if err != nil {
				return nil, err
			}
			return reasoningResult(cc.L, s.ReasoningEffort), nil
		},
	})
	return g
}

// reasoningResult builds the picker: a header with the current level plus one
// button per level (current marked ✓). Tapping re-dispatches "/reasoning set X"
// which edits the message in place. Level tokens (off/none/low/…) are canonical
// args and stay untranslated; only the surrounding prose is localized via t.
//
// Buttons render for everyone; the owner-only gate is enforced at execution
// time (IsWrite), so a non-owner tap returns a clear "owner only" message rather
// than the buttons being hidden — hiding them also hid them from owners whose
// Telegram identity isn't resolved as owner, killing the feature.
func reasoningResult(t *i18n.Localizer, effort string) *Result {
	effort = strings.ToLower(strings.TrimSpace(effort))
	disabled := models.IsReasoningDisabled(effort)
	current := effort
	switch {
	case disabled:
		current = t.T("cmd.common.off")
	case current == "":
		current = t.T("cmd.common.on")
	}
	header := MdBold(t.T("cmd.reasoning.header")) + "\n" + t.T("cmd.reasoning.current", map[string]any{"level": current})
	choices := make([]ListItem, 0, len(reasoningChoices))
	for _, lvl := range reasoningChoices {
		selected := false
		if lvl == "off" {
			selected = disabled
		} else {
			selected = !disabled && lvl == effort
		}
		choices = append(choices, ListItem{
			Label:    lvl,
			Selected: selected,
			Action:   &ItemAction{Resource: "reasoning", Action: "set", Args: []string{lvl}},
		})
	}
	// Telegram users see header + "Choose a level:" + tappable buttons. No-button
	// channels see header + the same "Choose a level:" explainer (the tradeoff
	// reads as orienting advice on both surfaces) + the auto-derived
	// "Pick with /reasoning set <…>" trailer the renderer appends. Without the
	// explainer in Text, text-channel users only saw the bare current-level
	// header and a command syntax line with no context for why the levels matter.
	body := header + "\n\n" + t.T("cmd.reasoning.choosePrompt")
	return &Result{
		Text: body,
		Interactive: &Interactive{
			Kind:    InteractiveChoices,
			Choices: &ChoicesView{Title: body, Choices: choices},
		},
	}
}
