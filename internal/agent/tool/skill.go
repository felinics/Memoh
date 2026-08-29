package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	sdk "github.com/felinics/twilight/sdk"
)

type SkillProvider struct {
	logger *slog.Logger
}

func NewSkillProvider(log *slog.Logger) *SkillProvider {
	if log == nil {
		log = slog.Default()
	}
	return &SkillProvider{logger: log.With(slog.String("tool", "skill"))}
}

func (*SkillProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	if listRef, ok := available.Ref(ToolListSkills()); ok {
		parts = append(parts, "Use "+listRef+" to inspect skill names and descriptions when needed.")
	}
	if useRef, ok := available.Ref(ToolUseSkill()); ok {
		parts = append(parts,
			"Use "+useRef+" to load a relevant skill's full instructions before following it.",
			"Do not activate skills that are unrelated to the current task.",
		)
	}
	return usageSection("Skills", parts)
}

func (*SkillProvider) Tools(_ context.Context, session SessionContext) ([]sdk.Tool, error) {
	if len(session.Skills) == 0 {
		return nil, nil
	}
	skills := session.Skills
	return []sdk.Tool{
		{
			Name:        ToolListSkills().String(),
			Description: "List the skills available in the current session.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				names := make([]string, 0, len(skills))
				for name := range skills {
					names = append(names, name)
				}
				sort.Strings(names)

				items := make([]map[string]any, 0, len(names))
				for _, name := range names {
					skill := skills[name]
					items = append(items, map[string]any{
						"name":        name,
						"description": skill.Description,
						"path":        skill.Path,
					})
				}
				return map[string]any{
					"success": true,
					"count":   len(items),
					"skills":  items,
				}, nil
			},
		},
		{
			Name:        ToolUseSkill().String(),
			Description: "Activate a skill to get its full instructions. Call this when you think a skill is relevant to the current task.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"skillName": map[string]any{
						"type":        "string",
						"description": "The name of the skill to activate",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Why this skill is relevant to the current task",
					},
				},
				"required": []string{"skillName", "reason"},
			},
			Execute: func(_ *sdk.ToolExecContext, input any) (any, error) {
				args := inputAsMap(input)
				skillName := StringArg(args, "skillName")
				if skillName == "" {
					return nil, errors.New("skillName is required")
				}
				skill, ok := skills[skillName]
				if !ok {
					return map[string]any{
						"success": false,
						"error":   fmt.Sprintf("skill %q not found — check available skills in the system prompt", skillName),
					}, nil
				}
				return map[string]any{
					"success":     true,
					"skillName":   skillName,
					"description": skill.Description,
					"content":     skill.Content,
					"path":        skill.Path,
				}, nil
			},
		},
	}, nil
}
