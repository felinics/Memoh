package native

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

const toolUsageGroupID = "system.tool_usage"

type toolUsageSection struct {
	capability string
	text       string
}

func firstToolName(providerTools []sdk.Tool) string {
	first := ""
	for _, tool := range providerTools {
		name := strings.TrimSpace(tool.Name)
		if name != "" && (first == "" || name < first) {
			first = name
		}
	}
	return first
}

func structuredToolUsage(sections []toolUsageSection, scope contextfrag.Scope) []contextfrag.ContextFrag {
	if len(sections) == 0 {
		return nil
	}
	render := contextfrag.RenderPolicy{
		Format:      contextfrag.RenderMarkdown,
		GroupID:     toolUsageGroupID,
		GroupJoiner: "\n\n",
	}
	frag := func(id, text, capability string, index int) contextfrag.ContextFrag {
		return contextfrag.TextFrag(contextfrag.TextFragInput{
			ID:                 id,
			Kind:               contextfrag.KindToolUsage,
			Role:               sdk.MessageRoleSystem,
			Slot:               contextfrag.SlotSystem,
			Text:               text,
			Priority:           45,
			RetentionTier:      contextfrag.RetentionPreferred,
			RequiredCapability: capability,
			CacheClass:         contextfrag.CacheStable,
			Trust:              contextfrag.TrustSystem,
			Scope:              scope,
			Source:             contextfrag.SourceAgentToolUsage,
			SourceID:           capability,
			Collector:          "assemble_tools",
			Index:              index,
			Render:             render,
		})
	}

	frags := make([]contextfrag.ContextFrag, 0, len(sections)+1)
	frags = append(frags, frag(
		toolUsageGroupID+".header",
		"## Tool usage",
		sections[0].capability,
		0,
	))
	for i, section := range sections {
		frags = append(frags, frag(
			toolUsageGroupID+"."+section.capability,
			section.text,
			section.capability,
			i+1,
		))
	}
	return frags
}
