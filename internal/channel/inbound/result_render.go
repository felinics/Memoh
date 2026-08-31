package inbound

import (
	"context"
	"fmt"
	"strings"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/models"
)

const actionTypeCallback = "callback"

// RenderContext bundles everything the renderer needs to turn a neutral
// command.Result into a channel.Message: the target channel's capability matrix
// and a Localizer for the renderer's own chrome (Close/Prev/Next/…). Business
// copy is already localized by the command layer; the renderer only localizes
// the chrome it owns.
type RenderContext struct {
	Caps channel.ChannelCapabilities
	T    *i18n.Localizer
}

// friendlyOps renders a blame-free, recovery-oriented line for an operational
// failure on a user-command path. The raw cause belongs in logs; the user never
// sees infra nouns ("route resolver", "session service", etc.). verbKey is an
// i18n key under "ops.verb.*"; t localizes both the verb and the template.
func friendlyOps(t *i18n.Localizer, verbKey string) string {
	return t.T("ops.template", map[string]any{"verb": t.T(verbKey)})
}

func reasoningLabel(t *i18n.Localizer, cc command.CurrentContext) string {
	reasoning := strings.TrimSpace(cc.ReasoningEffort)
	if models.IsReasoningDisabled(reasoning) {
		return t.T("cmd.common.off")
	}
	if reasoning == "" {
		return t.T("cmd.common.on")
	}
	return reasoning
}

// appendCurrentContextLines adds the model/reasoning/context summary
// shown after /new resets a session.
func appendCurrentContextLines(b *strings.Builder, t *i18n.Localizer, cc command.CurrentContext) {
	fmt.Fprintf(b, "\n\n- %s: %s", t.T("newSession.labelModel"), cc.ChatModel)
	fmt.Fprintf(b, "\n- %s: %s", t.T("newSession.labelReasoning"), reasoningLabel(t, cc))
	if cw := strings.TrimSpace(cc.ContextWindow); cw != "" {
		fmt.Fprintf(b, "\n- %s: %s %s", t.T("newSession.labelContext"), cw, t.T("newSession.contextUnit"))
	}
}

// formatStartWelcomeMessage builds the /start doorway greeting. It welcomes the
// user and invites the first message — it does not reset history or dump bot
// configuration (that is /new's job after a fresh session is created).
func formatStartWelcomeMessage(t *i18n.Localizer, botUsername string) string {
	var b strings.Builder
	b.WriteString(command.MdBold(t.T("startWelcome.title")))
	b.WriteString("\n\n")
	b.WriteString(t.T("startWelcome.intro"))
	fmt.Fprintf(&b, "\n\n%s", t.T("startWelcome.tip", map[string]any{
		"new":  command.CommandText("new", ""),
		"help": command.CommandText("help", ""),
	}))
	appendTelegramGroupCommandTip(&b, t, botUsername)
	return b.String()
}

// formatNewSessionMessage builds the /new confirmation card. A fresh start is an
// orientation moment, so it confirms the full setup the user is departing with —
// which model (and its provider) will answer, whether reasoning is on, and how
// much context budget they have. These are not "defaults to hide"; on this
// surface they reassure and inform. Markdown markers are authored unconditionally
// and stripped later for non-markdown channels.
func formatNewSessionMessage(t *i18n.Localizer, modeLabel string, cc command.CurrentContext, botUsername string) string {
	var b strings.Builder
	b.WriteString(command.MdBold(t.T("newSession.title", map[string]any{"mode": modeLabel})))
	appendCurrentContextLines(&b, t, cc)
	fmt.Fprintf(&b, "\n\n%s", t.T("newSession.tip", map[string]any{
		"model":     command.CommandText("model", ""),
		"reasoning": command.CommandText("reasoning", ""),
	}))
	appendTelegramGroupCommandTip(&b, t, botUsername)
	return b.String()
}

