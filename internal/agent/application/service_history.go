package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
	toolapproval "github.com/memohai/memoh/internal/agent/decision/approval"
	userinput "github.com/memohai/memoh/internal/agent/decision/input"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/textutil"
)

func injectWorkspaceTransitionRecords(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(records) == 0 {
		return records
	}
	result := make([]historyfrag.HistoryRecord, 0, len(records)+2)
	var previous *WorkspaceTarget
	for _, record := range records {
		if current := workspaceTargetFromMetadata(record.Metadata); current != nil && !sameWorkspaceTarget(previous, current) {
			text := renderWorkspaceTransition(previous, current)
			result = append(result, historyfrag.HistoryRecord{ModelMessage: ModelMessage{Role: "system", Content: newTextContent(text)}})
			previous = current
		}
		result = append(result, record)
	}
	return result
}

func renderWorkspaceTransition(previous, current *WorkspaceTarget) string {
	if current == nil || strings.TrimSpace(current.TargetID) == "" {
		return ""
	}
	if previous != nil && sameWorkspaceTarget(previous, current) {
		return ""
	}
	if previous == nil {
		return fmt.Sprintf(
			"[Execution location] The default Computer is %q (target_id=%q, kind=%q). Workspace tools that omit target_id run there.",
			current.Name,
			current.TargetID,
			current.Kind,
		)
	}
	return fmt.Sprintf(
		"[Execution location changed] The default Computer changed from %q (target_id=%q, kind=%q) to %q (target_id=%q, kind=%q). Earlier file and command results belong to their recorded Computer. Files, processes, and working-directory state do not transfer between Computers; inspect the new Computer before continuing.",
		previous.Name,
		previous.TargetID,
		previous.Kind,
		current.Name,
		current.TargetID,
		current.Kind,
	)
}

func sameWorkspaceTarget(left, right *WorkspaceTarget) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.TrimSpace(left.TargetID) == strings.TrimSpace(right.TargetID) &&
		strings.TrimSpace(left.Kind) == strings.TrimSpace(right.Kind)
}

func workspaceTargetFromMetadata(metadata map[string]any) *WorkspaceTarget {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["execution_location"].(map[string]any)
	if !ok {
		return nil
	}
	target := &WorkspaceTarget{
		TargetID: strings.TrimSpace(readAnyString(raw["target_id"])),
		Kind:     strings.TrimSpace(readAnyString(raw["kind"])),
		Name:     strings.TrimSpace(readAnyString(raw["name"])),
	}
	if target.TargetID == "" {
		return nil
	}
	if target.Name == "" {
		target.Name = target.TargetID
	}
	return target
}

func (s *Service) currentWorkspaceContextMessage(ctx context.Context, req ChatRequest) *ModelMessage {
	current := req.WorkspaceTarget
	if current == nil || strings.TrimSpace(current.TargetID) == "" {
		return nil
	}
	var previous *WorkspaceTarget
	if s != nil && s.sessionService != nil && strings.TrimSpace(req.ThreadID) != "" {
		if sess, err := s.sessionService.Get(ctx, req.ThreadID); err == nil {
			if raw, ok := sess.Metadata["workspace_target"].(map[string]any); ok {
				previous = workspaceTargetFromMetadata(map[string]any{"execution_location": raw})
			}
		}
	}
	text := renderWorkspaceTransition(previous, current)
	if text == "" {
		return nil
	}
	message := ModelMessage{Role: "system", Content: newTextContent(text)}
	return &message
}

