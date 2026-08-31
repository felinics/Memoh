// Package-shared MCP form-elicitation core: translates an MCP elicitation
// requestedSchema into the strict ask_user question payload and maps the
// submitted answers back into schema-conformant content. Runtime adapters
// (ACP, codex) own only their protocol envelopes around this core.
package input

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type elicitationChoice struct {
	value       string
	label       string
	description string
}

type elicitationField struct {
	propertyID       string
	customPropertyID string
	questionID       string
	kind             string
	optionValues     map[string]string
	required         bool
	customExclusive  bool
}

type ElicitationFormMapping struct {
	fields []elicitationField
}

func ElicitationFormInput(message string, schema map[string]any) (map[string]any, ElicitationFormMapping, error) {
	if err := requireOnlySchemaKeywords(schema, "requestedSchema", "type", "properties", "required", "title", "description", "$schema"); err != nil {
		return nil, ElicitationFormMapping{}, err
	}
	if schemaType, err := optionalSchemaString(schema, "type", "requestedSchema"); err != nil {
		return nil, ElicitationFormMapping{}, err
	} else if schemaType != "" && schemaType != "object" {
		return nil, ElicitationFormMapping{}, fmt.Errorf("requestedSchema.type %q is unsupported", schemaType)
	}
	for _, key := range []string{"title", "description"} {
		if _, err := optionalSchemaString(schema, key, "requestedSchema"); err != nil {
			return nil, ElicitationFormMapping{}, err
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return nil, ElicitationFormMapping{}, errors.New("requestedSchema.properties must be a non-empty object")
	}
	requiredProperties, err := elicitationRequiredProperties(schema, properties)
	if err != nil {
		return nil, ElicitationFormMapping{}, err
	}

	customFields := make(map[string]string)
	customExclusiveFields := make(map[string]bool)
	customIDs := make(map[string]struct{})
	propertyTypes := make(map[string]string, len(properties))
	for propertyID, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			return nil, ElicitationFormMapping{}, fmt.Errorf("property %q must be an object", propertyID)
		}
		propertyType, err := requiredSchemaString(property, "type", "property "+propertyID)
		if err != nil {
			return nil, ElicitationFormMapping{}, err
		}
		if schemaPropertyIsSecret(property) {
			return nil, ElicitationFormMapping{}, fmt.Errorf("property %q is secret input, which this form surface cannot safely render", propertyID)
		}
		if err := validateElicitationPropertyKeywords(propertyID, propertyType, property); err != nil {
			return nil, ElicitationFormMapping{}, err
		}
		propertyTypes[propertyID] = propertyType
		target, custom, exclusive, err := customAnswerTarget(property)
		if err != nil {
			return nil, ElicitationFormMapping{}, fmt.Errorf("property %q: %w", propertyID, err)
		}
		if !custom {
			continue
		}
		if _, exists := customFields[target]; exists {
			return nil, ElicitationFormMapping{}, fmt.Errorf("property %q has more than one custom-answer field", target)
		}
		customFields[target] = propertyID
		customExclusiveFields[target] = exclusive
		customIDs[propertyID] = struct{}{}
	}
	for customID := range customIDs {
		if requiredProperties[customID] {
			return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q cannot be required", customID)
		}
	}
	// Accepting a form promises the returned content satisfies every declared
	// constraint, so custom-answer relations we cannot honor are rejected here
	// at the protocol boundary instead of failing (or silently passing) after
	// the user already submitted.
	for target, customID := range customFields {
		if _, exists := properties[target]; !exists {
			return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q targets unknown property %q", customID, target)
		}
		if _, isCustom := customIDs[target]; isCustom {
			return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q cannot target custom-answer property %q", customID, target)
		}
		if requiredProperties[target] {
			return nil, ElicitationFormMapping{}, fmt.Errorf("required property %q cannot take a custom answer via %q", target, customID)
		}
	}
	propertyIDs := make([]string, 0, len(properties)-len(customIDs))
	for propertyID := range properties {
		if strings.TrimSpace(propertyID) == "" || propertyID != strings.TrimSpace(propertyID) {
			return nil, ElicitationFormMapping{}, errors.New("elicitation property ids must be non-empty and have no surrounding whitespace")
		}
		if _, custom := customIDs[propertyID]; !custom {
			propertyIDs = append(propertyIDs, propertyID)
		}
	}
	sort.Strings(propertyIDs)

	questions := make([]any, 0, len(propertyIDs))
	mapping := ElicitationFormMapping{fields: make([]elicitationField, 0, len(propertyIDs))}
	// Option values per field, in option order. Canonical question/option IDs
	// are assigned by ParseAskUserPayload below rather than duplicated here.
	fieldValues := make([][]string, 0, len(propertyIDs))
	for _, propertyID := range propertyIDs {
		property := properties[propertyID].(map[string]any)
		propertyType := propertyTypes[propertyID]
		description, err := optionalSchemaString(property, "description", "property "+propertyID)
		if err != nil {
			return nil, ElicitationFormMapping{}, err
		}
		title, err := optionalSchemaString(property, "title", "property "+propertyID)
		if err != nil {
			return nil, ElicitationFormMapping{}, err
		}
		questionText := description
		if questionText == "" && len(propertyIDs) == 1 {
			questionText = strings.TrimSpace(message)
		}
		if questionText == "" {
			questionText = title
		}
		if questionText == "" {
			questionText = propertyID
		}

		field := elicitationField{
			propertyID: propertyID,
			required:   requiredProperties[propertyID],
		}
		var orderedValues []string
		// Always emit the boolean. The shared ask_user payload treats an absent
		// field as legacy/native behavior; ACP needs explicit false to preserve
		// optional JSON Schema properties.
		question := map[string]any{"text": questionText, "required": field.required}

		switch propertyType {
		case "string":
			choices, hasChoices, err := elicitationChoices(property, "property "+propertyID)
			if err != nil {
				return nil, ElicitationFormMapping{}, err
			}
			if hasChoices {
				field.kind = QuestionKindSingleSelect
				question["kind"] = field.kind
				options, values, err := elicitationOptions(choices)
				if err != nil {
					return nil, ElicitationFormMapping{}, fmt.Errorf("property %q: %w", propertyID, err)
				}
				question["options"] = options
				orderedValues = values
			} else {
				field.kind = QuestionKindText
				question["kind"] = field.kind
			}
		case "array":
			items, ok := property["items"].(map[string]any)
			if !ok {
				return nil, ElicitationFormMapping{}, fmt.Errorf("property %q must define array items", propertyID)
			}
			itemsPath := fmt.Sprintf("property %q.items", propertyID)
			if err := requireOnlySchemaKeywords(items, itemsPath, "type", "enum", "enumNames", "oneOf", "anyOf"); err != nil {
				return nil, ElicitationFormMapping{}, err
			}
			if itemType, err := optionalSchemaString(items, "type", "property "+propertyID+".items"); err != nil {
				return nil, ElicitationFormMapping{}, err
			} else if itemType != "" && itemType != "string" {
				return nil, ElicitationFormMapping{}, fmt.Errorf("property %q array items type %q is unsupported", propertyID, itemType)
			}
			choices, hasChoices, err := elicitationChoices(items, "property "+propertyID+".items")
			if err != nil {
				return nil, ElicitationFormMapping{}, err
			}
			if !hasChoices {
				return nil, ElicitationFormMapping{}, fmt.Errorf("property %q array items must define enum choices", propertyID)
			}
			field.kind = QuestionKindMultiSelect
			question["kind"] = field.kind
			options, values, err := elicitationOptions(choices)
			if err != nil {
				return nil, ElicitationFormMapping{}, fmt.Errorf("property %q: %w", propertyID, err)
			}
			question["options"] = options
			orderedValues = values
		default:
			return nil, ElicitationFormMapping{}, fmt.Errorf("property %q type %q is unsupported", propertyID, propertyType)
		}

		if customID := customFields[propertyID]; customID != "" {
			if field.kind == QuestionKindText {
				return nil, ElicitationFormMapping{}, fmt.Errorf("text property %q cannot have a custom-answer companion", propertyID)
			}
			customProperty, ok := properties[customID].(map[string]any)
			if !ok {
				return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q must be an object", customID)
			}
			customType, err := requiredSchemaString(customProperty, "type", "property "+customID)
			if err != nil {
				return nil, ElicitationFormMapping{}, err
			}
			if customType != "string" {
				return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q must be a string", customID)
			}
			if _, hasChoices, err := elicitationChoices(customProperty, "property "+customID); err != nil {
				return nil, ElicitationFormMapping{}, err
			} else if hasChoices {
				return nil, ElicitationFormMapping{}, fmt.Errorf("custom-answer property %q cannot define choices", customID)
			}
			placeholder, err := optionalSchemaString(customProperty, "description", "property "+customID)
			if err != nil {
				return nil, ElicitationFormMapping{}, err
			}
			question["allow_custom"] = true
			field.customExclusive = customExclusiveFields[propertyID]
			if field.customExclusive {
				question["custom_exclusive"] = true
			}
			if placeholder != "" {
				question["placeholder"] = placeholder
			}
			field.customPropertyID = customID
		}

		questions = append(questions, question)
		mapping.fields = append(mapping.fields, field)
		fieldValues = append(fieldValues, orderedValues)
	}

	input := map[string]any{"questions": questions}
	// ParseAskUserPayload owns question/option ID assignment; reading the IDs
	// it generated keeps this mapping correct even if the ID scheme changes.
	payload, err := ParseAskUserPayload(input)
	if err != nil {
		return nil, ElicitationFormMapping{}, err
	}
	if len(payload.Questions) != len(mapping.fields) {
		return nil, ElicitationFormMapping{}, fmt.Errorf("elicitation form built %d fields but the payload parsed %d questions", len(mapping.fields), len(payload.Questions))
	}
	for i := range mapping.fields {
		parsed := payload.Questions[i]
		mapping.fields[i].questionID = parsed.ID
		values := fieldValues[i]
		if len(values) == 0 {
			continue
		}
		if len(parsed.Options) != len(values) {
			return nil, ElicitationFormMapping{}, fmt.Errorf("question %q built %d option values but the payload parsed %d options", parsed.ID, len(values), len(parsed.Options))
		}
		optionValues := make(map[string]string, len(values))
		for j, option := range parsed.Options {
			optionValues[option.ID] = values[j]
		}
		mapping.fields[i].optionValues = optionValues
	}
	return input, mapping, nil
}

