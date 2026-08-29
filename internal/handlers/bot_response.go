package handlers

import (
	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/bots"
)

func scrubBotForResponse(bot bots.Bot) bots.Bot {
	bot.Metadata = acpprofile.ScrubMetadataForResponse(bot.Metadata)
	bot.Metadata = scrubWorkspaceDiagnosticsForResponse(bot.Metadata)
	return bot
}

func scrubBotsForResponse(items []bots.Bot) []bots.Bot {
	out := make([]bots.Bot, 0, len(items))
	for _, item := range items {
		out = append(out, scrubBotForResponse(item))
	}
	return out
}

func scrubWorkspaceDiagnosticsForResponse(metadata map[string]any) map[string]any {
	workspace, ok := metadata["workspace"].(map[string]any)
	if !ok {
		return metadata
	}
	if _, ok := workspace["last_setup_error"]; !ok {
		return metadata
	}
	nextWorkspace := make(map[string]any, len(workspace)-1)
	for key, value := range workspace {
		if key != "last_setup_error" {
			nextWorkspace[key] = value
		}
	}
	if len(nextWorkspace) == 0 {
		delete(metadata, "workspace")
		return metadata
	}
	metadata["workspace"] = nextWorkspace
	return metadata
}

func scrubBotChecksForResponse(items []bots.BotCheck, includeDetails bool) []bots.BotCheck {
	if includeDetails {
		return items
	}
	out := make([]bots.BotCheck, 0, len(items))
	for _, item := range items {
		item.Detail = ""
		item.Metadata = nil
		out = append(out, item)
	}
	return out
}