// telegramGroupBotUsername returns the Bot API username only where Telegram's
// group privacy rules make explicit /command@bot addressing useful. Other
// channels use different addressing syntax, and private chats do not need it.
// Missing or malformed metadata intentionally degrades to no tip: command
// execution remains valid, while this renderer stays independent of adapters.
func telegramGroupBotUsername(msg channel.InboundMessage) string {
	if msg.Channel != channel.ChannelTypeTelegram || msg.Conversation.Type != channel.ConversationTypeGroup {
		return ""
	}
	username, _ := msg.Metadata["bot_username"].(string)
	return strings.TrimPrefix(strings.TrimSpace(username), "@")
}

func appendTelegramGroupCommandTip(b *strings.Builder, t *i18n.Localizer, botUsername string) {
	if tip := command.TelegramGroupCommandTip(t, botUsername); tip != "" {
		fmt.Fprintf(b, "\n\n%s", tip)
	}
}

// localizer resolves the command-UI Localizer for a bot, used for renderer
// chrome and operational-failure messages outside the command handler (e.g.
// /new, /stop, /status). It mirrors the locale the command handler resolves, so
// a bot's whole command surface stays in one language. Falls back to the default
// locale when the command handler or bot is unavailable.
func (p *ChannelInboundProcessor) localizer(ctx context.Context, botID string) *i18n.Localizer {
	if p == nil || p.commandHandler == nil {
		return i18n.New("")
	}
	return i18n.New(p.commandHandler.ResolveLocale(ctx, strings.TrimSpace(botID)))
}

// localizerFor returns the Localizer the renderer should use for chrome. The
// command layer stamps Result.Locale with the locale its text/labels were
// rendered in (including an in-place language switch, where it differs from the
// pre-command locale), so chrome must follow Result.Locale to keep the whole
// reply in one language. RenderContext.T is only a fallback for results that
// carry no locale.
func (rc RenderContext) localizerFor(result *command.Result) *i18n.Localizer {
	if result != nil && result.Locale != "" {
		return i18n.New(result.Locale)
	}
	if rc.T != nil {
		return rc.T
	}
	return i18n.New("")
}

// renderResult converts a neutral command.Result into a channel.Message,
// upgrading to interactive inline-keyboard buttons when the channel advertises
// button support. Channels without button support (or results without
// structured data) degrade to the complete fallback Text, with a derived
// "typeable affordances" trailer appended so users on no-button channels can
// still discover and invoke what the buttons would have done. Inbound picks
// Format here; outbound coerceFormatForCaps re-validates before send. The
// renderer's own chrome (Close/Prev/Next/…) is localized to Result.Locale.
func renderResult(result *command.Result, rc RenderContext) channel.Message {
	if result == nil {
		return channel.Message{}
	}
	t := rc.localizerFor(result)
	if result.FeedbackError != nil {
		return applyMessageFormat(channel.Message{Text: renderACPFeedbackText(result.FeedbackError, t)}, rc.Caps)
	}
	var msg channel.Message
	if result.Interactive == nil || !rc.Caps.Buttons {
		msg = channel.Message{Text: appendFallbackTrailer(result.Text, result.Interactive, rc.Caps, t)}
	} else {
		switch result.Interactive.Kind {
		case command.InteractiveList:
			msg = renderListView(result.Text, result.Interactive.List, t)
		case command.InteractiveModelPicker:
			msg = renderModelPicker(result.Text, result.Interactive.Picker, t)
		case command.InteractiveChoices:
			msg = renderChoicesView(result.Text, result.Interactive.Choices, t)
		case command.InteractiveRange:
			msg = renderRangeView(result.Text, result.Interactive.Range, t)
		default:
			msg = channel.Message{Text: result.Text}
		}
	}
	return applyMessageFormat(msg, rc.Caps)
}

