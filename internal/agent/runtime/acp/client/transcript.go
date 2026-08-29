package client

import (
	"strings"
	"sync"

	sdk "github.com/felinics/twilight/sdk"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
)

// TranscriptRecorder folds stream events into the persisted round transcript.
// It is not capped by the UI event buffer.
type TranscriptRecorder struct {
	mu             sync.Mutex
	output         []sdk.Message
	assistantParts []sdk.MessagePart
	reasoning      strings.Builder
	text           strings.Builder
	sawTextDelta   bool
	limit          ToolOutputLimit
}

// NewTranscriptRecorder creates an empty transcript recorder.
func NewTranscriptRecorder(limits ...ToolOutputLimit) *TranscriptRecorder {
	recorder := &TranscriptRecorder{}
	if len(limits) > 0 {
		recorder.limit = limits[0]
	}
	return recorder
}

// Add folds one event into the transcript in arrival order.
func (b *TranscriptRecorder) Add(ev event.StreamEvent) {
	if b == nil {
		return
	}
	ev = LimitStreamEvent(ev, b.limit)
	b.mu.Lock()
	defer b.mu.Unlock()
	switch ev.Type {
	case event.ReasoningDelta:
		b.appendReasoning(ev.Delta)
	case event.TextDelta:
		b.appendText(ev.Delta)
	case event.ToolCallStart:
		b.upsertToolCallStart(ev)
	case event.ToolCallEnd:
		b.flushAssistant()
		b.appendToolResult(ev)
	case event.ToolApprovalRequest:
		b.attachToolMetadata(ev, "approval", approvalTranscriptMetadata(ev))
	case event.UserInputRequest:
		b.attachToolMetadata(ev, "user_input", userInputTranscriptMetadata(ev))
	}
}

// Messages finalizes and returns the transcript. fallbackText is used when
// the runtime never streamed a text delta (some agents only return final
// text). Safe to call more than once; finalization is idempotent on the
// accumulated state.
func (b *TranscriptRecorder) Messages(fallbackText string) []sdk.Message {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.sawTextDelta {
		b.appendText(strings.TrimSpace(fallbackText))
	}
	b.flushAssistant()
	return append([]sdk.Message(nil), b.output...)
}

func (b *TranscriptRecorder) flushReasoning() {
	if b.reasoning.Len() == 0 {
		return
	}
	b.assistantParts = append(b.assistantParts, sdk.ReasoningPart{Text: b.reasoning.String()})
	b.reasoning.Reset()
}

func (b *TranscriptRecorder) flushText() {
	if b.text.Len() == 0 {
		return
	}
	b.assistantParts = append(b.assistantParts, sdk.TextPart{Text: b.text.String()})
	b.text.Reset()
}

func (b *TranscriptRecorder) assistantHasToolCall() bool {
	for _, part := range b.assistantParts {
		if _, ok := part.(sdk.ToolCallPart); ok {
			return true
		}
	}
	return false
}

func (b *TranscriptRecorder) flushAssistant() {
	b.flushReasoning()
	b.flushText()
	if len(b.assistantParts) == 0 {
		return
	}
	b.output = append(b.output, sdk.Message{
		Role:    sdk.MessageRoleAssistant,
		Content: append([]sdk.MessagePart(nil), b.assistantParts...),
	})
	b.assistantParts = b.assistantParts[:0]
}

func (b *TranscriptRecorder) appendText(delta string) {
	if delta == "" {
		return
	}
	if b.assistantHasToolCall() {
		b.flushAssistant()
	}
	b.sawTextDelta = true
	b.text.WriteString(delta)
}

func (b *TranscriptRecorder) appendReasoning(delta string) {
	if delta == "" {
		return
	}
	if b.text.Len() > 0 || b.assistantHasToolCall() {
		b.flushAssistant()
	}
	b.reasoning.WriteString(delta)
}

func (b *TranscriptRecorder) appendToolResult(ev event.StreamEvent) {
	result := ev.Result
	isError := strings.TrimSpace(ev.Error) != "" || resultIsMCPError(result)
	if result == nil && isError {
		result = strings.TrimSpace(ev.Error)
	}
	b.output = append(b.output, sdk.ToolMessage(sdk.ToolResultPart{
		ToolCallID: strings.TrimSpace(ev.ToolCallID),
		ToolName:   strings.TrimSpace(ev.ToolName),
		Result:     result,
		IsError:    isError,
	}))
}

func resultIsMCPError(result any) bool {
	obj, ok := result.(map[string]any)
	if !ok {
		return false
	}
	isError, _ := obj["isError"].(bool)
	return isError
}

// findToolCallPart matches by ID when the event has one, by name otherwise.
// Approval events can arrive before the matching tool_call_start, so both
// attachToolMetadata and upsertToolCallStart merge into the same part.
func (b *TranscriptRecorder) findToolCallPart(toolCallID, toolName string) int {
	for idx, part := range b.assistantParts {
		toolCall, ok := part.(sdk.ToolCallPart)
		if !ok {
			continue
		}
		if toolCallID != "" {
			if strings.TrimSpace(toolCall.ToolCallID) == toolCallID {
				return idx
			}
			continue
		}
		if toolName != "" && strings.TrimSpace(toolCall.ToolName) == toolName {
			return idx
		}
	}
	return -1
}

