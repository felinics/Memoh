package bot

import (
	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	"github.com/memohai/memoh/domains/api/bot"
)

func scrubBotForResponse(record bot.Bot) bot.Bot {
	record.Metadata = acpprofile.ScrubMetadataForResponse(record.Metadata)
	record.Metadata = scrubWorkspaceDiagnosticsForResponse(record.Metadata)
	return record
}

func scrubBotsForResponse(items []bot.Bot) []bot.Bot {
	out := make([]bot.Bot, 0, len(items))
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

func scrubBotChecksForResponse(items []bot.BotCheck, includeDetails bool) []bot.BotCheck {
	if includeDetails {
		return items
	}
	out := make([]bot.BotCheck, 0, len(items))
	for _, item := range items {
		item.Detail = ""
		item.Metadata = nil
		out = append(out, item)
	}
	return out
}
