package native

import (
	"context"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// toolExecutionMetadataRegistry keeps UI-only metadata beside a tool call
// without adding display fields to what the model sees: target identity
// pinned at approval time, and tool-result payloads (e.g. the edit diff)
// stripped from the tool output before the SDK records it. Both are merged
// back only into harness-side channels — stream events and the in-memory
// ToolCallPart.ProviderMetadata, which the persist path then lifts onto the
// row's metadata column so the payload never counts against history bytes.
type toolExecutionMetadataRegistry struct {
	mu        sync.RWMutex
	locations map[string]any
	uiExtras  map[string]map[string]any
	onUpdate  func(sdk.ToolCall, map[string]any)
}

func newToolExecutionMetadataRegistry(onUpdate func(sdk.ToolCall, map[string]any)) *toolExecutionMetadataRegistry {
	return &toolExecutionMetadataRegistry{
		locations: make(map[string]any),
		uiExtras:  make(map[string]map[string]any),
		onUpdate:  onUpdate,
	}
}

// wrap captures the execution location pinned by the approval handler — the
// authoritative point where an omitted target resolves to the current
// default, so metadata recorded earlier could be wrong.
func (r *toolExecutionMetadataRegistry) wrap(
	next func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error),
) func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
	if r == nil || next == nil {
		return next
	}
	return func(ctx context.Context, call sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
		result, err := next(ctx, call)
		if err != nil {
			return result, err
		}
		location, ok := result.Metadata[toolapproval.ExecutionLocationMetadataKey]
		if !ok || location == nil || strings.TrimSpace(call.ToolCallID) == "" {
			return result, nil
		}
		callID := strings.TrimSpace(call.ToolCallID)
		r.mu.Lock()
		r.locations[callID] = location
		r.mu.Unlock()

		metadata := r.metadata(callID)
		if r.onUpdate != nil && metadata != nil {
			r.onUpdate(call, metadata)
		}
		return result, nil
	}
}

func (r *toolExecutionMetadataRegistry) metadata(toolCallID string) map[string]any {
	if r == nil {
		return nil
	}
	callID := strings.TrimSpace(toolCallID)
	r.mu.RLock()
	location, hasLocation := r.locations[callID]
	extras := r.uiExtras[callID]
	r.mu.RUnlock()
	if !hasLocation && len(extras) == 0 {
		return nil
	}
	metadata := make(map[string]any, 1+len(extras))
	for key, value := range extras {
		// System-owned keys are never overridable by tool output — extras
		// arrive via the allowlist already, but keep the guard local so the
		// invariant holds wherever uiExtras is fed from.
		if key == toolapproval.ExecutionLocationMetadataKey {
			continue
		}
		metadata[key] = value
	}
	if hasLocation && location != nil {
		metadata[toolapproval.ExecutionLocationMetadataKey] = location
	}
	return metadata
}

func (r *toolExecutionMetadataRegistry) annotate(messages []sdk.Message) []sdk.Message {
	if r == nil || len(messages) == 0 {
		return messages
	}
	annotated := make([]sdk.Message, len(messages))
	copy(annotated, messages)
	changed := false
	for messageIndex := range annotated {
		if annotated[messageIndex].Role != sdk.MessageRoleAssistant {
			continue
		}
		parts := append([]sdk.MessagePart(nil), annotated[messageIndex].Content...)
		messageChanged := false
		for partIndex := range parts {
			call, ok := parts[partIndex].(sdk.ToolCallPart)
			if !ok {
				continue
			}
			metadata := r.metadata(call.ToolCallID)
			if metadata == nil {
				continue
			}
			for key, value := range metadata {
				call.ProviderMetadata = partmeta.Set(call.ProviderMetadata, key, value)
			}
			parts[partIndex] = call
			messageChanged = true
		}
		if messageChanged {
			annotated[messageIndex].Content = parts
			changed = true
		}
	}
	if !changed {
		return messages
	}
	return annotated
}
