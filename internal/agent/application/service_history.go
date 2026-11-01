package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/chat/checkpoint"
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

// loadHistoryRecords loads the thread's history under its history
// checkpoint: what it holds in its order, its summaries and the rows it keeps
// raw, and the rows after its position. What a load reads follows what is
// unsummarized, never the thread's length or age. The rows after the
// checkpoint are byte-budgeted on the database side (CM-ADM-001): admitted
// newest-first within contextTokenBudget (the absolute cap when the budget
// is unset). It reports
// whether the budget left older rows after the checkpoint out: they stay
// compaction candidates, and a request that renders the load compacts them
// rather than send a history that silently starts later. An edit or retry
// renders the checkpoint its replacement keeps and the history before its
// cutoff. A request without a thread has no history.
func (s *Service) loadHistoryRecords(ctx context.Context, fallback historyfrag.ScopeFallback, scope contextfrag.Scope, contextTokenBudget int, cut historyCut) ([]historyfrag.HistoryRecord, bool, error) {
	sessionID := strings.TrimSpace(scope.SessionID)
	if s.messageService == nil || sessionID == "" {
		return nil, false, nil
	}
	held, err := s.historyCheckpoint(ctx, sessionID, cut)
	if err != nil {
		return nil, false, err
	}
	kept := make(map[string]messagepkg.Message)
	if rows := held.RowIDs(); len(rows) > 0 {
		msgs, err := s.messageService.ListHistoryByIDs(ctx, sessionID, rows)
		if err != nil {
			return nil, false, fmt.Errorf("load history checkpoint rows: %w", err)
		}
		for _, msg := range msgs {
			kept[msg.ID] = msg
		}
	}
	load, err := s.messageService.ListHistoryWithinBytes(ctx, sessionID, messagepkg.HistoryWindow{
		After:    held.Position,
		Before:   cut.before,
		MaxBytes: s.historyLoadMaxBytes(contextTokenBudget),
	})
	if err != nil {
		return nil, false, err
	}
	msgs := load.Messages
	result := make([]historyfrag.HistoryRecord, 0, len(held.Items)+len(msgs))
	for _, item := range held.Items {
		if item.SummaryID != "" {
			result = append(result, historyfrag.SummaryRecord(item.SummaryID, item.Summary, scope))
			continue
		}
		msg, ok := kept[item.RowID]
		if !ok {
			return nil, false, fmt.Errorf("history checkpoint %s keeps row %s, which is not a context row", held.ID, item.RowID)
		}
		record, err := historyfrag.FromDBMessageWithLogger(s.logger, msg, fallback)
		if err != nil {
			return nil, false, err
		}
		result = append(result, record)
	}
	// The budget covers content only; metadata arrives undecoded. Release each
	// row once converted so at most one row's metadata is decoded at a time.
	for i := range msgs {
		record, err := historyfrag.FromDBMessageWithLogger(s.logger, msgs[i], fallback)
		if err != nil {
			return nil, false, err
		}
		msgs[i] = messagepkg.Message{}
		result = append(result, record)
	}
	return result, load.Truncated, nil
}

// historyCheckpoint is the checkpoint a request renders: the session's
// active one, or the one an edit or retry's replacement keeps.
func (s *Service) historyCheckpoint(ctx context.Context, sessionID string, cut historyCut) (checkpoint.Checkpoint, error) {
	var (
		held checkpoint.Checkpoint
		err  error
	)
	if cut.replacedTurnID != "" {
		held, err = s.checkpoints.KeptBeforeTurn(ctx, sessionID, cut.replacedTurnID)
	} else {
		held, err = s.checkpoints.Active(ctx, sessionID)
	}
	if err != nil {
		return checkpoint.Checkpoint{}, fmt.Errorf("load history checkpoint: %w", err)
	}
	return held, nil
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
	if containsHistoryRecord(messages, messageID) {
		return messages, nil
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
