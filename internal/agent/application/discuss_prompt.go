package application

import (
	"strings"

	"github.com/felinics/memoh/internal/agent/turn"
)

const discussAgentPromptPrefix = "You are replying in a discuss-mode conversation. The runtime is reset each turn, so use the complete context below as the source of truth.\n\n" +
	"IMPORTANT: You MUST use the `send` tool to speak in the observed conversation. Ordinary text output is internal and invisible to everyone.\n\n"
const discussAgentPromptSuffix = "Reply to the latest user-visible message when a response is appropriate."

// discussAgentFullContextPrompt renders the composed context into the single
// reset-each-turn prompt used by external ACP runtimes. ACP does not receive
// native ToolUsage, so its stable preamble owns the send-only output contract.
func discussAgentFullContextPrompt(messages []turn.DiscussMessage) string {
	var b strings.Builder
	b.WriteString(discussAgentPromptPrefix)
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		b.WriteString("[")
		b.WriteString(role)
		b.WriteString("]\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	b.WriteString(discussAgentPromptSuffix)
	return b.String()
}
