package input

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

const maxTextInteractionRetries = 3

// AdvanceText consumes one plain-text reply without submitting the request.
// The final answer set is persisted first so a process crash before Submit can
// resume the same completion instead of losing earlier questions.
func (s *Service) AdvanceText(ctx context.Context, input AdvanceTextInput) (AdvanceTextResult, error) {
	if s == nil || s.queries == nil {
		return AdvanceTextResult{}, errors.New("user input queries not configured")
	}
	resolve := ResolveInput{
		BotID:                  input.BotID,
		SessionID:              input.SessionID,
		ExplicitID:             input.ExplicitID,
		ReplyExternalMessageID: input.ReplyExternalMessageID,
	}
	for attempt := 0; attempt < maxTextInteractionRetries; attempt++ {
		req, err := s.resolveInteractionTarget(ctx, resolve)
		if errors.Is(err, ErrNotFound) {
			return AdvanceTextResult{Handled: false}, nil
		}
		if err != nil {
			return AdvanceTextResult{}, err
		}
		// Once selected, retries must stay on this request. Otherwise another
		// pending ask_user created between CAS attempts could consume the reply.
		if strings.TrimSpace(resolve.ExplicitID) == "" {
			resolve.ExplicitID = req.ID
		}
		state, invalid, changed, err := advanceTextState(req.UIPayload, req.Interaction, input.Text)
		if err != nil {
			return AdvanceTextResult{}, err
		}
		if invalid || !changed {
			req.Interaction = state
			return AdvanceTextResult{Handled: true, Invalid: invalid, Request: req}, nil
		}
		updated, ok, err := s.persistInteraction(ctx, req, state)
		if err != nil {
			return AdvanceTextResult{}, err
		}
		if ok {
			return AdvanceTextResult{Handled: true, Request: updated}, nil
		}
	}
	return AdvanceTextResult{}, errors.New("user input changed concurrently; retry the reply")
}

func advanceTextState(payload UIPayload, state TextInteractionState, raw string) (TextInteractionState, bool, bool, error) {
	if len(payload.Questions) == 0 {
		return state, false, false, errors.New("user input has no questions")
	}
	state = normalizeTextInteraction(payload, state)
	command := strings.ToLower(strings.TrimSpace(raw))
	if isBackCommand(command) {
		if state.QuestionIndex == 0 {
			return state, false, false, nil
		}
		state.QuestionIndex--
		state.Completed = false
		return state, false, true, nil
	}
	if state.Completed {
		return state, false, false, nil
	}
	question := payload.Questions[state.QuestionIndex]
	answer := QuestionAnswer{QuestionID: question.ID}
	if isSkipCommand(command) {
		if questionIsExplicitlyRequired(question) {
			return state, true, false, nil
		}
		answer.Skipped = true
	} else {
		var err error
		answer, err = parseTextAnswer(question, raw)
		if err != nil {
			return state, true, false, nil
		}
	}
	state.Answers = putTextAnswer(state.Answers, answer)
	if state.QuestionIndex == len(payload.Questions)-1 {
		state = completeOrFocusRequired(payload, state)
	} else {
		state.QuestionIndex++
	}
	return state, false, true, nil
}

func normalizeTextInteraction(payload UIPayload, state TextInteractionState) TextInteractionState {
	if state.QuestionIndex < 0 {
		state.QuestionIndex = 0
	}
	if state.QuestionIndex >= len(payload.Questions) {
		state.QuestionIndex = len(payload.Questions) - 1
	}
	valid := make(map[string]struct{}, len(payload.Questions))
	for _, question := range payload.Questions {
		valid[question.ID] = struct{}{}
	}
	answers := make([]QuestionAnswer, 0, len(state.Answers))
	seen := map[string]struct{}{}
	for _, answer := range state.Answers {
		if _, ok := valid[answer.QuestionID]; !ok {
			continue
		}
		if _, ok := seen[answer.QuestionID]; ok {
			continue
		}
		seen[answer.QuestionID] = struct{}{}
		answers = append(answers, answer)
	}
	state.Answers = answers
	return state
}

