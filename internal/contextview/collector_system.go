package contextview

import (
	"context"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const systemPromptCollectorName = "system_prompt"

type SystemPromptConfig struct {
	System    string
	ToolUsage string
	// SplitWorkspace splits the workspace instruction section into its own
	// fragment even without embedded tool usage, so an agent-side tool usage
	// fragment can sort between prompt and workspace instructions.
	SplitWorkspace bool
}

// SystemPromptCollector reverse-parses a flat system prompt string for callers
// that do not already provide typed source fragments.
type SystemPromptCollector struct{}

func (*SystemPromptCollector) Name() string {
	return systemPromptCollectorName
}

func (*SystemPromptCollector) Collect(_ context.Context, req CollectRequest) ([]contextfrag.ContextFrag, error) {
	cfg, err := systemPromptConfig(req.Config)
	if err != nil {
		return nil, err
	}

	rawSystem := cfg.System
	if rawSystem == "" {
		return nil, nil
	}
	system := strings.TrimSpace(rawSystem)
	if system == "" {
		return preserveSystemBytes(rawSystem, nil, req.Scope), nil
	}

	toolUsage := strings.TrimSpace(cfg.ToolUsage)
	toolStart := -1
	if toolUsage != "" {
		toolStart = strings.Index(system, toolUsage)
	}
	if toolStart < 0 {
		if cfg.SplitWorkspace {
			if idx := strings.Index(system, contextfrag.WorkspaceInstructionAnchor); idx >= 0 {
				frags := make([]contextfrag.ContextFrag, 0, 2)
				if prompt := strings.TrimSpace(system[:idx]); prompt != "" {
					frags = append(frags, systemPromptTextFrag(req.Scope, "system.prompt", contextfrag.KindSystemPrompt, prompt, 20, contextfrag.SourceRunConfig, 0))
				}
				frags = append(frags, systemPromptTextFrag(req.Scope, "system.workspace_instructions", contextfrag.KindWorkspaceInstruction, strings.TrimSpace(system[idx:]), 50, contextfrag.SourceRunConfig, 1))
				return preserveSystemBytes(rawSystem, frags, req.Scope), nil
			}
		}
		return preserveSystemBytes(rawSystem, []contextfrag.ContextFrag{
			systemPromptTextFrag(req.Scope, "system.prompt", contextfrag.KindSystemPrompt, system, 20, contextfrag.SourceRunConfig, 0),
		}, req.Scope), nil
	}

	frags := make([]contextfrag.ContextFrag, 0, 3)
	if prefix := strings.TrimSpace(system[:toolStart]); prefix != "" {
		frags = append(frags, systemPromptTextFrag(req.Scope, "system.prompt", contextfrag.KindSystemPrompt, prefix, 20, contextfrag.SourceRunConfig, 0))
	}

	rest := strings.TrimSpace(system[toolStart:])
	toolEnd := len(toolUsage)
	if toolUsageText := strings.TrimSpace(rest[:toolEnd]); toolUsageText != "" {
		frags = append(frags, systemPromptTextFrag(req.Scope, "system.tool_usage", contextfrag.KindToolUsage, toolUsageText, 45, contextfrag.SourceAgentToolUsage, 1))
	}
	if suffix := strings.TrimSpace(rest[toolEnd:]); suffix != "" {
		kind := contextfrag.KindSystemPrompt
		id := "system.prompt.tail"
		if strings.HasPrefix(suffix, "## Workspace instruction files") {
			kind = contextfrag.KindWorkspaceInstruction
			id = "system.workspace_instructions"
		}
		frags = append(frags, systemPromptTextFrag(req.Scope, id, kind, suffix, 50, contextfrag.SourceRunConfig, 2))
	}
	return preserveSystemBytes(rawSystem, frags, req.Scope), nil
}

func preserveSystemBytes(raw string, frags []contextfrag.ContextFrag, scope contextfrag.Scope) []contextfrag.ContextFrag {
	var parts []string
	for _, frag := range frags {
		for _, part := range frag.Parts {
			if part.Type == contextfrag.PartText {
				parts = append(parts, part.Text)
			}
		}
	}
	if strings.Join(parts, "\n\n") == raw {
		return frags
	}
	return []contextfrag.ContextFrag{{
		ID:            "system.prompt",
		Kind:          contextfrag.KindSystemPrompt,
		Role:          sdk.MessageRoleSystem,
		Slot:          contextfrag.SlotSystem,
		Priority:      20,
		RetentionTier: contextfrag.RetentionRequired,
		CacheClass:    contextfrag.CacheStable,
		Trust:         contextfrag.TrustSystem,
		Scope:         scope,
		Render:        contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		Provenance:    contextfrag.Provenance{Source: contextfrag.SourceRunConfig, Collector: systemPromptCollectorName},
		Parts:         []contextfrag.Part{{Type: contextfrag.PartText, Text: raw}},
	}}
}

func systemPromptConfig(config any) (SystemPromptConfig, error) {
	return collectorConfig[SystemPromptConfig](config, "system_prompt config must be SystemPromptConfig")
}

func systemPromptTextFrag(scope contextfrag.Scope, id string, kind contextfrag.Kind, text string, priority int, source string, index int) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:            id,
		Kind:          kind,
		Role:          sdk.MessageRoleSystem,
		Slot:          contextfrag.SlotSystem,
		Text:          text,
		Priority:      priority,
		RetentionTier: contextfrag.RetentionRequired,
		CacheClass:    contextfrag.CacheStable,
		Trust:         contextfrag.TrustSystem,
		Scope:         scope,
		Source:        source,
		Collector:     systemPromptCollectorName,
		Index:         index,
		Render:        contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
	})
}
