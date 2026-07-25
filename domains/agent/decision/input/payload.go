package input

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidAskUserInput = errors.New("invalid ask_user input")

// Allowed keys mirror the tool schema's additionalProperties:false. The
// parser is the only enforcement point a caller cannot skip — MCP clients and
// ACP runtimes do not necessarily validate against the JSON schema.
var (
	allowedPayloadKeys  = map[string]struct{}{"questions": {}}
	allowedQuestionKeys = map[string]struct{}{
		"text": {}, "kind": {}, "options": {}, "allow_custom": {}, "placeholder": {},
	}
	allowedOptionKeys = map[string]struct{}{"label": {}, "description": {}}
)

// ParseAskUserPayload is the single write-side entry point for ask_user
// arguments. It validates strictly (no aliases, no inference) and returns the
// canonical v2 payload with server-generated question/option IDs.
func ParseAskUserPayload(input any) (UIPayload, error) {
	raw := payloadObject(input)
	if len(raw) == 0 {
		return UIPayload{}, invalidf("input must be a JSON object with a questions array")
	}
	if err := rejectUnknownKeys(raw, allowedPayloadKeys, "input"); err != nil {
		return UIPayload{}, err
	}
	items, ok := raw["questions"].([]any)
	if !ok || len(items) == 0 {
		return UIPayload{}, invalidf("questions must be a non-empty array")
	}
	if len(items) > MaxQuestionsPerRequest {
		return UIPayload{}, invalidf("questions has %d items, the maximum is %d", len(items), MaxQuestionsPerRequest)
	}

	payload := UIPayload{
		Version:   PayloadVersion,
		Questions: make([]UIQuestion, 0, len(items)),
	}
	for idx, item := range items {
		question, err := parseQuestion(item, idx)
		if err != nil {
			return UIPayload{}, err
		}
		payload.Questions = append(payload.Questions, question)
	}
	return payload, nil
}

// ValidateAskUserInput reports whether the arguments form a valid ask_user
// payload. CreatePending still parses again and owns the write-side
// normalization boundary.
func ValidateAskUserInput(input any) error {
	_, err := ParseAskUserPayload(input)
	return err
}

func parseQuestion(item any, idx int) (UIQuestion, error) {
	obj, ok := item.(map[string]any)
	if !ok {
		return UIQuestion{}, invalidf("questions[%d] must be an object", idx)
	}
	if err := rejectUnknownKeys(obj, allowedQuestionKeys, fmt.Sprintf("questions[%d]", idx)); err != nil {
		return UIQuestion{}, err
	}
	text, err := strictString(obj, "text", fmt.Sprintf("questions[%d]", idx))
	if err != nil {
		return UIQuestion{}, err
	}
	question := UIQuestion{
		ID:   fmt.Sprintf("q%d", idx+1),
		Text: text,
	}
	if question.Text == "" {
		return UIQuestion{}, invalidf("questions[%d].text is required", idx)
	}

	kind, ok := obj["kind"].(string)
	if !ok {
		return UIQuestion{}, invalidf("questions[%d].kind is required and must be one of %q, %q, %q", idx, QuestionKindSingleSelect, QuestionKindMultiSelect, QuestionKindText)
	}
	question.Kind = strings.TrimSpace(kind)
	switch question.Kind {
	case QuestionKindSingleSelect, QuestionKindMultiSelect, QuestionKindText:
	default:
		return UIQuestion{}, invalidf("questions[%d].kind %q is invalid; use %q, %q, or %q", idx, question.Kind, QuestionKindSingleSelect, QuestionKindMultiSelect, QuestionKindText)
	}

	if value, exists := obj["allow_custom"]; exists {
		allowCustom, ok := value.(bool)
		if !ok {
			return UIQuestion{}, invalidf("questions[%d].allow_custom must be a boolean", idx)
		}
		question.AllowCustom = allowCustom
	}
	if question.Placeholder, err = strictString(obj, "placeholder", fmt.Sprintf("questions[%d]", idx)); err != nil {
		return UIQuestion{}, err
	}

	options, hasOptions := obj["options"]
	switch question.Kind {
	case QuestionKindText:
		if hasOptions && options != nil {
			return UIQuestion{}, invalidf("questions[%d] has kind %q and must not include options; use a select kind for choices", idx, QuestionKindText)
		}
		if question.AllowCustom {
			return UIQuestion{}, invalidf("questions[%d] has kind %q which is already free text; remove allow_custom", idx, QuestionKindText)
		}
	default:
		parsed, err := parseOptions(options, question.ID, idx)
		if err != nil {
			return UIQuestion{}, err
		}
		question.Options = parsed
	}
	return question, nil
}

