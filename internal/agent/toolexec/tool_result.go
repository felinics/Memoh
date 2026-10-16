package toolexec

import sdk "github.com/felinics/twilight/sdk"

// ToolResult pairs a tool call with what the tool returned.
type ToolResult struct {
	ToolCallID string            `json:"toolCallId"`
	ToolName   string            `json:"toolName"`
	Input      sdk.ToolArguments `json:"input"`
	Output     sdk.ToolOutput    `json:"output"`
}