func renderACPFeedbackText(feedback *agentfeedback.Error, t *i18n.Localizer) string {
	if feedback == nil {
		return ""
	}
	text := strings.TrimSpace(feedback.Message)
	if key := strings.TrimSpace(feedback.I18nKey); key != "" && t != nil {
		params := make(map[string]any, len(feedback.Args))
		for name, value := range feedback.Args {
			params[name] = value
		}
		localized := t.T(key, params)
		if strings.TrimSpace(localized) != "" && localized != key {
			text = localized
		}
	}
	if text == "" {
		text = strings.TrimSpace(feedback.Code)
	}
	return text
}

// appendFallbackTrailer adds a typeable-command guide derived from Interactive
// when buttons aren't available (or absent). Telegram-class channels (with
// buttons) take a different code path and never enter here, so the trailer
// never appears alongside live buttons.
func appendFallbackTrailer(text string, iv *command.Interactive, caps channel.ChannelCapabilities, t *i18n.Localizer) string {
	if caps.Buttons || iv == nil {
		return text
	}
	trailer := command.FallbackTrailer(iv, t)
	if trailer == "" {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return trailer
	}
	// TrimRight on the body so a body that already ends in "\n" doesn't
	// produce a triple-newline gap before the trailer.
	return strings.TrimRight(text, "\n") + "\n\n" + trailer
}

// applyMessageFormat picks Format and inline-markup handling based on what
// the channel can render: Markdown for capable channels (Telegram et al.),
// Plain elsewhere with `**` / backticks stripped from the body upstream.
//
// The outbound layer's coerceFormatForCaps is the authoritative defense
// against an auto-promoted Markdown reaching a plain-text-only channel; this
// inbound step is the in-process companion that picks the right shape from
// the start so the body the user sees matches what we render here.
func applyMessageFormat(msg channel.Message, caps channel.ChannelCapabilities) channel.Message {
	if caps.Markdown || caps.RichText {
		msg.Format = channel.MessageFormatMarkdown
	} else {
		msg.Text = channel.StripInlineMarkup(msg.Text)
		msg.Format = channel.MessageFormatPlain
	}
	return msg
}

// plainTextMessage builds a fully-formed channel.Message from `text`, routing
// through applyMessageFormat so the body's inline markup matches what the
// target channel can render. Convenience wrapper for operational replies
// constructed outside the main renderResult path (e.g. /new, /stop, ops
// errors) — keeps every such reply consistent with the renderer's contract.
func plainTextMessage(text string, caps channel.ChannelCapabilities) channel.Message {
	return applyMessageFormat(channel.Message{Text: text}, caps)
}