func parseOptions(value any, questionID string, questionIdx int) ([]UIOption, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, invalidf("questions[%d].options is required for select questions", questionIdx)
	}
	if len(items) < MinOptionsPerQuestion {
		return nil, invalidf("questions[%d].options needs at least %d options", questionIdx, MinOptionsPerQuestion)
	}
	if len(items) > MaxOptionsPerQuestion {
		return nil, invalidf("questions[%d].options has %d items, the maximum is %d", questionIdx, len(items), MaxOptionsPerQuestion)
	}
	options := make([]UIOption, 0, len(items))
	for idx, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, invalidf("questions[%d].options[%d] must be an object with a label", questionIdx, idx)
		}
		path := fmt.Sprintf("questions[%d].options[%d]", questionIdx, idx)
		if err := rejectUnknownKeys(obj, allowedOptionKeys, path); err != nil {
			return nil, err
		}
		label, err := strictString(obj, "label", path)
		if err != nil {
			return nil, err
		}
		description, err := strictString(obj, "description", path)
		if err != nil {
			return nil, err
		}
		option := UIOption{
			ID:          fmt.Sprintf("%s.o%d", questionID, idx+1),
			Label:       label,
			Description: description,
		}
		if option.Label == "" {
			return nil, invalidf("%s.label is required", path)
		}
		options = append(options, option)
	}
	return options, nil
}

// strictString returns the trimmed string at key, erroring when the value is
// present but not a string — no fmt.Sprint coercion on the write side.
func strictString(obj map[string]any, key, path string) (string, error) {
	value, exists := obj[key]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", invalidf("%s.%s must be a string", path, key)
	}
	return strings.TrimSpace(text), nil
}

func rejectUnknownKeys(obj map[string]any, allowed map[string]struct{}, path string) error {
	for key := range obj {
		if _, ok := allowed[key]; !ok {
			return invalidf("%s has unknown field %q", path, key)
		}
	}
	return nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAskUserInput, fmt.Sprintf(format, args...))
}

// PayloadFromStored is the single read-side entry point. It decodes a stored
// or streamed ui_payload value, upgrading legacy (pre-v2) rows so the rest of
// the system only ever sees the canonical shape. It is tolerant: stored data
// must keep rendering even when it predates current validation.
func PayloadFromStored(value any) UIPayload {
	raw := payloadObject(value)
	if len(raw) == 0 {
		return UIPayload{Version: PayloadVersion}
	}
	if _, ok := raw["questions"].([]any); ok {
		return decodeStoredV2(raw)
	}
	return upgradeLegacyPayload(raw)
}

