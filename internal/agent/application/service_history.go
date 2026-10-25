package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

// injectWorkspaceTransitionRecords marks every switch of Computer between
// consecutive located records. The first located record in the window gets
// no marker: the turn's default Computer is stated by the tool usage, and a
// marker derived from where the window happens to start would render a row
// differently from the request that first sent it.
func injectWorkspaceTransitionRecords(records []historyfrag.HistoryRecord) []historyfrag.HistoryRecord {
	if len(records) == 0 {
		return records
	}
	result := make([]historyfrag.HistoryRecord, 0, len(records)+2)
	var previous *WorkspaceTarget
	for _, record := range records {
		if current := workspaceTargetFromMetadata(record.Metadata); current != nil {
			if text := renderWorkspaceTransition(previous, current); text != "" {
				result = append(result, historyfrag.HistoryRecord{
					ModelMessage: ModelMessage{Role: "system", Content: newTextContent(text)},
					Synthetic:    true,
				})
			}
			previous = current
		}
		result = append(result, record)
	}
	return result
}

func renderWorkspaceTransition(previous, current *WorkspaceTarget) string {
	if previous == nil || current == nil || sameWorkspaceTarget(previous, current) {
		return ""
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

// currentWorkspaceTransitionMessage marks a switch of Computer at the current
// request, against the last located record of the history it is sent with —
// the record a later replay compares the request row with, so the marker this
// request sends is the one the next turn replays.
func currentWorkspaceTransitionMessage(history []historyfrag.HistoryRecord, current *WorkspaceTarget) *ModelMessage {
	if current == nil || strings.TrimSpace(current.TargetID) == "" {
		return nil
	}
	var previous *WorkspaceTarget
	for i := len(history) - 1; i >= 0 && previous == nil; i-- {
		previous = workspaceTargetFromMetadata(history[i].Metadata)
	}
	text := renderWorkspaceTransition(previous, current)
	if text == "" {
		return nil
	}
	message := ModelMessage{Role: "system", Content: newTextContent(text)}
	return &message
}

// loadHistoryRecords loads session history by budget, not age. Raw rows are
// read newest-first through the visible-history index; a folded load skips
// summarized payloads and returns one placement row per usable compact group.
// Without a session there is no history; never widen the lookup to the bot.
// Final request admission still enforces token limits.
func (s *Service) loadHistoryRecords(ctx context.Context, fallback historyfrag.ScopeFallback, sessionID string, contextTokenBudget int, fold bool) ([]historyfrag.HistoryRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if s.messageService == nil || sessionID == "" {
		return nil, nil
	}
	msgs, err := s.messageService.ListActiveSinceBySessionWithinBytes(ctx, sessionID, time.Unix(0, 0), s.historyLoadMaxBytes(contextTokenBudget), fold)
	if err != nil {
		return nil, err
	}
	// The budget covers content only; metadata arrives undecoded. Release each
	// row once converted so at most one row's metadata is decoded at a time.
	result := make([]historyfrag.HistoryRecord, 0, len(msgs))
	for i := range msgs {
		record, err := historyfrag.FromDBMessageWithLogger(s.logger, msgs[i], fallback)
		if err != nil {
			return nil, err
		}
		msgs[i] = messagepkg.Message{}
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
		return contextfrag.TokensFromBytes(len(data))
	}
	return contextfrag.TokensFromBytes(len(text) + replayedPartBytes(msg.Content))
}

// replayedPartBytes measures the reasoning and tool exchange parts that ride
// along with assistant text; TextContent alone would leave them uncounted.
func replayedPartBytes(content json.RawMessage) int {
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return 0
	}
	total := 0
	for _, raw := range parts {
		var part struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &part) != nil {
			continue
		}
		switch part.Type {
		case "reasoning", "tool-call", "tool-result":
			total += len(raw)
		}
	}
	return total
}

// historyMessagesFromRecords derives workspace-transition markers from the
// records and projects them to model messages.
func historyMessagesFromRecords(records []historyfrag.HistoryRecord) ([]ModelMessage, []historyfrag.HistoryRecord) {
	annotated := injectWorkspaceTransitionRecords(records)
	messages := make([]ModelMessage, 0, len(annotated))
	for _, record := range annotated {
		messages = append(messages, record.ModelMessage)
	}
	return messages, annotated
}

func estimateMessagesTokens(messages []historyfrag.HistoryRecord) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m.ModelMessage)
	}
	return total
}
