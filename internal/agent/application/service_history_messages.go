package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/chat/timeline"
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

	trs := s.loadTurnResponses(ctx, sessionID, contextTokenBudget)
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
// timeline composition. A load failure degrades into the bounded admission
// window — the pipeline trim below still caps the recomposed raw history —
// and is surfaced with a stable key instead of passing silently (CM-ADM-003).
func (s *Service) loadTimelineArtifacts(ctx context.Context, botID, sessionID string) []timeline.CompactionArtifact {
	if s.queries == nil {
		return nil
	}
	artifacts, err := compaction.NewTimelineArtifactSource(s.queries).ActiveCompactionArtifacts(ctx, botID, sessionID)
	if err != nil {
		s.logger.Warn("context_admission_degraded",
			slog.String("path", "pipeline_chat"),
			slog.String("reason", "artifact_load_failed"),
			slog.String("session_id", sessionID),
			slog.Any("error", err))
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
// for use as the TR stream in pipeline-based context assembly. The load is
// byte-budgeted on the database side (CM-ADM-001): rows are admitted
// newest-first within the context budget, so a dense 24-hour window can
// never materialize more than the budget's worth of payload.
func (s *Service) loadTurnResponses(ctx context.Context, sessionID string, contextTokenBudget int) []timeline.TurnResponseEntry {
	if s.messageService == nil {
		return nil
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	msgs, err := s.messageService.ListActiveSinceBySessionWithinBytes(ctx, sessionID, since, s.historyLoadMaxBytes(contextTokenBudget))
	if err != nil {
		s.logger.Warn("load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}
	return timeline.DecodeTurnResponseEntries(msgs)
}

// historyLoadMaxBytes converts a token budget into the byte budget used for
// database-side history admission; a missing budget falls back to the
// absolute cap so no load path is ever unbounded.
func (s *Service) historyLoadMaxBytes(contextTokenBudget int) int64 {
	if contextTokenBudget <= 0 {
		contextTokenBudget = s.contextAbsoluteMaxTokens()
	}
	return int64(contextTokenBudget) * contextfrag.EstimateBytesPerToken
}

// stripToolMessages removes bulky tool interactions from the context while
// keeping ask_user calls and results. ask_user is conversation-visible: the
// question and the user's answer are part of the chat semantics, not tool noise.
//
// It also caps how much reasoning goes back: only the most recent assistant
// message keeps its reasoning blocks. Replayed reasoning otherwise grows without
// bound — every turn carries every earlier turn's blocks, and encrypted ones are
// several hundred bytes each — until long conversations pay for reasoning they
// will never use again. Providers verify the thinking blocks of the latest
// assistant message and filter older turns server-side, so the newest turn is
// the only one that has to survive.
//
// The newest turn keeps its tool calls along with its reasoning. A thinking
// block explains the call it was issued with; replaying it while dropping the
// call shows the model its own decision to use a tool with no record that the
// call happened. The results those calls produced are kept for the same reason.
func stripToolMessages(messages []ModelMessage) []ModelMessage {
	latestAssistant := lastAssistantIndex(messages)
	keptToolCallIDs := map[string]struct{}{}
	// Only a latest turn that actually replays reasoning needs its calls kept:
	// with no thinking block there is nothing whose explanation would dangle,
	// and the tool noise is worth dropping as before.
	if latestAssistant >= 0 && hasReasoningContent(messages[latestAssistant]) {
		keptToolCallIDs = toolCallIDsOf(messages[latestAssistant])
	}
	filtered := make([]ModelMessage, 0, len(messages))
	for i, m := range messages {
		role := strings.TrimSpace(m.Role)
		if strings.EqualFold(role, "tool") {
			if kept := keepAskUserToolResultMessage(m, keptToolCallIDs); kept != nil {
				filtered = append(filtered, *kept)
			}
			continue
		}
		if strings.EqualFold(role, "assistant") {
			// Remove assistant messages that only contain tool calls / reasoning
			// with no visible text. Tool-call metadata may live either in
			// ToolCalls or in structured content parts.
			if hasToolCallContent(m) {
				keepLatestTurn := i == latestAssistant && hasReasoningContent(m)
				stripped, ok := stripNonAskUserToolCalls(m, keepLatestTurn)
				if !ok {
					continue
				}
				m = stripped
			} else if i != latestAssistant {
				// A plain conversational turn has no tool call to strip, but its
				// reasoning still accumulates, so drop it here as well.
				m = dropReasoning(m)
			}
		}
		filtered = append(filtered, m)
	}
	return filtered
}

// hasReasoningContent reports whether an assistant message carries a reasoning
// part, including an empty-text redacted one whose payload is all metadata.
func hasReasoningContent(message ModelMessage) bool {
	for _, part := range modelMessageToSDKMessage(message).Content {
		if _, ok := part.(sdk.ReasoningPart); ok {
			return true
		}
	}
	return false
}

// toolCallIDsOf reports the tool call IDs an assistant message issued, across
// both the structured content parts and the legacy top-level ToolCalls field.
// It is only consulted for a message whose calls are being kept, so that the
// results pairing with them can be kept too.
func toolCallIDsOf(message ModelMessage) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, part := range modelMessageToSDKMessage(message).Content {
		if call, ok := part.(sdk.ToolCallPart); ok {
			if id := strings.TrimSpace(call.ToolCallID); id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	for _, call := range message.ToolCalls {
		if id := strings.TrimSpace(call.ID); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

// dropReasoning removes an assistant message's reasoning parts, leaving the rest
// of its content untouched.
//
// A message whose only content is reasoning cannot simply lose it: an emptied
// assistant message is dropped by sanitizeMessages and rejected by providers.
// Keeping the opaque block instead would exempt exactly the shape an
// interruption mid-thinking leaves behind — reasoning with no answer text is
// still checkpointed — and that shape recurs, so the cap would not hold. The
// thinking is projected to text instead: the turn stays alive and readable
// while the block itself stops being replayed.
func dropReasoning(message ModelMessage) ModelMessage {
	parts := modelMessageToSDKMessage(message).Content
	kept := make([]sdk.MessagePart, 0, len(parts))
	var reasoning strings.Builder
	dropped := false
	for _, part := range parts {
		if reasoningPart, ok := part.(sdk.ReasoningPart); ok {
			dropped = true
			reasoning.WriteString(reasoningPart.Text)
			continue
		}
		kept = append(kept, part)
	}
	if !dropped {
		return message
	}
	if len(kept) == 0 {
		// Nothing but reasoning: an empty-text block (a redacted one carries its
		// payload in metadata) leaves no text to project, so the message has to
		// keep its original form rather than vanish.
		if strings.TrimSpace(reasoning.String()) == "" {
			return message
		}
		kept = append(kept, sdk.TextPart{Text: reasoning.String()})
	}
	stripped := modelMessageFromSDKParts(sdk.MessageRoleAssistant, kept, message.Usage)
	stripped.ToolCalls = message.ToolCalls
	return stripped
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

func stripNonAskUserToolCalls(message ModelMessage, keepLatestTurn bool) (ModelMessage, bool) {
	legacyToolCalls := message.ToolCalls
	if !keepLatestTurn {
		legacyToolCalls = keepAskUserLegacyToolCalls(message.ToolCalls)
	}
	text := strings.TrimSpace(message.TextContent())

	keptParts := filterAssistantContextParts(modelMessageToSDKMessage(message).Content, keepLatestTurn)
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

// keepAskUserToolResultMessage keeps a tool message when it carries an ask_user
// answer, or a result pairing with a tool call that survived stripping. The
// latter keeps the newest turn's tool exchange whole: the call is replayed, so
// the result it produced has to be there too.
func keepAskUserToolResultMessage(message ModelMessage, keptToolCallIDs map[string]struct{}) *ModelMessage {
	if strings.EqualFold(strings.TrimSpace(message.Name), userinput.ToolNameAskUser) {
		return &message
	}
	if _, ok := keptToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok && strings.TrimSpace(message.ToolCallID) != "" {
		return &message
	}
	results := filterAskUserToolResults(modelMessageToSDKMessage(message).Content, keptToolCallIDs)
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
// keepLatestTurn preserves the message's reasoning parts and the tool calls they
// were issued with, which the caller sets for the latest assistant turn: its
// thinking blocks are the ones a provider verifies, and they have to be replayed
// whole, empty-text blocks included. The calls travel with them, since a
// thinking block explains a call that must still be there to explain.
func filterAssistantContextParts(parts []sdk.MessagePart, keepLatestTurn bool) []sdk.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	kept := make([]sdk.MessagePart, 0, len(parts))
	for _, part := range parts {
		switch typed := part.(type) {
		case sdk.ToolCallPart:
			if keepLatestTurn || strings.EqualFold(strings.TrimSpace(typed.ToolName), userinput.ToolNameAskUser) {
				kept = append(kept, typed)
			}
		case sdk.ReasoningPart:
			if keepLatestTurn {
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

// filterAskUserToolResults keeps ask_user results, plus results pairing with a
// tool call that survived stripping so the newest turn's exchange stays whole.
func filterAskUserToolResults(parts []sdk.MessagePart, keptToolCallIDs map[string]struct{}) []sdk.MessagePart {
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
			continue
		}
		if id := strings.TrimSpace(result.ToolCallID); id != "" {
			if _, pairs := keptToolCallIDs[id]; pairs {
				kept = append(kept, result)
			}
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
