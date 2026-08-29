package contextlimit

import (
	"fmt"

	sdk "github.com/felinics/twilight/sdk"
)

// StepToolResultTruncateBytes is the content-size threshold above which an
// old tool-result message is replaced with a byte-count summary during
// step-level / mid-task pruning.
const StepToolResultTruncateBytes = 512

// TruncateStepToolResult replaces every sdk.ToolResultPart in msg with a
// "[tool result pruned: N bytes]" summary when their combined encoded size
// exceeds thresholdBytes (falling back to StepToolResultTruncateBytes when
// thresholdBytes <= 0), preserving ToolCallID/ToolName. ok reports whether
// msg was changed.
func TruncateStepToolResult(msg sdk.Message, thresholdBytes int) (out sdk.Message, ok bool) {
	if thresholdBytes <= 0 {
		thresholdBytes = StepToolResultTruncateBytes
	}
	contentSize := 0
	for _, part := range msg.Content {
		if tr, isResult := part.(sdk.ToolResultPart); isResult {
			contentSize += len(fmt.Sprintf("%v", tr.Result))
		}
	}
	if contentSize <= thresholdBytes {
		return msg, false
	}
	parts := make([]sdk.MessagePart, 0, len(msg.Content))
	for _, part := range msg.Content {
		if tr, isResult := part.(sdk.ToolResultPart); isResult {
			parts = append(parts, sdk.ToolResultPart{
				ToolCallID: tr.ToolCallID,
				ToolName:   tr.ToolName,
				Result:     fmt.Sprintf("[tool result pruned: %d bytes]", contentSize),
			})
			continue
		}
		parts = append(parts, part)
	}
	return sdk.Message{Role: msg.Role, Content: parts}, true
}
