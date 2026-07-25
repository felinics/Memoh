package lifecycle

import (
	"strings"

	ctr "github.com/memohai/memoh/domains/runtime/container"
	runtimeworkspace "github.com/memohai/memoh/domains/runtime/workspace"
)

var knownContainerPrefixes = []string{runtimeworkspace.ContainerPrefix, runtimeworkspace.LegacyContainerPrefix}

// BotIDFromContainerID infers a bot ID from a known container naming scheme.
// This is only used as a fallback for legacy containers when labels are missing.
func BotIDFromContainerID(containerID string) (string, bool) {
	for _, prefix := range knownContainerPrefixes {
		if !strings.HasPrefix(containerID, prefix) {
			continue
		}
		botID := strings.TrimPrefix(containerID, prefix)
		if botID == "" {
			return "", false
		}
		return botID, true
	}
	return "", false
}

// BotIDFromContainerInfo resolves the bot ID from container metadata.
// It prefers the current label and only falls back to name inference.
func BotIDFromContainerInfo(info ctr.ContainerInfo) (string, bool) {
	if botID := strings.TrimSpace(info.Labels[runtimeworkspace.BotLabelKey]); botID != "" {
		return botID, true
	}
	return BotIDFromContainerID(info.ID)
}