// renderListView renders a paginated list. The list content lives in the
// message text; buttons are added only for navigation (Prev/Next + Close) when
// there is more than one page, or for rows that carry an explicit ItemAction.
// A single-page, action-free list renders as plain text (no keyboard), matching
// prior behavior.
func renderListView(text string, lv *command.ListView, t *i18n.Localizer) channel.Message {
	if lv != nil && strings.TrimSpace(lv.ButtonText) != "" {
		text = lv.ButtonText
	}
	msg := channel.Message{Text: text}
	if lv == nil {
		return msg
	}

	pageSize := lv.PageSize
	if pageSize <= 0 {
		pageSize = 12
	}
	totalPages := 1
	if pageSize > 0 {
		totalPages = (lv.Total + pageSize - 1) / pageSize
	}

	// Clamp the page index used for nav rendering into [0, totalPages-1] in a
	// local variable; do not mutate lv (a caller-owned pointer). A stale
	// callback from a keyboard captured before the list shrunk (rows deleted
	// on another device) would otherwise emit a broken nav row — "11/3"
	// counter, Prev encoding a Page-1 jump backward into nowhere — and Next
	// would silently vanish. Mirrors the clamp in renderModelPicker.
	page := lv.Page
	if page < 0 {
		page = 0
	}
	if totalPages > 0 && page > totalPages-1 {
		page = totalPages - 1
	}

	var actions []channel.Action
	row := 0

	for _, item := range lv.Items {
		if item.Action == nil && item.Callback == "" {
			continue
		}
		label := item.Label
		if item.Selected {
			label = "✓ " + label
		}
		value := item.Callback
		if value == "" {
			value = command.EncodeListCallback(item.Action.Resource, item.Action.Action, item.Action.Args, 0)
		}
		actions = append(actions, channel.Action{
			Type:  actionTypeCallback,
			Label: truncateButtonLabel(label),
			Value: value,
			Row:   row,
		})
		row++
	}

	// Contextual entry buttons below the data rows (e.g. "All commands").
	if len(lv.ExtraActions) > 0 {
		col := 0
		for _, ea := range lv.ExtraActions {
			if ea.Action == nil {
				continue
			}
			actions = append(actions, channel.Action{
				Type:  actionTypeCallback,
				Label: truncateButtonLabel(ea.Label),
				Value: command.EncodeListCallback(ea.Action.Resource, ea.Action.Action, ea.Action.Args, 0),
				Row:   row,
			})
			col++
			if col == 2 {
				col = 0
				row++
			}
		}
		if col != 0 {
			row++
		}
	}

	if totalPages <= 1 && len(actions) == 0 {
		return msg
	}

	if totalPages > 1 {
		navRow := row
		if page > 0 {
			actions = append(actions, channel.Action{
				Type:  actionTypeCallback,
				Label: t.T("chrome.prev"),
				Value: command.EncodeListCallback(lv.Resource, lv.Action, lv.Args, page-1),
				Row:   navRow,
			})
		}
		actions = append(actions, channel.Action{
			Type:  actionTypeCallback,
			Label: fmt.Sprintf("%d/%d", page+1, totalPages),
			Value: command.NoopCallback(),
			Row:   navRow,
		})
		if page < totalPages-1 {
			actions = append(actions, channel.Action{
				Type:  actionTypeCallback,
				Label: t.T("chrome.next"),
				Value: command.EncodeListCallback(lv.Resource, lv.Action, lv.Args, page+1),
				Row:   navRow,
			})
		}
		row++
	}

	actions = append(actions, channel.Action{
		Type:  actionTypeCallback,
		Label: t.T("chrome.close"),
		Value: command.DismissCallback(),
		Row:   row,
	})

	msg.Actions = actions
	return msg
}