func validateElicitationPropertyKeywords(propertyID, propertyType string, property map[string]any) error {
	path := fmt.Sprintf("property %q", propertyID)
	if meta, exists := property["_meta"]; exists && meta != nil {
		if _, ok := meta.(map[string]any); !ok {
			return fmt.Errorf("%s._meta must be an object", path)
		}
	}
	switch propertyType {
	case "string":
		return requireOnlySchemaKeywords(
			property,
			path,
			"type", "title", "description", "enum", "enumNames", "oneOf", "anyOf", "_meta",
		)
	case "array":
		return requireOnlySchemaKeywords(property, path, "type", "title", "description", "items", "_meta")
	default:
		return fmt.Errorf("property %q type %q is unsupported", propertyID, propertyType)
	}
}

// Reject constraints the ask-user surface cannot preserve or validate. An
// accepted form must produce content that satisfies every advertised keyword.
func requireOnlySchemaKeywords(schema map[string]any, path string, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	unsupported := make([]string, 0)
	for key := range schema {
		if _, ok := allowedSet[key]; !ok {
			unsupported = append(unsupported, key)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	sort.Strings(unsupported)
	return fmt.Errorf("%s contains unsupported schema keyword %q", path, unsupported[0])
}

func customAnswerTarget(property map[string]any) (string, bool, bool, error) {
	meta, ok := property["_meta"].(map[string]any)
	if !ok {
		return "", false, false, nil
	}
	if marker, exists := meta["_askUserQuestionCustomAnswer"]; exists {
		config, ok := marker.(map[string]any)
		if !ok {
			return "", false, false, errors.New("custom-answer metadata must be an object")
		}
		custom, ok := config["isCustomAnswer"].(bool)
		if !ok || !custom {
			return "", false, false, errors.New("custom-answer metadata must set isCustomAnswer=true")
		}
		target, err := requiredSchemaString(config, "questionId", "custom-answer metadata")
		return target, true, true, err
	}
	codex, ok := meta["codex"].(map[string]any)
	if !ok {
		return "", false, false, nil
	}
	other, exists := codex["isOtherAnswer"]
	if !exists {
		return "", false, false, nil
	}
	isOther, ok := other.(bool)
	if !ok {
		return "", false, false, errors.New("codex isOtherAnswer metadata must be a boolean")
	}
	if !isOther {
		return "", false, false, nil
	}
	target, err := requiredSchemaString(codex, "questionId", "codex custom-answer metadata")
	return target, true, false, err
}

func elicitationRequiredProperties(schema, properties map[string]any) (map[string]bool, error) {
	required := map[string]bool{}
	raw, exists := schema["required"]
	if !exists || raw == nil {
		return required, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("requestedSchema.required must be an array")
	}
	for index, item := range items {
		propertyID, ok := item.(string)
		if !ok || strings.TrimSpace(propertyID) == "" || propertyID != strings.TrimSpace(propertyID) {
			return nil, fmt.Errorf("requestedSchema.required[%d] must be a non-empty property id", index)
		}
		if _, ok := properties[propertyID]; !ok {
			return nil, fmt.Errorf("requestedSchema.required references unknown property %q", propertyID)
		}
		required[propertyID] = true
	}
	return required, nil
}

func schemaPropertyIsSecret(property map[string]any) bool {
	if format, _ := property["format"].(string); strings.EqualFold(strings.TrimSpace(format), "password") {
		return true
	}
	meta, _ := property["_meta"].(map[string]any)
	codex, _ := meta["codex"].(map[string]any)
	secret, _ := codex["isSecret"].(bool)
	return secret
}

func elicitationChoices(schema map[string]any, path string) ([]elicitationChoice, bool, error) {
	carriers := make([]string, 0, 3)
	for _, key := range []string{"oneOf", "anyOf", "enum"} {
		if _, exists := schema[key]; exists {
			carriers = append(carriers, key)
		}
	}
	if len(carriers) == 0 {
		if _, exists := schema["enumNames"]; exists {
			return nil, false, fmt.Errorf("%s.enumNames requires enum", path)
		}
		return nil, false, nil
	}
	if len(carriers) != 1 {
		return nil, false, fmt.Errorf("%s has ambiguous choice definitions", path)
	}

	key := carriers[0]
	items, ok := schema[key].([]any)
	if !ok {
		return nil, false, fmt.Errorf("%s.%s must be an array", path, key)
	}
	choices := make([]elicitationChoice, 0, len(items))
	if key == "enum" {
		var names []any
		if rawNames, exists := schema["enumNames"]; exists {
			var ok bool
			names, ok = rawNames.([]any)
			if !ok || len(names) != len(items) {
				return nil, false, fmt.Errorf("%s.enumNames must match enum", path)
			}
		}
		for index, raw := range items {
			value, ok := raw.(string)
			if !ok {
				return nil, false, fmt.Errorf("%s.enum[%d] must be a string", path, index)
			}
			label := value
			if names != nil {
				name, ok := names[index].(string)
				if !ok {
					return nil, false, fmt.Errorf("%s.enumNames[%d] must be a string", path, index)
				}
				label = strings.TrimSpace(name)
			}
			choices = append(choices, elicitationChoice{value: value, label: label})
		}
		return choices, true, nil
	}

	for index, raw := range items {
		option, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("%s.%s[%d] must be an object", path, key, index)
		}
		optionPath := fmt.Sprintf("%s.%s[%d]", path, key, index)
		if err := requireOnlySchemaKeywords(option, optionPath, "const", "title", "description"); err != nil {
			return nil, false, err
		}
		value, ok := option["const"].(string)
		if !ok {
			return nil, false, fmt.Errorf("%s.%s[%d].const must be a string", path, key, index)
		}
		label, err := optionalSchemaString(option, "title", fmt.Sprintf("%s.%s[%d]", path, key, index))
		if err != nil {
			return nil, false, err
		}
		if label == "" {
			label = value
		}
		description, err := optionalSchemaString(option, "description", fmt.Sprintf("%s.%s[%d]", path, key, index))
		if err != nil {
			return nil, false, err
		}
		choices = append(choices, elicitationChoice{value: value, label: label, description: description})
	}
	return choices, true, nil
}

// elicitationOptions renders the choices as ask_user options and returns their
// schema values in option order. Option IDs are not assigned here — the caller
// reads the canonical IDs from the parsed payload.
func elicitationOptions(choices []elicitationChoice) ([]any, []string, error) {
	options := make([]any, 0, len(choices))
	values := make([]string, 0, len(choices))
	seenValues := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		if _, duplicate := seenValues[choice.value]; duplicate {
			return nil, nil, fmt.Errorf("choice value %q is duplicated", choice.value)
		}
		seenValues[choice.value] = struct{}{}
		option := map[string]any{"label": strings.TrimSpace(choice.label)}
		if choice.description != "" {
			option["description"] = choice.description
		}
		options = append(options, option)
		values = append(values, choice.value)
	}
	return options, values, nil
}

