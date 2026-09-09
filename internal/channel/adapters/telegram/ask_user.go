package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/i18n"
)

// Telegram ask_user rendering over the durable interaction state.
//
// There is deliberately NO adapter-side state machine here: every button
// callback carries the request UUID and is applied to the persistent
// userinput.TextInteractionState via Service.AdvanceInteraction (optimistic
// CAS). The card is then re-rendered from the returned state. This is the
// same state machine the plain-text fallback drives, so a pending ask_user
// survives process restarts and can even be finished from another surface.
//
// The only ephemeral piece is the force-reply text-prompt binding
// (askUserTextPrompts): losing it on restart merely means the user re-taps
// the "Fill answer" button; no answers are lost.
const askUserCallbackPrefix = "aui~"

type askUserCallback struct {
	Op        string
	RequestID string
	Locale    string
	Page      int
	QIndex    int
	OIndex    int
}

// askUserTextPrompt binds a sent force-reply prompt message to the request
// and question awaiting typed text.
type askUserTextPrompt struct {
	RequestID  string
	QuestionID string
	Locale     string
}

type askUserTextPromptStore struct {
	mu      sync.Mutex
	byMsgID map[string]askUserTextPrompt // "chatID:msgID" → prompt
}

func newAskUserTextPromptStore() *askUserTextPromptStore {
	return &askUserTextPromptStore{byMsgID: make(map[string]askUserTextPrompt)}
}

func (s *askUserTextPromptStore) put(chatID int64, msgID int, prompt askUserTextPrompt) {
	if s == nil || chatID == 0 || msgID == 0 || prompt.RequestID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byMsgID[textPromptKey(chatID, msgID)] = prompt
}

