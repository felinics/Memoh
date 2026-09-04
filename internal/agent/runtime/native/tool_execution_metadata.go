package native

import (
	"context"
	"strings"
	"sync"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	"github.com/felinics/memoh/internal/agent/event"
)

// toolExecutionMetadataRegistry keeps UI-only target identity and execution
// timing beside a tool call without adding display fields to the
// model-generated tool arguments. The approval handler is the authoritative
// point where an omitted target is pinned to the current default, so metadata
// recorded earlier could be wrong.
type toolExecutionMetadataRegistry struct {
	mu        sync.RWMutex
	now       func() time.Time
	since     func(time.Time) time.Duration
	locations map[string]any
	timings   map[string]event.ExecutionTiming
	onUpdate  func(sdk.ToolCall, map[string]any)
}

func newToolExecutionMetadataRegistry(onUpdate func(sdk.ToolCall, map[string]any)) *toolExecutionMetadataRegistry {
	registry := &toolExecutionMetadataRegistry{
		now:       time.Now,
		locations: make(map[string]any),
		timings:   make(map[string]event.ExecutionTiming),
		onUpdate:  onUpdate,
	}
	registry.since = func(startedAt time.Time) time.Duration { return registry.now().Sub(startedAt) }
	return registry
}

// wrapExecute clocks each tool execution under its call ID. The timing is
// complete by the time the SDK reports the tool result, so it rides both the
// tool_call_end event and the persisted tool-call part.
func (r *toolExecutionMetadataRegistry) wrapExecute(tools []sdk.Tool) []sdk.Tool {
	if r == nil || len(tools) == 0 {
		return tools
	}
	wrapped := make([]sdk.Tool, len(tools))
	copy(wrapped, tools)
	for i := range wrapped {
		execute := wrapped[i].Execute
		if execute == nil {
			continue
		}
		wrapped[i].Execute = func(ctx *sdk.ToolExecContext, input any) (any, error) {
			callID := ""
			if ctx != nil {
				callID = strings.TrimSpace(ctx.ToolCallID)
			}
			startedAt := r.now()
			out, err := execute(ctx, input)
			if callID != "" {
				started := startedAt.UnixMilli()
				r.mu.Lock()
				r.timings[callID] = event.ExecutionTiming{StartedAtMS: started, EndedAtMS: started + r.since(startedAt).Milliseconds()}
				r.mu.Unlock()
			}
			return out, err
		}
	}
	return wrapped
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
	callID := strings.TrimSpace(toolCallID)
	r.mu.RLock()
	location, hasLocation := r.locations[callID]
	timing, hasTiming := r.timings[callID]
	r.mu.RUnlock()
	if (!hasLocation || location == nil) && !hasTiming {
		return nil
	}
	metadata := make(map[string]any, 2)
	if hasLocation && location != nil {
		metadata[toolapproval.ExecutionLocationMetadataKey] = location
	}
	if hasTiming {
		metadata[event.ExecutionTimingMetadataKey] = timing
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