// renderModelPicker renders the two-level model picker. On button channels the
// message body is a compact status header (current model + reasoning, or the
// provider + page range) — not the flat fallback list. The provider level shows
// a 2-column grid (● marks the provider holding the current model, with its
// model count); the model level shows one model per row (✓ marks the selected
// model) with a back button. Both levels paginate and carry a Close button.
//
// fallbackText guards against a partial Interactive (Kind=ModelPicker but
// Picker=nil). No production caller constructs that today, but if a future
// refactor sets Kind before populating the view, the alternative is a
// p.Level nil-dereference panic in modelPickerHeader. Cheap structural
// safety net; the corresponding coverage-gate test row enforces it.
func renderModelPicker(fallbackText string, p *command.ModelPickerView, t *i18n.Localizer) channel.Message {
	if p == nil {
		return channel.Message{Text: fallbackText}
	}
	pageSize := p.PageSize
	if pageSize <= 0 {
		pageSize = 8
	}
	totalPages := 1
	if p.Total > 0 {
		totalPages = (p.Total + pageSize - 1) / pageSize
	}
	page := p.Page
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := page * pageSize
	end := start + pageSize

	msg := channel.Message{Text: modelPickerHeader(p, start, end, t)}

	var actions []channel.Action
	row := 0

	switch p.Level {
	case command.LevelProviders:
		if end > len(p.Providers) {
			end = len(p.Providers)
		}
		col := 0
		for i := start; i < end; i++ {
			prov := p.Providers[i]
			label := fmt.Sprintf("%s (%d)", prov.Name, prov.Count)
			if prov.HasCurrent {
				label += " ●"
			}
			actions = append(actions, channel.Action{
				Type:  actionTypeCallback,
				Label: truncateButtonLabel(label),
				Value: command.EncodeModelProviderCallback(prov.Index, 0),
				Row:   row,
			})
			col++
			if col == 2 {
				col = 0
				row++
			}
		}
		if col != 0 {
			row++
		}
	case command.LevelModels:
		if end > len(p.Models) {
			end = len(p.Models)
		}
		for i := start; i < end; i++ {
			m := p.Models[i]
			label := m.Name
			if m.Selected {
				label = "✓ " + label
			}
			actions = append(actions, channel.Action{
				Type:  actionTypeCallback,
				Label: truncateButtonLabel(label),
				Value: command.EncodeModelSelectCallback(m.DBID),
				Row:   row,
			})
			row++
		}
	}

	if totalPages > 1 {
		navRow := row
		if page > 0 {
			actions = append(actions, channel.Action{
				Type: actionTypeCallback, Label: t.T("chrome.prev"),
				Value: pickerPageCallback(p, page-1), Row: navRow,
			})
		}
		actions = append(actions, channel.Action{
			Type: actionTypeCallback, Label: fmt.Sprintf("%d/%d", page+1, totalPages),
			Value: command.NoopCallback(), Row: navRow,
		})
		if page < totalPages-1 {
			actions = append(actions, channel.Action{
				Type: actionTypeCallback, Label: t.T("chrome.next"),
				Value: pickerPageCallback(p, page+1), Row: navRow,
			})
		}
		row++
	}

	if p.Level == command.LevelModels {
		actions = append(actions, channel.Action{
			Type: actionTypeCallback, Label: t.T("chrome.providers"),
			Value: command.EncodeListCallback("model", "list", nil, 0), Row: row,
		})
		row++
	}
	actions = append(actions, channel.Action{
		Type: actionTypeCallback, Label: t.T("chrome.close"),
		Value: command.DismissCallback(), Row: row,
	})

	msg.Actions = actions
	return msg
}

// modelPickerHeader builds the compact status header shown above the keyboard.
func modelPickerHeader(p *command.ModelPickerView, start, end int, t *i18n.Localizer) string {
	var b strings.Builder
	b.WriteString(command.MdBold(t.T("chrome.modelConfiguration")) + "\n\n")
	if p.Level == command.LevelModels {
		provider := p.ProviderName
		if provider == "" {
			provider = t.T("chrome.models")
		}
		if p.Total > 0 && end > p.Total {
			end = p.Total
		}
		if p.Total > end-start {
			fmt.Fprintf(&b, "%s\n\n%s", t.T("chrome.providerRange", map[string]any{
				"provider": provider, "start": start + 1, "end": end, "total": p.Total,
			}), t.T("chrome.selectModel"))
		} else {
			fmt.Fprintf(&b, "%s\n\n%s", t.T("chrome.provider", map[string]any{"provider": provider}), t.T("chrome.selectModel"))
		}
		return b.String()
	}
	current := p.CurrentDisplay
	if strings.TrimSpace(current) == "" {
		current = t.T("chrome.none")
	}
	// Display names / enums read better plain than monospace.
	fmt.Fprintf(&b, "%s\n", t.T("chrome.currentModel", map[string]any{"model": current}))
	if r := strings.TrimSpace(p.Reasoning); r != "" {
		fmt.Fprintf(&b, "%s\n", t.T("chrome.reasoningLine", map[string]any{"effort": r}))
	}
	b.WriteString("\n" + t.T("chrome.selectProvider"))
	return b.String()
}

// pickerPageCallback builds the callback_data for paginating within the current
// picker level.
func pickerPageCallback(p *command.ModelPickerView, page int) string {
	if p.Level == command.LevelModels {
		return command.EncodeModelProviderCallback(p.ProviderIndex, page)
	}
	return command.EncodeListCallback("model", "list", nil, page)
}