func (s *askUserTextPromptStore) take(chatID int64, msgID int) (askUserTextPrompt, bool) {
	if s == nil {
		return askUserTextPrompt{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := textPromptKey(chatID, msgID)
	prompt, ok := s.byMsgID[key]
	if ok {
		delete(s.byMsgID, key)
	}
	return prompt, ok
}

func textPromptKey(chatID int64, msgID int) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(msgID)
}

// compactAskUserID strips dashes so a UUID fits Telegram's 64-byte
// callback_data alongside op, locale, and indexes.
func compactAskUserID(requestID string) string {
	return strings.ReplaceAll(strings.TrimSpace(requestID), "-", "")
}

// expandAskUserID restores the canonical dashed UUID form. Returns "" when
// the compact form is not a 32-char hex string.
func expandAskUserID(compact string) string {
	compact = strings.TrimSpace(compact)
	if len(compact) != 32 {
		return ""
	}
	for _, r := range compact {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}

// Callback layout: aui~<op>~<id32>~<locale>[~args...]. Ops:
//
//	s <q> <opt>  single-select (auto-advance / toggle-off)
//	t <q> <opt>  multi-select toggle
//	x <q>        request free text (force-reply)
//	n <page>     navigate
//	go           submit with skips
func encodeAskUserCallback(op, requestID, locale string, args ...int) string {
	parts := []string{op, compactAskUserID(requestID), locale}
	for _, arg := range args {
		parts = append(parts, strconv.Itoa(arg))
	}
	return askUserCallbackPrefix + strings.Join(parts, "~")
}

func parseAskUserCallback(data string) (askUserCallback, bool) {
	data = strings.TrimSpace(data)
	if !strings.HasPrefix(data, askUserCallbackPrefix) {
		return askUserCallback{}, false
	}
	parts := strings.Split(strings.TrimPrefix(data, askUserCallbackPrefix), "~")
	if len(parts) < 3 {
		return askUserCallback{}, false
	}
	cb := askUserCallback{
		Op:        strings.TrimSpace(parts[0]),
		RequestID: expandAskUserID(parts[1]),
		Locale:    strings.TrimSpace(parts[2]),
	}
	if cb.Op == "" || cb.RequestID == "" {
		return askUserCallback{}, false
	}
	intArg := func(idx int) (int, bool) {
		value, err := strconv.Atoi(parts[idx])
		return value, err == nil && value >= 0
	}
	switch cb.Op {
	case "n":
		if len(parts) != 4 {
			return askUserCallback{}, false
		}
		page, ok := intArg(3)
		if !ok {
			return askUserCallback{}, false
		}
		cb.Page = page
		return cb, true
	case "s", "t":
		if len(parts) != 5 {
			return askUserCallback{}, false
		}
		qi, okQ := intArg(3)
		oi, okO := intArg(4)
		if !okQ || !okO {
			return askUserCallback{}, false
		}
		cb.QIndex, cb.OIndex = qi, oi
		return cb, true
	case "x":
		if len(parts) != 4 {
			return askUserCallback{}, false
		}
		qi, ok := intArg(3)
		if !ok {
			return askUserCallback{}, false
		}
		cb.QIndex = qi
		return cb, true
	case "go":
		return cb, len(parts) == 3
	default:
		return askUserCallback{}, false
	}
}

// parseAskUserToolCall extracts the pending request ID and canonical payload
// from an ask_user tool-call start event. The payload decoder is the shared
// read-side entry point (PayloadFromStored) so Telegram can never drift from
// what the service persisted.
func parseAskUserToolCall(tc *channel.StreamToolCall) (requestID string, payload userinput.UIPayload, ok bool) {
	if tc == nil || !strings.EqualFold(strings.TrimSpace(tc.Name), "ask_user") {
		return "", userinput.UIPayload{}, false
	}
	in, _ := tc.Input.(map[string]any)
	if in == nil {
		return "", userinput.UIPayload{}, false
	}
	requestID = strings.TrimSpace(asString(in["user_input_id"]))
	payload = userinput.PayloadFromStored(in["payload"])
	return requestID, payload, requestID != "" && len(payload.Questions) > 0
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}

// askUserAnswered reports whether the question has a real (non-skip) answer.
// Persisted skip entries render as unanswered so the user can still fill them
// after navigating back.
func askUserAnswered(state userinput.TextInteractionState, questionID string) (userinput.QuestionAnswer, bool) {
	answer, ok := state.Answer(questionID)
	if !ok || answer.Skipped {
		return userinput.QuestionAnswer{}, false
	}
	return answer, true
}

// renderAskUserPage builds the card body and inline keyboard for the current
// question of the durable interaction state. Each question is one page.
// Choice buttons show selection state (✓ prefix); free text and custom values
// stay in the body because they cannot fit in a button label.
func renderAskUserPage(requestID string, loc *i18n.Localizer, payload userinput.UIPayload, state userinput.TextInteractionState) (text string, actions []channel.Action) {
	if len(payload.Questions) == 0 {
		return loc.T("cmd.userInput.inputRequested"), nil
	}
	page := state.QuestionIndex
	if page < 0 {
		page = 0
	}
	if page >= len(payload.Questions) {
		page = len(payload.Questions) - 1
	}
	q := payload.Questions[page]
	locale := loc.Locale()

	var b strings.Builder
	b.WriteString(q.Text)
	answer, answered := askUserAnswered(state, q.ID)
	if body := askUserBodyAnswer(q, answer, answered, loc); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}

	row := 0
	switch q.Kind {
	case userinput.QuestionKindSingleSelect, userinput.QuestionKindMultiSelect:
		op := "s"
		if q.Kind == userinput.QuestionKindMultiSelect {
			op = "t"
		}
		for oi, opt := range q.Options {
			label := opt.Label
			if askUserOptionSelected(q.Kind, answer, answered, opt.ID) {
				label = "✓ " + label
			}
			actions = append(actions, channel.Action{
				Type:  "user_input",
				Label: truncateAskUserLabel(label),
				Value: encodeAskUserCallback(op, requestID, locale, page, oi),
				Row:   row,
			})
			if (oi+1)%2 == 0 {
				row++
			}
		}
		if len(q.Options)%2 != 0 {
			row++
		}
		if q.AllowCustom {
			label := loc.T("cmd.userInput.otherOption")
			if answered && strings.TrimSpace(answer.CustomText) != "" {
				label = "✓ " + label
			}
			actions = append(actions, channel.Action{
				Type:  "user_input",
				Label: label,
				Value: encodeAskUserCallback("x", requestID, locale, page),
				Row:   row,
			})
			row++
		}
	default: // text
		label := loc.T("cmd.userInput.fillAnswer")
		if answered {
			label = loc.T("cmd.userInput.refillAnswer")
		}
		actions = append(actions, channel.Action{
			Type:  "user_input",
			Label: label,
			Value: encodeAskUserCallback("x", requestID, locale, page),
			Row:   row,
		})
		row++
	}

	if len(payload.Questions) == 1 {
		label := loc.T("cmd.userInput.submit")
		if !answered {
			label = loc.T("cmd.userInput.skip")
		}
		actions = append(actions, channel.Action{
			Type:  "user_input",
			Label: label,
			Value: encodeAskUserCallback("go", requestID, locale),
			Row:   row,
		})
		return b.String(), actions
	}

	if page > 0 {
		actions = append(actions, channel.Action{
			Type:  "user_input",
			Label: "←",
			Value: encodeAskUserCallback("n", requestID, locale, page-1),
			Row:   row,
		})
	}
	// Page indicator navigates to the same page; AdvanceInteraction reports
	// it unchanged, so tapping it never triggers a no-op message edit.
	actions = append(actions, channel.Action{
		Type:  "user_input",
		Label: fmt.Sprintf("%d/%d", page+1, len(payload.Questions)),
		Value: encodeAskUserCallback("n", requestID, locale, page),
		Row:   row,
	})
	if page < len(payload.Questions)-1 {
		label := loc.T("cmd.userInput.next") + " →"
		if !answered {
			label = loc.T("cmd.userInput.skip") + " →"
		}
		actions = append(actions, channel.Action{
			Type:  "user_input",
			Label: label,
			Value: encodeAskUserCallback("n", requestID, locale, page+1),
			Row:   row,
		})
	} else {
		label := loc.T("cmd.userInput.submit")
		if !answered {
			label = loc.T("cmd.userInput.skipAndSubmit")
		}
		actions = append(actions, channel.Action{
			Type:  "user_input",
			Label: label,
			Value: encodeAskUserCallback("go", requestID, locale),
			Row:   row,
		})
	}
	return b.String(), actions
}

func askUserOptionSelected(kind string, answer userinput.QuestionAnswer, answered bool, optionID string) bool {
	if !answered {
		return false
	}
	if kind == userinput.QuestionKindSingleSelect {
		return len(answer.OptionIDs) == 1 && answer.OptionIDs[0] == optionID && answer.CustomText == ""
	}
	for _, id := range answer.OptionIDs {
		if id == optionID {
			return true
		}
	}
	return false
}

// askUserBodyAnswer echoes free-text / custom answers into the card body.
func askUserBodyAnswer(q userinput.UIQuestion, answer userinput.QuestionAnswer, answered bool, loc *i18n.Localizer) string {
	if !answered {
		return ""
	}
	if q.Kind == userinput.QuestionKindText {
		return strings.TrimSpace(answer.Text)
	}
	if custom := strings.TrimSpace(answer.CustomText); custom != "" {
		return loc.T("cmd.userInput.customAnswerLabel") + ": " + custom
	}
	return ""
}

// askUserAnswerLabel renders the final value for one question in the
// submitted summary.
func askUserAnswerLabel(q userinput.UIQuestion, state userinput.TextInteractionState) string {
	answer, ok := askUserAnswered(state, q.ID)
	if !ok {
		return ""
	}
	if text := strings.TrimSpace(answer.Text); text != "" {
		return text
	}
	parts := make([]string, 0, len(answer.OptionIDs)+1)
	for _, id := range answer.OptionIDs {
		label := id
		if opt, ok := q.Option(id); ok {
			label = opt.Label
		}
		parts = append(parts, label)
	}
	if custom := strings.TrimSpace(answer.CustomText); custom != "" {
		parts = append(parts, custom)
	}
	return strings.Join(parts, ", ")
}

// formatAskUserSubmittedSummary replaces the active page with a stable record
// of every question and final answer once the request is submitted.
func formatAskUserSubmittedSummary(loc *i18n.Localizer, payload userinput.UIPayload, state userinput.TextInteractionState) string {
	if len(payload.Questions) == 0 {
		return loc.T("cmd.userInput.inputRequested")
	}
	var b strings.Builder
	for i, q := range payload.Questions {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(q.Text)
		b.WriteString("\n")
		answer := askUserAnswerLabel(q, state)
		if answer == "" {
			answer = loc.T("cmd.userInput.skipped")
		}
		b.WriteString(answer)
	}
	return b.String()
}

func truncateAskUserLabel(label string) string {
	label = strings.TrimSpace(label)
	// Telegram button labels are soft-limited; keep readable.
	const maxRunes = 40
	runes := []rune(label)
	if len(runes) <= maxRunes {
		return label
	}
	return string(runes[:maxRunes-1]) + "…"
}

// askUserRejectToast maps a structured rejection to a localized toast.
func askUserRejectToast(loc *i18n.Localizer, reject userinput.InteractionReject) string {
	switch reject {
	case userinput.RejectNone:
		return ""
	case userinput.RejectEmptyText:
		return loc.T("cmd.userInput.answerRequired")
	case userinput.RejectCustomNotAllowed:
		return loc.T("cmd.userInput.customNotAllowed")
	default:
		return loc.T("cmd.userInput.invalidOperation")
	}
}

// prepareTelegramAskUser builds the initial card for an outbound ask_user
// tool call: page 0 of an empty interaction. Returns ok=false when the tool
// call is not a renderable ask_user prompt.
func prepareTelegramAskUser(tc *channel.StreamToolCall) (text string, actions []channel.Action, requestID string, ok bool) {
	requestID, payload, parsed := parseAskUserToolCall(tc)
	if !parsed {
		return "", nil, "", false
	}
	loc := i18n.New(tc.Locale)
	text, actions = renderAskUserPage(requestID, loc, payload, userinput.TextInteractionState{})
	return text, actions, requestID, true
}
