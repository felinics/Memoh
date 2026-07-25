package slash

import (
	"errors"
	"testing"
)

func TestRejectReservedSkillMetadata(t *testing.T) {
	tests := []string{
		"requested_skills",
		"requestedSkills",
		"model_requested_skills",
		"modelRequestedSkills",
		"MODEL.REQUESTED.SKILLS",
		"model-context-skills",
		"MODEL.USED.SKILLS",
		"loadedSkills",
		"user_message_kind",
		"userMessageKind",
		"skill_activation",
		"skillActivation",
		"skill.activation.prompt",
		"skill-activation-skills",
		"audit_requested_skills",
	}
	for _, key := range tests {
		t.Run(key, func(t *testing.T) {
			err := RejectReservedSkillMetadata(map[string]any{key: []string{"x"}})
			if err == nil {
				t.Fatal("err = nil, want reserved metadata error")
			}
			var slashErr Error
			if !errors.As(err, &slashErr) || slashErr.Code != CodeReservedSkillMetadata {
				t.Fatalf("err = %#v, want %s", err, CodeReservedSkillMetadata)
			}
		})
	}
}

func TestRejectReservedSkillMetadataAllowsUnrelated(t *testing.T) {
	if err := RejectReservedSkillMetadata(map[string]any{"reply": "x"}); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestRejectReservedSkillMetadataRejectsNestedValues(t *testing.T) {
	err := RejectReservedSkillMetadata(map[string]any{
		"attachment": map[string]any{
			"metadata": map[string]any{
				"model_requested_skills": []string{"alpha"},
			},
		},
	})
	var slashErr Error
	if !errors.As(err, &slashErr) || slashErr.Code != CodeReservedSkillMetadata {
		t.Fatalf("err = %#v, want %s", err, CodeReservedSkillMetadata)
	}
}