func decodeStoredV2(raw map[string]any) UIPayload {
	items, _ := raw["questions"].([]any)
	payload := UIPayload{
		Version:   PayloadVersion,
		Questions: make([]UIQuestion, 0, len(items)),
	}
	for idx, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		question := UIQuestion{
			ID:          stringValue(obj["id"]),
			Text:        stringValue(obj["text"]),
			Kind:        stringValue(obj["kind"]),
			AllowCustom: boolValue(obj["allow_custom"]),
			Placeholder: stringValue(obj["placeholder"]),
		}
		if question.ID == "" {
			question.ID = fmt.Sprintf("q%d", idx+1)
		}
		if question.Kind == "" {
			question.Kind = QuestionKindText
		}
		if options, ok := obj["options"].([]any); ok {
			for optIdx, optItem := range options {
				optObj, ok := optItem.(map[string]any)
				if !ok {
					continue
				}
				option := UIOption{
					ID:          stringValue(optObj["id"]),
					Label:       stringValue(optObj["label"]),
					Description: stringValue(optObj["description"]),
				}
				if option.ID == "" {
					option.ID = fmt.Sprintf("%s.o%d", question.ID, optIdx+1)
				}
				if option.Label == "" {
					option.Label = option.ID
				}
				question.Options = append(question.Options, option)
			}
		}
		payload.Questions = append(payload.Questions, question)
	}
	return payload
}

// upgradeLegacyPayload converts the pre-v2 single-question shape
// (question/options/input_type/multiple/allow_custom) into the canonical
// payload. This is the only place legacy aliases are still understood.
func upgradeLegacyPayload(raw map[string]any) UIPayload {
	question := UIQuestion{
		ID:          "q1",
		Text:        stringValue(raw["question"]),
		AllowCustom: boolValue(raw["allow_custom"]),
		Placeholder: stringValue(raw["placeholder"]),
	}
	if question.Text == "" {
		question.Text = "Please choose an option."
	}

	if items, ok := raw["options"].([]any); ok {
		for idx, item := range items {
			option, isCustomText := upgradeLegacyOption(item, idx)
			if option.ID == "" && option.Label == "" {
				continue
			}
			if isCustomText {
				// v1 modeled "custom answer" as an option with
				// input_type=text; v2 models it as question-level
				// allow_custom.
				question.AllowCustom = true
				if question.Placeholder == "" {
					question.Placeholder = option.Description
				}
				continue
			}
			question.Options = append(question.Options, option)
		}
	}

	switch {
	case len(question.Options) == 0:
		question.Kind = QuestionKindText
		question.AllowCustom = false
	case legacyMultipleValue(raw):
		question.Kind = QuestionKindMultiSelect
	default:
		question.Kind = QuestionKindSingleSelect
	}
	return UIPayload{Version: PayloadVersion, Questions: []UIQuestion{question}}
}

func upgradeLegacyOption(item any, idx int) (UIOption, bool) {
	switch typed := item.(type) {
	case map[string]any:
		option := UIOption{
			ID:          stringValue(typed["id"]),
			Label:       stringValue(typed["label"]),
			Description: stringValue(typed["description"]),
		}
		if option.ID == "" {
			option.ID = stringValue(typed["value"])
		}
		if option.ID == "" {
			option.ID = option.Label
		}
		if option.ID == "" {
			option.ID = fmt.Sprintf("q1.o%d", idx+1)
		}
		if option.Label == "" {
			option.Label = stringValue(typed["value"])
		}
		if option.Label == "" {
			option.Label = option.ID
		}
		isCustomText := strings.EqualFold(strings.TrimSpace(stringValue(typed["input_type"])), "text")
		if isCustomText {
			option.Description = stringValue(typed["placeholder"])
		}
		return option, isCustomText
	default:
		text := stringValue(typed)
		return UIOption{ID: text, Label: text}, false
	}
}

func legacyMultipleValue(raw map[string]any) bool {
	if boolValue(raw["multiple"]) || boolValue(raw["multi_select"]) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(raw["selection_type"]))) {
	case "multiple", "multi", "multi_select":
		return true
	default:
		return false
	}
}

func payloadObject(input any) map[string]any {
	switch typed := input.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case json.RawMessage:
		return unmarshalObject(typed)
	case []byte:
		return unmarshalObject(typed)
	default:
		data, err := json.Marshal(input)
		if err != nil {
			return nil
		}
		return unmarshalObject(data)
	}
}

func unmarshalObject(data []byte) map[string]any {
	if len(data) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