func requiredSchemaString(object map[string]any, key, path string) (string, error) {
	value, err := optionalSchemaString(object, key, path)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s.%s is required", path, key)
	}
	return value, nil
}

func optionalSchemaString(object map[string]any, key, path string) (string, error) {
	raw, exists := object[key]
	if !exists || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s must be a string", path, key)
	}
	return strings.TrimSpace(value), nil
}

func (m ElicitationFormMapping) Content(req Request) (map[string]any, error) {
	answers := AnswersFromResult(req.Result)
	byQuestion := make(map[string]UIAnswer, len(answers))
	for _, answer := range answers {
		byQuestion[answer.QuestionID] = answer
	}
	content := make(map[string]any, len(m.fields))
	for _, field := range m.fields {
		answer, ok := byQuestion[field.questionID]
		if !ok {
			return nil, fmt.Errorf("elicitation answer for field %q is missing", field.propertyID)
		}
		if answer.Skipped {
			if field.required {
				return nil, fmt.Errorf("elicitation required field %q was skipped", field.propertyID)
			}
			continue
		}
		switch field.kind {
		case QuestionKindText:
			if strings.TrimSpace(answer.Text) == "" {
				return nil, fmt.Errorf("elicitation text answer for field %q is empty", field.propertyID)
			}
			content[field.propertyID] = answer.Text
		case QuestionKindSingleSelect:
			if strings.TrimSpace(answer.CustomText) != "" {
				if field.customPropertyID == "" {
					return nil, fmt.Errorf("elicitation field %q does not accept custom input", field.propertyID)
				}
				content[field.customPropertyID] = answer.CustomText
				continue
			}
			if len(answer.Selected) != 1 {
				return nil, fmt.Errorf("elicitation field %q requires one selection", field.propertyID)
			}
			value, ok := field.optionValues[answer.Selected[0].ID]
			if !ok {
				return nil, fmt.Errorf("elicitation field %q selected an unknown option", field.propertyID)
			}
			content[field.propertyID] = value
		case QuestionKindMultiSelect:
			if field.customExclusive && len(answer.Selected) > 0 && strings.TrimSpace(answer.CustomText) != "" {
				return nil, fmt.Errorf("elicitation field %q accepts options or custom input, not both", field.propertyID)
			}
			selected := make([]string, 0, len(answer.Selected))
			for _, option := range answer.Selected {
				value, ok := field.optionValues[option.ID]
				if !ok {
					return nil, fmt.Errorf("elicitation field %q selected an unknown option", field.propertyID)
				}
				selected = append(selected, value)
			}
			if len(selected) > 0 {
				content[field.propertyID] = selected
			}
			if strings.TrimSpace(answer.CustomText) != "" {
				if field.customPropertyID == "" {
					return nil, fmt.Errorf("elicitation field %q does not accept custom input", field.propertyID)
				}
				content[field.customPropertyID] = answer.CustomText
			}
		default:
			return nil, fmt.Errorf("elicitation field %q has unsupported question kind %q", field.propertyID, field.kind)
		}
		if field.required {
			if _, ok := content[field.propertyID]; !ok {
				return nil, fmt.Errorf("elicitation required field %q is missing", field.propertyID)
			}
		}
	}
	return content, nil
}
