package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/agent/context/compaction"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	"github.com/memohai/memoh/internal/chat/timeline"
)

// buildMessagesFromPipeline assembles chat context from the DCP pipeline's
// RenderedContext (RC) merged with assistant/tool turns (TR) from
// bot_history_messages. This gives chat mode the same event-driven context
// that discuss mode uses, replacing the legacy loadMessages path.
func (s *Service) buildMessagesFromPipeline(ctx context.Context, req ChatRequest, contextTokenBudget int) []ModelMessage {
	sessionID := strings.TrimSpace(req.ThreadID)
	if s.pipeline == nil || sessionID == "" {
		return nil
	}
	rc := s.pipeline.GetRC(sessionID)
	if len(rc) == 0 {
		return nil
	}

	trs := s.loadTurnResponses(ctx, sessionID)
	artifacts := s.loadTimelineArtifacts(ctx, req.BotID, sessionID)

	composed := timeline.ComposeContextWithArtifacts(rc, trs, artifacts)
	if composed == nil {
		return nil
	}

	messages := make([]ModelMessage, 0, len(composed.Messages))
	pinned := make([]bool, 0, len(composed.Messages))
	for _, m := range composed.Messages {
		contentJSON := m.RawContent
		if len(contentJSON) == 0 {
			var err error
			contentJSON, err = json.Marshal(m.Content)
			if err != nil {
				continue
			}
		}
		messages = append(messages, ModelMessage{
			Role:    m.Role,
			Content: contentJSON,
		})
		pinned = append(pinned, m.CompactionArtifactID != "")
	}

	// Apply context token budget trimming to pipeline path as well.
	if contextTokenBudget > 0 && len(messages) > 0 {
		messages = trimPipelineMessagesByTokens(s.logger, messages, pinned, contextTokenBudget)
	}

	return messages
}

