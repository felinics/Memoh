package native

import (
	"context"
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
)

// toolExecutionMetadataRegistry keeps UI-only target identity beside a tool
// call without adding display fields to the model-generated tool arguments.
// The approval handler is the authoritative point where an omitted target is
// pinned to the current default, so metadata recorded earlier could be wrong.
type toolExecutionMetadataRegistry struct {
	mu        sync.RWMutex
	locations map[string]any
	onUpdate  func(sdk.ToolCall, map[string]any)
}

func newToolExecutionMetadataRegistry(onUpdate func(sdk.ToolCall, map[string]any)) *toolExecutionMetadataRegistry {
	return &toolExecutionMetadataRegistry{
		locations: make(map[string]any),
		onUpdate:  onUpdate,
	}
}

func (r *toolExecutionMetadataRegistry) wrap(
	next func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error),
) func(context.Context, sdk.ToolCall) (sdk.ToolApprovalResult, error) {
	if r == nil || next == nil {
		return next
	}
	return func(ctx context.Context, call sdk.ToolCall) (sdk.ToolApprovalResult, error) {
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
	r.mu.RLock()
	location, ok := r.locations[strings.TrimSpace(toolCallID)]
	r.mu.RUnlock()
	if !ok || location == nil {
		return nil
	}
	return map[string]any{toolapproval.ExecutionLocationMetadataKey: location}
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
			providerMetadata := make(map[string]any, len(call.ProviderMetadata)+1)
			for key, value := range call.ProviderMetadata {
				providerMetadata[key] = value
			}
			for key, value := range metadata {
				providerMetadata[key] = value
			}
			call.ProviderMetadata = providerMetadata
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
