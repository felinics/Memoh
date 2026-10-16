package toolexec

import sdk "github.com/felinics/twilight/sdk"

// Stream part types emitted only by the executor. The SDK's own stream
// vocabulary stops at the model's tool call; these continue it through
// approval, progress and the tool's result.
const (
	StreamPartTypeToolResult          sdk.StreamPartType = "tool-result"
	StreamPartTypeToolError           sdk.StreamPartType = "tool-error"
	StreamPartTypeToolOutputDenied    sdk.StreamPartType = "tool-output-denied"
	StreamPartTypeToolApprovalRequest sdk.StreamPartType = "tool-approval-request"
	StreamPartTypeToolProgress        sdk.StreamPartType = "tool-progress"
)

type StreamToolResultPart struct {
	ToolCallID string
	ToolName   string
	Input      sdk.ToolArguments
	Output     sdk.ToolOutput
}

type StreamToolErrorPart struct {
	ToolCallID string
	ToolName   string
	Error      error
}

type ToolOutputDeniedPart struct {
	ToolCallID string
	ToolName   string
}

type ToolApprovalRequestPart struct {
	ApprovalID string
	ToolCallID string
	ToolName   string
	Input      sdk.ToolArguments
	Metadata   map[string]any
}

type ToolProgressPart struct {
	ToolCallID string
	ToolName   string
	Content    sdk.ToolOutput
}

func (*StreamToolResultPart) Type() sdk.StreamPartType    { return StreamPartTypeToolResult }
func (*StreamToolErrorPart) Type() sdk.StreamPartType     { return StreamPartTypeToolError }
func (*ToolOutputDeniedPart) Type() sdk.StreamPartType    { return StreamPartTypeToolOutputDenied }
func (*ToolApprovalRequestPart) Type() sdk.StreamPartType { return StreamPartTypeToolApprovalRequest }
func (*ToolProgressPart) Type() sdk.StreamPartType        { return StreamPartTypeToolProgress }