// truncateButtonLabel keeps inline-keyboard labels within Telegram's practical
// length so long model names don't overflow the button.
func truncateButtonLabel(s string) string {
	const maxLen = 60
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen-1]) + "…"
}

// renderChoicesView renders a flat set of one-tap choices (e.g. reasoning
// levels or settings toggles). Short labels share two columns; long labels fall
// back to one column so Telegram does not crush the text. Each tap re-dispatches
// "/{resource} {action} {args}" and edits in place.
//
// fallbackText guards against a partial Interactive (Kind=Choices but
// Choices=nil). No production caller constructs that today — same structural
// safety net as renderModelPicker. The alternative is a cv.Title / cv.Choices
// nil-dereference panic.
func renderChoicesView(fallbackText string, cv *command.ChoicesView, t *i18n.Localizer) channel.Message {
	if cv == nil {
		return channel.Message{Text: fallbackText}
	}
	msg := channel.Message{Text: cv.Title}
	var actions []channel.Action
	columns := choiceColumns(cv)
	col, row := 0, 0
	for _, item := range cv.Choices {
		if item.Action == nil {
			continue
		}
		label := item.Label
		if item.Selected {
			label = "✓ " + label
		}
		actions = append(actions, channel.Action{
			Type:  actionTypeCallback,
			Label: truncateButtonLabel(label),
			Value: command.EncodeListCallback(item.Action.Resource, item.Action.Action, item.Action.Args, 0),
			Row:   row,
		})
		col++
		if col == columns {
			col = 0
			row++
		}
	}
	if col != 0 {
		row++
	}
	actions = append(actions, channel.Action{
		Type: actionTypeCallback, Label: t.T("chrome.close"), Value: command.DismissCallback(), Row: row,
	})
	msg.Actions = actions
	return msg
}

// maxChoiceColumns caps how many inline-keyboard buttons share one row. Telegram
// sizes a button to (row width / buttons in that row) — it offers no absolute
// width control — so columns-per-row is the only lever to stop a 2-char label
// from stretching across the whole chat. 3 keeps short picks (languages,
// reasoning levels) compact without crushing the text.
const maxChoiceColumns = 3

func choiceColumns(cv *command.ChoicesView) int {
	if cv == nil {
		return 1
	}
	// An explicit column count is honored (clamped to maxChoiceColumns) so a
	// caller can pack short value-picks tighter than the auto heuristic would.
	if cv.Columns >= 1 {
		if cv.Columns > maxChoiceColumns {
			return maxChoiceColumns
		}
		return cv.Columns
	}
	// Auto (Columns unset): a long label takes its own row; otherwise pair up.
	for _, item := range cv.Choices {
		if len([]rune(item.Label)) > 18 {
			return 1
		}
	}
	return 2
}

// renderRangeView renders a time-window selector: one row of preset buttons
// (the active preset marked ●) plus Close. Tapping a preset re-runs the command
// with that --range and edits the message in place.
func renderRangeView(text string, rv *command.RangeView, t *i18n.Localizer) channel.Message {
	msg := channel.Message{Text: text}
	if rv == nil {
		return msg
	}
	var actions []channel.Action
	for _, preset := range rv.Presets {
		label := rangePresetLabel(preset, t)
		if preset == rv.Current {
			label += " ●"
		}
		actions = append(actions, channel.Action{
			Type:  actionTypeCallback,
			Label: label,
			Value: command.EncodeRangeCallback(rv.Resource, rv.Action, preset),
			Row:   0,
		})
	}
	actions = append(actions, channel.Action{
		Type: actionTypeCallback, Label: t.T("chrome.close"),
		Value: command.DismissCallback(), Row: 1,
	})
	msg.Actions = actions
	return msg
}

func rangePresetLabel(preset string, t *i18n.Localizer) string {
	if preset == "all" {
		return t.T("chrome.rangeAll")
	}
	return preset
}
