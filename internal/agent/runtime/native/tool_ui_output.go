package native

import (
	"encoding/json"
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// recordUIOutput stores a tool's UI-only payload (the allowlisted keys under
// the reserved tools.UIOutputMetadataKey) beside the tool call. It rides the
// same harness-side channels as the approval-pinned location: stream events
// and ToolCallPart.ProviderMetadata, lifted onto row metadata at persist
// time. The model never sees it.
func (r *toolExecutionMetadataRegistry) recordUIOutput(toolCallID string, values map[string]any) {
	if r == nil || len(values) == 0 {
		return
	}
	callID := strings.TrimSpace(toolCallID)
	if callID == "" {
		return
	}
	r.mu.Lock()
	existing := r.uiExtras[callID]
	for key, value := range values {
		if !tools.IsUIOutputKey(key) {
			continue
		}
		if existing == nil {
			existing = make(map[string]any, len(values))
			r.uiExtras[callID] = existing
		}
		existing[key] = value
	}
	r.mu.Unlock()
}

// wrapToolUIOutput strips the reserved UI-only key from tool outputs before
// the SDK records them. The SDK builds the model's tool-result message and
// the streamed result part from the same value, so stripping here keeps the
// payload out of the model in the current turn and in rebuilt history alike.
// Apply it innermost (before output limits and hook wrappers) so the payload
// is recorded whole and never counts against the model's output budget.
func (r *toolExecutionMetadataRegistry) wrapToolUIOutput(sdkTools []toolexec.Tool) []toolexec.Tool {
	if r == nil || len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]toolexec.Tool, len(sdkTools))
	copy(wrapped, sdkTools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		wrapped[i].Execute = func(ctx *toolexec.ToolExecContext, input sdk.ToolArguments) (sdk.ToolOutput, error) {
			output, err := execute(ctx, input)
			if err != nil || !output.IsJSON() {
				return output, err
			}
			// Only an object output can carry the reserved key; any other JSON
			// document passes through untouched.
			var outputMap map[string]any
			if json.Unmarshal(output.JSON, &outputMap) != nil || outputMap == nil {
				return output, nil
			}
			raw, ok := outputMap[tools.UIOutputMetadataKey]
			if !ok {
				return output, nil
			}
			if values, ok := raw.(map[string]any); ok {
				callID := ""
				if ctx != nil {
					callID = ctx.ToolCallID
				}
				r.recordUIOutput(callID, values)
			}
			delete(outputMap, tools.UIOutputMetadataKey)
			return toolexec.OutputFromValue(outputMap), nil
		}
	}
	return wrapped
}
