package contextlimit

import (
	"fmt"

	sdk "github.com/felinics/twilight/sdk"
)

// StepToolResultTruncateBytes is the content-size threshold above which an
// old tool-result message is replaced with a byte-count summary during
// step-level / mid-task pruning.
const StepToolResultTruncateBytes = 512

// TruncateStepToolResult replaces each sdk.ToolResultPart in msg whose own
// encoded size exceeds thresholdBytes (falling back to
// StepToolResultTruncateBytes when thresholdBytes <= 0) with a
// "[tool result pruned: N bytes]" summary. Parallel-call batches share one
// tool message, so sizing is per part: small siblings survive verbatim, and
// every other field of the part — IsError above all — is preserved so a
// failed result never reads as success. ok reports whether msg was changed.
func TruncateStepToolResult(msg sdk.Message, thresholdBytes int) (out sdk.Message, ok bool) {
	if thresholdBytes <= 0 {
		thresholdBytes = StepToolResultTruncateBytes
	}
	changed := false
	parts := make([]sdk.MessagePart, 0, len(msg.Content))
	for _, part := range msg.Content {
		tr, isResult := part.(sdk.ToolResultPart)
		if !isResult {
			parts = append(parts, part)
			continue
		}
		size := len(fmt.Sprintf("%v", tr.Result))
		if size <= thresholdBytes {
			parts = append(parts, tr)
			continue
		}
		tr.Result = fmt.Sprintf("[tool result pruned: %d bytes]", size)
		parts = append(parts, tr)
		changed = true
	}
	if !changed {
		return msg, false
	}
	return sdk.Message{Role: msg.Role, Content: parts}, true
}