func (b *TranscriptRecorder) attachToolMetadata(ev event.StreamEvent, key string, value map[string]any) {
	toolCallID := strings.TrimSpace(ev.ToolCallID)
	toolName := strings.TrimSpace(ev.ToolName)
	if idx := b.findToolCallPart(toolCallID, toolName); idx >= 0 {
		toolCall := b.assistantParts[idx].(sdk.ToolCallPart)
		if toolCall.ProviderMetadata == nil {
			toolCall.ProviderMetadata = map[string]any{}
		}
		toolCall.ProviderMetadata[key] = value
		if ev.Input != nil {
			toolCall.Input = ev.Input
		}
		b.assistantParts[idx] = toolCall
		return
	}
	if b.text.Len() > 0 || b.reasoning.Len() > 0 {
		b.flushAssistant()
	}
	b.assistantParts = append(b.assistantParts, sdk.ToolCallPart{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Input:      ev.Input,
		ProviderMetadata: map[string]any{
			key: value,
		},
	})
}

func (b *TranscriptRecorder) upsertToolCallStart(ev event.StreamEvent) {
	b.flushReasoning()
	b.flushText()
	toolCallID := strings.TrimSpace(ev.ToolCallID)
	toolName := strings.TrimSpace(ev.ToolName)
	if idx := b.findToolCallPart(toolCallID, toolName); idx >= 0 {
		toolCall := b.assistantParts[idx].(sdk.ToolCallPart)
		if toolCallID != "" {
			toolCall.ToolCallID = toolCallID
		}
		if toolName != "" {
			toolCall.ToolName = toolName
		}
		if ev.Input != nil {
			toolCall.Input = ev.Input
		}
		b.assistantParts[idx] = toolCall
		return
	}
	b.assistantParts = append(b.assistantParts, sdk.ToolCallPart{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Input:      ev.Input,
	})
}

func userInputTranscriptMetadata(ev event.StreamEvent) map[string]any {
	status := strings.TrimSpace(ev.Status)
	if status == "" {
		status = userinput.StatusPending
	}
	userInputID := strings.TrimSpace(ev.UserInputID)
	if userInputID == "" {
		if value, _ := ev.Metadata["user_input_id"].(string); value != "" {
			userInputID = strings.TrimSpace(value)
		}
	}
	metadata := map[string]any{
		"user_input_id": userInputID,
		"short_id":      ev.ShortID,
		"status":        status,
		"ui_payload":    ev.Metadata["ui_payload"],
	}
	if answers := userinput.AnswersFromStored(ev.Metadata["answers"]); len(answers) > 0 {
		metadata["answers"] = answers
	}
	return metadata
}

func approvalTranscriptMetadata(ev event.StreamEvent) map[string]any {
	status := strings.TrimSpace(ev.Status)
	if status == "" {
		status = toolapproval.StatusPending
	}
	approvalID := strings.TrimSpace(ev.ApprovalID)
	if approvalID == "" {
		if value, _ := ev.Metadata["approval_id"].(string); value != "" {
			approvalID = strings.TrimSpace(value)
		}
	}
	return map[string]any{
		"approval_id": approvalID,
		"short_id":    ev.ShortID,
		"status":      status,
		"can_approve": strings.EqualFold(status, toolapproval.StatusPending),
	}
}

// TranscriptFromEvents builds a transcript by folding a complete event
// sequence. Production uses the incremental builder wired into the prompt
// collector; this convenience exists for tests and offline tooling.
func TranscriptFromEvents(events []event.StreamEvent, fallbackText string) []sdk.Message {
	builder := NewTranscriptRecorder()
	for _, ev := range events {
		builder.Add(ev)
	}
	return builder.Messages(fallbackText)
}

// AppendTranscriptText appends extra text (e.g. a failure note) to a
// finalized transcript with the same merge semantics the builder uses while
// streaming: merge into the trailing assistant text when possible, otherwise
// start a new assistant message.
func AppendTranscriptText(messages []sdk.Message, delta string) []sdk.Message {
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return messages
	}
	out := append([]sdk.Message(nil), messages...)
	if len(out) > 0 {
		last := out[len(out)-1]
		if last.Role == sdk.MessageRoleAssistant && !messageHasToolCall(last) {
			if len(last.Content) > 0 {
				if text, ok := last.Content[len(last.Content)-1].(sdk.TextPart); ok {
					text.Text = strings.TrimSpace(text.Text + "\n\n" + delta)
					content := append([]sdk.MessagePart(nil), last.Content...)
					content[len(content)-1] = text
					last.Content = content
					out[len(out)-1] = last
					return out
				}
			}
			content := append([]sdk.MessagePart(nil), last.Content...)
			content = append(content, sdk.TextPart{Text: delta})
			last.Content = content
			out[len(out)-1] = last
			return out
		}
	}
	return append(out, sdk.Message{
		Role:    sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.TextPart{Text: delta}},
	})
}

func messageHasToolCall(message sdk.Message) bool {
	for _, part := range message.Content {
		if _, ok := part.(sdk.ToolCallPart); ok {
			return true
		}
	}
	return false
}