// loadTimelineArtifacts projects the session's active compaction frontier for
// timeline composition. Failures degrade to uncompacted context.
func (s *Service) loadTimelineArtifacts(ctx context.Context, botID, sessionID string) []timeline.CompactionArtifact {
	if s.queries == nil {
		return nil
	}
	artifacts, err := compaction.NewTimelineArtifactSource(s.queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
	if err != nil {
		s.logger.Warn("load compaction artifacts failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}
	return artifacts
}

// trimPipelineMessagesByTokens trims pipeline-assembled messages to fit within
// the context token budget using character-based estimation. Pinned messages
// (compaction summaries) survive the dropped prefix.
func trimPipelineMessagesByTokens(log *slog.Logger, messages []ModelMessage, pinned []bool, maxTokens int) []ModelMessage {
	totalTokens := 0
	cutoff := 0
	for i := len(messages) - 1; i >= 0; i-- {
		totalTokens += estimateMessageTokens(messages[i])
		if totalTokens > maxTokens {
			cutoff = i + 1
			break
		}
	}

	// Avoid orphaned tool messages at the cutoff boundary.
	for cutoff < len(messages) && strings.EqualFold(strings.TrimSpace(messages[cutoff].Role), "tool") {
		cutoff++
	}

	if cutoff == 0 {
		return messages
	}

	kept := make([]ModelMessage, 0, len(messages)-cutoff)
	for i := 0; i < cutoff; i++ {
		if i < len(pinned) && pinned[i] {
			kept = append(kept, messages[i])
		}
	}
	kept = append(kept, messages[cutoff:]...)

	if log != nil {
		log.Info("trimPipelineMessagesByTokens: context trimmed",
			slog.Int("total_messages", len(messages)),
			slog.Int("estimated_tokens", totalTokens),
			slog.Int("max_tokens", maxTokens),
			slog.Int("kept_messages", len(kept)),
		)
	}

	return kept
}

// loadTurnResponses loads recent assistant/tool messages from bot_history_messages
// for use as the TR stream in pipeline-based context assembly.
func (s *Service) loadTurnResponses(ctx context.Context, sessionID string) []timeline.TurnResponseEntry {
	if s.messageService == nil {
		return nil
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	msgs, err := s.messageService.ListActiveSinceBySession(ctx, sessionID, since)
	if err != nil {
		s.logger.Warn("load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}
	return timeline.DecodeTurnResponseEntries(msgs)
}

// stripToolMessages removes bulky tool interactions from the context while
// keeping ask_user calls and results. ask_user is conversation-visible: the
// question and the user's answer are part of the chat semantics, not tool noise.
//
// Reasoning in the most recent assistant message is preserved. Providers verify
// the thinking blocks of the latest assistant turn and reject a sequence they
// did not produce, while older turns are filtered server-side and need no
// pruning here. Keeping only the newest turn satisfies that check without
// letting encrypted reasoning accumulate across a long conversation.
func stripToolMessages(messages []ModelMessage) []ModelMessage {
	latestAssistant := lastAssistantIndex(messages)
	filtered := make([]ModelMessage, 0, len(messages))
	for i, m := range messages {
		role := strings.TrimSpace(m.Role)
		if strings.EqualFold(role, "tool") {
			if kept := keepAskUserToolResultMessage(m); kept != nil {
				filtered = append(filtered, *kept)
			}
			continue
		}
		// Remove assistant messages that only contain tool calls / reasoning with
		// no visible text. Tool-call metadata may live either in ToolCalls or in
		// structured content parts.
		if strings.EqualFold(role, "assistant") && hasToolCallContent(m) {
			stripped, ok := stripNonAskUserToolCalls(m, i == latestAssistant)
			if !ok {
				continue
			}
			m = stripped
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// lastAssistantIndex reports the index of the most recent assistant message, or
// -1 when there is none.
func lastAssistantIndex(messages []ModelMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "assistant") {
			return i
		}
	}
	return -1
}

func hasToolCallContent(message ModelMessage) bool {
	if len(message.ToolCalls) > 0 {
		return true
	}
	for _, part := range message.ContentParts() {
		if part.Type == "tool-call" {
			return true
		}
	}
	return false
}

func stripNonAskUserToolCalls(message ModelMessage, keepReasoning bool) (ModelMessage, bool) {
	legacyToolCalls := keepAskUserLegacyToolCalls(message.ToolCalls)
	text := strings.TrimSpace(message.TextContent())

	keptParts := filterAssistantContextParts(modelMessageToSDKMessage(message).Content, keepReasoning)
	if len(keptParts) > 0 {
		message = modelMessageFromSDKParts(sdk.MessageRoleAssistant, keptParts, message.Usage)
		message.ToolCalls = legacyToolCalls
		return message, true
	}

	message.ToolCalls = legacyToolCalls
	if len(message.ToolCalls) > 0 {
		if text != "" {
			message.Content = newTextContent(text)
		}
		return message, true
	}
	if text == "" {
		return ModelMessage{}, false
	}
	message.Content = newTextContent(text)
	return message, true
}

func keepAskUserToolResultMessage(message ModelMessage) *ModelMessage {
	if strings.EqualFold(strings.TrimSpace(message.Name), userinput.ToolNameAskUser) {
		return &message
	}
	results := filterAskUserToolResults(modelMessageToSDKMessage(message).Content)
	if len(results) == 0 {
		return nil
	}
	message = modelMessageFromSDKParts(sdk.MessageRoleTool, results, message.Usage)
	return &message
}

func keepAskUserLegacyToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	kept := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		if strings.EqualFold(strings.TrimSpace(call.Function.Name), userinput.ToolNameAskUser) {
			kept = append(kept, call)
		}
	}
	return kept
}

// filterAssistantContextParts drops tool noise from an assistant message.
// keepReasoning preserves the message's reasoning parts, which the caller sets
// for the latest assistant turn: its thinking blocks are the ones a provider
// verifies, and they have to be replayed whole, empty-text blocks included.
func filterAssistantContextParts(parts []sdk.MessagePart, keepReasoning bool) []sdk.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	kept := make([]sdk.MessagePart, 0, len(parts))
	for _, part := range parts {
		switch typed := part.(type) {
		case sdk.ToolCallPart:
			if strings.EqualFold(strings.TrimSpace(typed.ToolName), userinput.ToolNameAskUser) {
				kept = append(kept, typed)
			}
		case sdk.ReasoningPart:
			if keepReasoning {
				kept = append(kept, typed)
			}
		case sdk.ToolResultPart:
			continue
		case sdk.TextPart:
			if strings.TrimSpace(typed.Text) != "" {
				kept = append(kept, typed)
			}
		default:
			kept = append(kept, part)
		}
	}
	return kept
}

func filterAskUserToolResults(parts []sdk.MessagePart) []sdk.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	kept := make([]sdk.MessagePart, 0, len(parts))
	for _, part := range parts {
		result, ok := part.(sdk.ToolResultPart)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(result.ToolName), userinput.ToolNameAskUser) {
			kept = append(kept, result)
		}
	}
	return kept
}

func modelMessageFromSDKParts(role sdk.MessageRole, parts []sdk.MessagePart, usage json.RawMessage) ModelMessage {
	converted := sdkMessagesToModelMessages([]sdk.Message{{Role: role, Content: parts}})
	if len(converted) == 0 {
		return ModelMessage{Role: string(role)}
	}
	converted[0].Usage = usage
	return converted[0]
}
