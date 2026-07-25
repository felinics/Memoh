package slash

import "strings"

var reservedMetadataKeys = map[string]struct{}{
	normalizeMetadataKey("requested_skills"):        {},
	normalizeMetadataKey("requestedSkills"):         {},
	normalizeMetadataKey("model_requested_skills"):  {},
	normalizeMetadataKey("modelRequestedSkills"):    {},
	normalizeMetadataKey("applied_skills"):          {},
	normalizeMetadataKey("model_used_skills"):       {},
	normalizeMetadataKey("model_context_skills"):    {},
	normalizeMetadataKey("loaded_skills"):           {},
	normalizeMetadataKey("user_message_kind"):       {},
	normalizeMetadataKey("userMessageKind"):         {},
	normalizeMetadataKey("skill_activation"):        {},
	normalizeMetadataKey("skillActivation"):         {},
	normalizeMetadataKey("skill_activation_prompt"): {},
	normalizeMetadataKey("skillActivationPrompt"):   {},
	normalizeMetadataKey("skill_activation_skills"): {},
	normalizeMetadataKey("skillActivationSkills"):   {},
	normalizeMetadataKey("audit_requested_skills"):  {},
}

func normalizeMetadataKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		switch r {
		case '_', '-', '.':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func IsReservedMetadataKey(key string) bool {
	_, ok := reservedMetadataKeys[normalizeMetadataKey(key)]
	return ok
}

func RejectReservedSkillMetadata(metadata map[string]any) error {
	return RejectReservedSkillMetadataValue(metadata)
}

// RejectReservedSkillMetadataValue rejects reserved skill metadata keys nested
// anywhere inside a JSON-like metadata value.
func RejectReservedSkillMetadataValue(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, nested := range v {
			if IsReservedMetadataKey(key) {
				return NewError(CodeReservedSkillMetadata)
			}
			if err := RejectReservedSkillMetadataValue(nested); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range v {
			if err := RejectReservedSkillMetadataValue(nested); err != nil {
				return err
			}
		}
	case []map[string]any:
		for _, nested := range v {
			if err := RejectReservedSkillMetadataValue(nested); err != nil {
				return err
			}
		}
	}
	return nil
}

// RejectReservedSkillMetadataShallow rejects reserved keys only at the top
// level of a metadata map.
func RejectReservedSkillMetadataShallow(metadata map[string]any) error {
	for key := range metadata {
		if IsReservedMetadataKey(key) {
			return NewError(CodeReservedSkillMetadata)
		}
	}
	return nil
}
