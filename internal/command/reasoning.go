package command

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
	"github.com/felinics/memoh/internal/settings"
)

// offChoice is the user-facing token for the disabled state. Storage represents
// it as ReasoningEffortDisable; "off" is what a person types and taps.
const offChoice = "off"

// reasoningOptions reports what the bot's current chat model can actually do.
// It replaces a hardcoded list that offered the same five levels on every model —
// including Off on models that cannot be turned off, and never xhigh's neighbours
// on models that advertise them.
//
// The bool reports whether both the model and its provider were resolved. It is
// deliberately separate from Options.Supported: a resolved model may genuinely
// have no reasoning capability, while a lookup failure leaves the capability
// unknown. Neither case is safe to treat as a generic reasoning model.
func (h *Handler) reasoningOptions(cc CommandContext, modelID string) (reasoning.Options, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || h.modelsService == nil {
		return reasoning.Options{}, false
	}
	opts, err := h.modelsService.ResolveReasoningOptions(cc.Ctx, modelID)
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("reasoning model lookup failed",
				slog.String("bot_id", cc.BotID),
				slog.String("model_id", modelID),
				slog.Any("error", err),
			)
		}
		return reasoning.Options{}, false
	}
	return opts, true
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
			opts, resolved := h.reasoningOptions(cc, s.ChatModelID)
			if !resolved {
				return &Result{Text: cc.T("cmd.reasoning.unavailable")}, nil
			}
			if !opts.Supported {
				return &Result{Text: cc.T("cmd.reasoning.unsupported")}, nil
			}
			if !hasReasoningControls(opts) {
				return &Result{Text: cc.T("cmd.reasoning.uncontrollable")}, nil
			}
			return reasoningResult(cc.L, s.ReasoningEffort, opts), nil
		},
	})
	g.Register(SubCommand{
		Name:    "set",
		Usage:   "set <off|minimal|low|medium|high|xhigh|max> - Set the reasoning level",
		IsWrite: true,
		ResultHandler: func(cc CommandContext) (*Result, error) {
			if h.settingsService == nil {
				return &Result{Text: cc.T("cmd.reasoning.unavailable")}, nil
			}
			current, err := h.getBotSettings(cc)
			if err != nil {
				return nil, err
			}
			opts, resolved := h.reasoningOptions(cc, current.ChatModelID)
			if !resolved {
				return &Result{Text: cc.T("cmd.reasoning.unavailable")}, nil
			}
			if !opts.Supported {
				return &Result{Text: cc.T("cmd.reasoning.unsupported")}, nil
			}
			if !hasReasoningControls(opts) {
				return &Result{Text: cc.T("cmd.reasoning.uncontrollable")}, nil
			}
			choices := reasoningChoicesFor(opts)
			if len(cc.Args) < 1 {
				return &Result{Text: cc.T("cmd.reasoning.setUsage", map[string]any{
					"levels": strings.Join(choices, "|"),
				})}, nil
			}
			level := strings.ToLower(strings.TrimSpace(cc.Args[0]))

			req := settings.UpsertRequest{}
			switch {
			case level == offChoice && acceptsEffort(level, opts):
				// "off" is the user-facing token; storage represents it as the
				// "disable" effort now that bots have no separate on/off flag.
				disable := models.ReasoningEffortDisable
				req.ReasoningEffort = &disable
			case acceptsEffort(level, opts):
				req.ReasoningEffort = &level
			default:
				// Name the levels rather than pointing at a list that may not be on
				// screen: a typed `set <level>` has no picker above it, and the
				// levels differ per model, so the message has to carry them.
				return &Result{Text: cc.T("cmd.reasoning.unknownLevel", map[string]any{
					"level":  fmt.Sprintf("%q", cc.Args[0]),
					"levels": strings.Join(choices, ", "),
				})}, nil
			}
			if _, err := h.settingsService.UpsertBot(cc.Ctx, cc.BotID, req); err != nil {
				return nil, err
			}
			s, err := h.getBotSettings(cc)
			if err != nil {
				return nil, err
			}
			return reasoningResult(cc.L, s.ReasoningEffort, opts), nil
		},
	})
	return g
}

// reasoningResult builds the picker: a header with the current level plus one
// button per level (current marked ✓). Tapping re-dispatches "/reasoning set X"
// which edits the message in place. Level tokens (off/low/…) are canonical
// args and stay untranslated; only the surrounding prose is localized via t.
//
// Buttons render for everyone; the owner-only gate is enforced at execution
// time (IsWrite), so a non-owner tap returns a clear "owner only" message rather
// than the buttons being hidden — hiding them also hid them from owners whose
// Telegram identity isn't resolved as owner, killing the feature.
func reasoningResult(t *i18n.Localizer, effort string, opts reasoning.Options) *Result {
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
	choices := make([]ListItem, 0, len(opts.Efforts)+1)
	for _, lvl := range reasoningChoicesFor(opts) {
		selected := false
		if lvl == offChoice {
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

// reasoningChoicesFor renders the levels this model actually offers, with "off"
// leading when off is reachable. Callers handle unresolved and unsupported models
// before reaching this function; inventing a fallback would make those two states
// look like a real model capability.
func reasoningChoicesFor(opts reasoning.Options) []string {
	tiers := opts.Efforts
	if !opts.Supported {
		return nil
	}
	out := make([]string, 0, len(tiers)+1)
	if opts.CanDisable {
		out = append(out, offChoice)
	}
	return append(out, tiers...)
}

func hasReasoningControls(opts reasoning.Options) bool {
	return opts.Supported && (opts.CanDisable || len(opts.Efforts) > 0)
}

// acceptsEffort reports whether a typed selection can be stored. Off follows the
// same capability gate as active tiers; hiding it from the picker is insufficient
// because users can type `/reasoning set off` directly.
func acceptsEffort(level string, opts reasoning.Options) bool {
	if !opts.Supported {
		return false
	}
	if level == offChoice {
		return opts.CanDisable
	}
	return slices.Contains(opts.Efforts, level)
}
