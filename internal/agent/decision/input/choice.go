package input

import (
	"fmt"
	"strings"
)

// ChoiceQuestion is a question in the shape an agent's own question tool
// asks it: text, the options to pick from, and whether several may be picked.
// Its JSON is what Claude Code's AskUserQuestion sends.
type ChoiceQuestion struct {
	Text        string         `json:"question"`
	Options     []ChoiceOption `json:"options"`
	MultiSelect bool           `json:"multiSelect"`
}

type ChoiceOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ChoiceQuestionsInput is the ask_user input for an agent's questions. One
// with too few options to choose from is asked as free text, and a choice
// still takes a typed answer.
func ChoiceQuestionsInput(questions []ChoiceQuestion) map[string]any {
	items := make([]any, 0, len(questions))
	for _, question := range questions {
		item := map[string]any{"text": question.Text, "kind": QuestionKindText}
		if len(question.Options) >= MinOptionsPerQuestion {
			options := make([]any, 0, min(len(question.Options), MaxOptionsPerQuestion))
			for _, option := range question.Options[:min(len(question.Options), MaxOptionsPerQuestion)] {
				options = append(options, map[string]any{"label": option.Label, "description": option.Description})
			}
			item["options"] = options
			item["allow_custom"] = true
			item["kind"] = QuestionKindSingleSelect
			if question.MultiSelect {
				item["kind"] = QuestionKindMultiSelect
			}
		}
		items = append(items, item)
	}
	return map[string]any{"questions": items}
}

// ChoiceAnswers reads the answer to each of count questions from a submitted
// request, in order: picked labels and typed text joined by ", ". It reports
// false when any question went unanswered.
func ChoiceAnswers(result map[string]any, count int) ([]string, bool) {
	byID := map[string]UIAnswer{}
	for _, answer := range AnswersFromResult(result) {
		byID[answer.QuestionID] = answer
	}
	answers := make([]string, count)
	for i := range answers {
		answer, ok := byID[fmt.Sprintf("q%d", i+1)]
		if !ok || answer.Skipped {
			return nil, false
		}
		var values []string
		for _, selected := range answer.Selected {
			values = append(values, selected.Label)
		}
		for _, text := range []string{answer.CustomText, answer.Text} {
			if text = strings.TrimSpace(text); text != "" {
				values = append(values, text)
			}
		}
		if len(values) == 0 {
			return nil, false
		}
		answers[i] = strings.Join(values, ", ")
	}
	return answers, true
}