func completeOrFocusRequired(payload UIPayload, state TextInteractionState) TextInteractionState {
	for index, question := range payload.Questions {
		if !questionIsExplicitlyRequired(question) {
			continue
		}
		answer, ok := state.Answer(question.ID)
		if !ok || answer.Skipped {
			state.QuestionIndex = index
			state.Completed = false
			return state
		}
	}
	have := make(map[string]struct{}, len(state.Answers))
	for _, answer := range state.Answers {
		have[answer.QuestionID] = struct{}{}
	}
	for _, question := range payload.Questions {
		if _, ok := have[question.ID]; !ok && !questionIsExplicitlyRequired(question) {
			state.Answers = append(state.Answers, QuestionAnswer{QuestionID: question.ID, Skipped: true})
		}
	}
	state.Completed = true
	return state
}

func questionIsExplicitlyRequired(question UIQuestion) bool {
	return question.Required != nil && *question.Required
}

func parseTextAnswer(question UIQuestion, raw string) (QuestionAnswer, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return QuestionAnswer{}, errors.New("answer is required")
	}
	answer := QuestionAnswer{QuestionID: question.ID}
	switch question.Kind {
	case QuestionKindText:
		answer.Text = text
	case QuestionKindSingleSelect:
		if optionID, ok := matchTextOption(question, text); ok {
			answer.OptionIDs = []string{optionID}
		} else if question.AllowCustom {
			answer.CustomText = text
		} else {
			return QuestionAnswer{}, fmt.Errorf("answer %q does not match an option", text)
		}
	case QuestionKindMultiSelect:
		parts := splitTextSelections(text)
		if len(parts) == 0 {
			if question.AllowCustom {
				return QuestionAnswer{QuestionID: question.ID, CustomText: text}, nil
			}
			return QuestionAnswer{}, errors.New("at least one selection is required")
		}
		seen := map[string]struct{}{}
		for _, part := range parts {
			if optionID, ok := matchTextOption(question, part); ok {
				if _, exists := seen[optionID]; !exists {
					seen[optionID] = struct{}{}
					answer.OptionIDs = append(answer.OptionIDs, optionID)
				}
				continue
			}
			if question.AllowCustom {
				// A sentence may contain commas or option words. If it is not
				// entirely a selection list, return the whole reply to the LLM.
				return QuestionAnswer{QuestionID: question.ID, CustomText: text}, nil
			}
			return QuestionAnswer{}, fmt.Errorf("selection %q does not match an option", part)
		}
	default:
		return QuestionAnswer{}, fmt.Errorf("unsupported question kind %q", question.Kind)
	}
	return answer, nil
}

func matchTextOption(question UIQuestion, raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if number, err := strconv.Atoi(text); err == nil && number >= 1 && number <= len(question.Options) {
		return question.Options[number-1].ID, true
	}
	for _, option := range question.Options {
		if strings.EqualFold(text, strings.TrimSpace(option.ID)) || strings.EqualFold(text, strings.TrimSpace(option.Label)) {
			return option.ID, true
		}
	}
	return "", false
}

func splitTextSelections(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' || unicode.Is(unicode.Zl, r) || r == '\n' || r == '\r'
	})
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		if part := strings.TrimSpace(field); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func putTextAnswer(answers []QuestionAnswer, answer QuestionAnswer) []QuestionAnswer {
	out := append([]QuestionAnswer(nil), answers...)
	for idx := range out {
		if out[idx].QuestionID == answer.QuestionID {
			out[idx] = answer
			return out
		}
	}
	return append(out, answer)
}

func isBackCommand(text string) bool {
	switch text {
	case "back", "返回", "上一步", "戻る":
		return true
	default:
		return false
	}
}

func isSkipCommand(text string) bool {
	switch text {
	case "skip", "跳过", "略过", "スキップ":
		return true
	default:
		return false
	}
}