func (s *Service) loadHistoryRecords(ctx context.Context, fallback historyfrag.ScopeFallback, sessionID string, maxContextMinutes int) ([]historyfrag.HistoryRecord, error) {
	if s.messageService == nil {
		return nil, nil
	}
	since := time.Now().UTC().Add(-time.Duration(maxContextMinutes) * time.Minute)
	var msgs []messagepkg.Message
	var err error
	if strings.TrimSpace(sessionID) != "" {
		msgs, err = s.messageService.ListActiveSinceBySession(ctx, sessionID, since)
	} else {
		msgs, err = s.messageService.ListActiveSince(ctx, fallback.ChatID, since)
	}
	if err != nil {
		return nil, err
	}
	result := make([]historyfrag.HistoryRecord, 0, len(msgs))
	for _, m := range msgs {
		record, err := historyfrag.FromDBMessageWithLogger(s.logger, m, fallback)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func historyScopeFallbackFromChatRequest(req ChatRequest) historyfrag.ScopeFallback {
	return historyfrag.ScopeFallback{
		ChatID:           strings.TrimSpace(req.ChatID),
		ConversationType: strings.TrimSpace(req.ConversationType),
		ConversationName: strings.TrimSpace(req.ConversationName),
		ReplyTarget:      strings.TrimSpace(req.ReplyTarget),
	}
}

func historyScopeFallbackFromUserInputRequest(req userinput.Request) historyfrag.ScopeFallback {
	return historyfrag.ScopeFallback{
		ConversationType: strings.TrimSpace(req.ConversationType),
		ReplyTarget:      strings.TrimSpace(req.ReplyTarget),
	}
}

func historyScopeFallbackFromToolApprovalRequest(req toolapproval.Request) historyfrag.ScopeFallback {
	return historyfrag.ScopeFallback{
		ConversationType: strings.TrimSpace(req.ConversationType),
		ReplyTarget:      strings.TrimSpace(req.ReplyTarget),
	}
}

func (s *Service) ensureRequiredHistoryMessage(ctx context.Context, messages []historyfrag.HistoryRecord, req ChatRequest) ([]historyfrag.HistoryRecord, error) {
	messageID := strings.TrimSpace(req.RequiredHistoryMessageID)
	if messageID == "" || s.messageService == nil || strings.TrimSpace(req.ThreadID) == "" {
		return messages, nil
	}
	for i, item := range messages {
		if strings.TrimSpace(item.DBMessageID) == messageID {
			messages[i].Required = true
			return messages, nil
		}
	}
	window, err := s.messageService.ListVisibleFromBySession(ctx, req.ThreadID, messageID)
	if err != nil {
		return nil, err
	}
	fallback := historyScopeFallbackFromChatRequest(req)
	required := make([]historyfrag.HistoryRecord, 0, len(window))
	for _, msg := range window {
		record, err := historyfrag.FromDBMessage(msg, fallback)
		if err != nil {
			return nil, err
		}
		required = append(required, record)
	}
	required = pruneHistoryForGateway(required)
	required = filterMessagesBeforeID(required, req.HistoryCutoffBeforeMessageID)
	if !containsHistoryRecord(required, messageID) {
		return nil, errors.New("required history message is not visible")
	}
	for i := range required {
		if strings.TrimSpace(required[i].DBMessageID) == messageID {
			required[i].Required = true
		}
	}
	return mergeRequiredHistoryWindow(messages, required), nil
}

func containsHistoryRecord(messages []historyfrag.HistoryRecord, messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	for _, item := range messages {
		if strings.TrimSpace(item.DBMessageID) == messageID {
			return true
		}
	}
	return false
}

func mergeRequiredHistoryWindow(messages []historyfrag.HistoryRecord, required []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(required) == 0 {
		return messages
	}
	requiredIDs := make(map[string]struct{}, len(required))
	for _, item := range required {
		if id := strings.TrimSpace(item.DBMessageID); id != "" {
			requiredIDs[id] = struct{}{}
		}
	}
	merged := make([]historyfrag.HistoryRecord, 0, len(messages)+len(required))
	for _, item := range messages {
		if _, ok := requiredIDs[strings.TrimSpace(item.DBMessageID)]; ok {
			continue
		}
		merged = append(merged, item)
	}
	return append(merged, required...)
}

func filterMessagesBeforeID(messages []historyfrag.HistoryRecord, messageID string) []historyfrag.HistoryRecord {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return messages
	}
	for i, item := range messages {
		if strings.TrimSpace(item.DBMessageID) == messageID {
			return messages[:i]
		}
	}
	return nil
}

func compactionSummaryScope(botID, chatID, sessionID, conversationType, conversationName, replyTarget string) contextfrag.Scope {
	return contextfrag.Scope{
		BotID:            strings.TrimSpace(botID),
		ChatID:           strings.TrimSpace(chatID),
		SessionID:        strings.TrimSpace(sessionID),
		ConversationType: strings.TrimSpace(conversationType),
		ConversationName: strings.TrimSpace(conversationName),
		ReplyTarget:      strings.TrimSpace(replyTarget),
	}
}

func dedupePersistedCurrentUserMessage(messages []historyfrag.HistoryRecord, req ChatRequest) []historyfrag.HistoryRecord {
	if !req.UserMessagePersisted || len(messages) == 0 {
		return messages
	}

	targetSessionID := strings.TrimSpace(req.ThreadID)
	targetExternalID := strings.TrimSpace(req.ExternalMessageID)
	targetPlatform := strings.TrimSpace(req.CurrentChannel)
	targetSenderChannelID := strings.TrimSpace(req.SourceChannelIdentityID)
	if targetExternalID == "" {
		return messages
	}

	for i := len(messages) - 1; i >= 0; i-- {
		item := messages[i]
		if !strings.EqualFold(strings.TrimSpace(item.ModelMessage.Role), "user") {
			continue
		}
		if strings.TrimSpace(item.ExternalMessageID) != targetExternalID {
			continue
		}
		if targetSessionID != "" && item.SessionID != "" && item.SessionID != targetSessionID {
			continue
		}
		if targetPlatform != "" && item.Platform != "" && !strings.EqualFold(item.Platform, targetPlatform) {
			continue
		}
		if targetSenderChannelID != "" && item.SenderChannelIdentityID != "" && item.SenderChannelIdentityID != targetSenderChannelID {
			continue
		}
		return append(messages[:i], messages[i+1:]...)
	}

	return messages
}

func estimateMessageTokens(msg ModelMessage) int {
	text := msg.TextContent()
	if len(text) == 0 {
		data, _ := json.Marshal(msg.Content)
		return textutil.EstimateTokensFromBytes(len(data))
	}
	return textutil.EstimateTokensFromBytes(len(text))
}

func trimMessagesByTokens(log *slog.Logger, messages []historyfrag.HistoryRecord, maxTokens int) ([]ModelMessage, int) {
	trimmed, _, totalTokens := trimMessagesAndRecordsByTokens(log, messages, maxTokens)
	return trimmed, totalTokens
}

// totalCompactableHistoryTokens estimates the tokens held by raw history rows
// only. Active summaries are excluded: compaction can never shrink them, so
// counting them toward the compaction trigger would re-fire it on every
// request once accumulated summaries alone cross the threshold.
func totalCompactableHistoryTokens(records []historyfrag.HistoryRecord) int {
	total := 0
	for _, record := range records {
		if record.Kind == contextfrag.KindConversationSummary || record.Lifecycle == historyfrag.LifecycleActiveSummary {
			continue
		}
		total += estimateMessageTokens(record.ModelMessage)
	}
	return total
}

func trimMessagesAndRecordsByTokens(log *slog.Logger, messages []historyfrag.HistoryRecord, maxTokens int) ([]ModelMessage, []historyfrag.HistoryRecord, int) {
	if maxTokens == 0 || len(messages) == 0 {
		totalTokens := 0
		for _, m := range messages {
			totalTokens += estimateMessageTokens(m.ModelMessage)
		}
		return historyfrag.ToModelMessages(messages), messages, totalTokens
	}

	// Scan from newest to oldest, accumulating per-message estimated context
	// token costs. Each message's cost represents the tokens it occupies in the
	// context window (not the output tokens it generated). We use a character-
	// based estimate for all messages since this measures context window impact.
	scannedTokens := 0
	cutoff := 0
	for i := len(messages) - 1; i >= 0; i-- {
		scannedTokens += estimateMessageTokens(messages[i].ModelMessage)
		if scannedTokens > maxTokens {
			cutoff = i + 1
			break
		}
	}

	// Keep provider-valid message order: a "tool" message must follow a preceding
	// assistant tool call. When history is head-trimmed, a leading tool message
	// may become orphaned and cause provider 400 errors.
	for cutoff < len(messages) && strings.EqualFold(strings.TrimSpace(messages[cutoff].ModelMessage.Role), "tool") {
		cutoff++
	}
	cutoff, totalTokens := fitRequiredMessagesWithinBudget(messages, cutoff, maxTokens)

	if cutoff > 0 && log != nil {
		log.Info("trimMessagesByTokens: context trimmed",
			slog.Int("total_messages", len(messages)),
			slog.Int("estimated_tokens", totalTokens),
			slog.Int("max_tokens", maxTokens),
			slog.Int("cutoff_index", cutoff),
			slog.Int("kept_messages", len(messages)-cutoff),
		)
	}

	requiredPrefix := requiredMessagesBeforeCutoff(messages, cutoff)
	retained := make([]historyfrag.HistoryRecord, 0, len(messages)-cutoff+len(requiredPrefix))
	retained = append(retained, requiredPrefix...)
	retained = append(retained, messages[cutoff:]...)
	result := make([]ModelMessage, 0, len(retained))
	if cutoff > 0 {
		// Add a truncation notice at the beginning so the LLM knows earlier
		// context was trimmed and it can use tools (memory, search) to look up
		// past information if needed.
		result = append(result, ModelMessage{
			Role: "system",
			Content: newTextContent(
				"[System Notice] Earlier conversation history has been trimmed to fit the context window. " +
					"If you need information from earlier in the conversation, use the available tools " +
					"(such as memory_read or web search) to retrieve it.",
			),
		})
	}
	for _, m := range retained {
		result = append(result, m.ModelMessage)
	}
	return result, retained, totalTokens
}

func fitRequiredMessagesWithinBudget(messages []historyfrag.HistoryRecord, cutoff int, maxTokens int) (int, int) {
	if maxTokens <= 0 || len(messages) == 0 {
		return cutoff, estimateMessagesTokens(messages)
	}
	if cutoff < 0 {
		cutoff = 0
	}
	if cutoff > len(messages) {
		cutoff = len(messages)
	}
	for {
		requiredPrefix := requiredMessagesBeforeCutoff(messages, cutoff)
		totalTokens := estimateMessagesTokens(requiredPrefix) + estimateMessagesTokens(messages[cutoff:])
		if totalTokens <= maxTokens || cutoff >= len(messages) {
			return cutoff, totalTokens
		}
		cutoff++
		for cutoff < len(messages) && strings.EqualFold(strings.TrimSpace(messages[cutoff].ModelMessage.Role), "tool") {
			cutoff++
		}
	}
}

func estimateMessagesTokens(messages []historyfrag.HistoryRecord) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m.ModelMessage)
	}
	return total
}

func requiredMessagesBeforeCutoff(messages []historyfrag.HistoryRecord, cutoff int) []historyfrag.HistoryRecord {
	if cutoff <= 0 {
		return nil
	}
	if cutoff > len(messages) {
		cutoff = len(messages)
	}
	var required []historyfrag.HistoryRecord
	for _, m := range messages[:cutoff] {
		if m.Required {
			required = append(required, m)
		}
	}
	return required
}
