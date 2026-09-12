package native

import (
	"strings"

	sdk "github.com/felinics/twilight/sdk"

	tools "github.com/felinics/memoh/internal/agent/tool"
)

// recordUIOutput stores a tool's UI-only payload (everything under the
// reserved tools.UIOutputMetadataKey) beside the tool call. It rides the
// same harness-side channels as the approval-pinned location: stream events
// and the persisted ToolCallPart.ProviderMetadata. The model never sees it.
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
	if existing == nil {
		existing = make(map[string]any, len(values))
		r.uiExtras[callID] = existing
	}
	for key, value := range values {
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
func (r *toolExecutionMetadataRegistry) wrapToolUIOutput(sdkTools []sdk.Tool) []sdk.Tool {
	if r == nil || len(sdkTools) == 0 {
		return sdkTools
	}
	wrapped := make([]sdk.Tool, len(sdkTools))
	copy(wrapped, sdkTools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			output, err := execute(ctx, input)
			if err != nil {
				return output, err
			}
			outputMap, ok := output.(map[string]any)
			if !ok {
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
			cleaned := make(map[string]any, len(outputMap)-1)
			for key, value := range outputMap {
				if key != tools.UIOutputMetadataKey {
					cleaned[key] = value
				}
			}
			return cleaned, nil
		}
	}
	return wrapped
}
