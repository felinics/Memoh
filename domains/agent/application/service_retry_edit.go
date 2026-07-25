package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
)

type RetryLatestMessageInput struct {
	BotID                  string
	SessionID              string
	StreamID               string
	MessageID              string
	ActorChannelIdentityID string
	ActorUserID            string
	ChatToken              string
	Model                  string
	ReasoningEffort        string
	WorkspaceTargetID      string
	ToolHTTPURL            string
}

type EditLatestMessageInput struct {
	BotID                  string
	SessionID              string
	StreamID               string
	MessageID              string
	Text                   string
	Attachments            []agentdomain.Attachment
	ActorChannelIdentityID string
	ActorUserID            string
	ChatToken              string
	Model                  string
	ReasoningEffort        string
	WorkspaceTargetID      string
	ToolHTTPURL            string
}

func (s *Service) RetryLatestMessageWS(ctx context.Context, input RetryLatestMessageInput, eventCh chan<- WSStreamEvent, abortCh <-chan struct{}) error {
	if s == nil || s.messageService == nil {
		return errors.New("message service not configured")
	}
	sessionID := strings.TrimSpace(input.SessionID)
	messageID := strings.TrimSpace(input.MessageID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if messageID == "" {
		return errors.New("message id is required")
	}

	turn, target, err := s.latestVisibleTurnAndMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(target.Role, "assistant") {
		return errors.New("only latest assistant messages can be retried")
	}
	if err := s.ensureLatestVisibleTurn(ctx, sessionID, turn.ID); err != nil {
		return errors.New("only the latest assistant message can be retried")
	}
	requestMessageID := strings.TrimSpace(turn.RequestMessageID)
	if requestMessageID == "" {
		return errors.New("retry target has no request message")
	}
	requestMessage, err := s.messageService.GetByIDBySession(ctx, sessionID, requestMessageID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(requestMessage.Role, "user") {
		return errors.New("retry target request is not a user message")
	}
	cutoffMessageID := strings.TrimSpace(turn.AssistantMessageID)
	if cutoffMessageID == "" {
		cutoffMessageID = target.ID
	}

	req := ChatRequest{
		BotID:                        strings.TrimSpace(input.BotID),
		ChatID:                       strings.TrimSpace(input.BotID),
		ThreadID:                     sessionID,
		StreamID:                     strings.TrimSpace(input.StreamID),
		UserID:                       strings.TrimSpace(input.ActorUserID),
		SourceChannelIdentityID:      strings.TrimSpace(input.ActorChannelIdentityID),
		ConversationType:             agentdomain.ConversationTypePrivate,
		Query:                        visibleMessageText(requestMessage),
		RawQuery:                     visibleMessageText(requestMessage),
		Token:                        strings.TrimSpace(input.ChatToken),
		ChatToken:                    strings.TrimSpace(input.ChatToken),
		CurrentChannel:               "local",
		ReplyTarget:                  strings.TrimSpace(input.BotID),
		Channels:                     []string{"local"},
		Model:                        strings.TrimSpace(input.Model),
		ReasoningEffort:              strings.TrimSpace(input.ReasoningEffort),
		WorkspaceTargetID:            strings.TrimSpace(input.WorkspaceTargetID),
		ToolHTTPURL:                  strings.TrimSpace(input.ToolHTTPURL),
		ReusePersistedUserMessage:    true,
		PersistedUserMessageID:       requestMessage.ID,
		SkipHistoryTurn:              true,
		HistoryCutoffBeforeMessageID: cutoffMessageID,
		RequiredHistoryMessageID:     requestMessage.ID,
	}
	return s.streamReplacementWS(ctx, req, turn.ID, requestMessage.ID, "retry", eventCh, abortCh)
}

func (s *Service) EditLatestMessageWS(ctx context.Context, input EditLatestMessageInput, eventCh chan<- WSStreamEvent, abortCh <-chan struct{}) error {
	if s == nil || s.messageService == nil {
		return errors.New("message service not configured")
	}
	sessionID := strings.TrimSpace(input.SessionID)
	messageID := strings.TrimSpace(input.MessageID)
	text := strings.TrimSpace(input.Text)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if messageID == "" {
		return errors.New("message id is required")
	}
	if text == "" && len(input.Attachments) == 0 {
		return errors.New("message text or attachments required")
	}

	turn, target, err := s.latestVisibleTurnAndMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(target.Role, "user") {
		return errors.New("only latest user messages can be edited")
	}
	if strings.TrimSpace(turn.RequestMessageID) != target.ID {
		return errors.New("edit target is not the request for its turn")
	}
	if err := s.ensureLatestVisibleTurn(ctx, sessionID, turn.ID); err != nil {
		return errors.New("only the latest user message can be edited")
	}

	req := ChatRequest{
		BotID:                        strings.TrimSpace(input.BotID),
		ChatID:                       strings.TrimSpace(input.BotID),
		ThreadID:                     sessionID,
		StreamID:                     strings.TrimSpace(input.StreamID),
		UserID:                       strings.TrimSpace(input.ActorUserID),
		SourceChannelIdentityID:      strings.TrimSpace(input.ActorChannelIdentityID),
		ConversationType:             agentdomain.ConversationTypePrivate,
		Query:                        text,
		RawQuery:                     text,
		Token:                        strings.TrimSpace(input.ChatToken),
		ChatToken:                    strings.TrimSpace(input.ChatToken),
		CurrentChannel:               "local",
		ReplyTarget:                  strings.TrimSpace(input.BotID),
		Channels:                     []string{"local"},
		Attachments:                  input.Attachments,
		Model:                        strings.TrimSpace(input.Model),
		ReasoningEffort:              strings.TrimSpace(input.ReasoningEffort),
		WorkspaceTargetID:            strings.TrimSpace(input.WorkspaceTargetID),
		ToolHTTPURL:                  strings.TrimSpace(input.ToolHTTPURL),
		SkipHistoryTurn:              true,
		HistoryCutoffBeforeMessageID: target.ID,
	}
	return s.streamReplacementWS(ctx, req, turn.ID, "", "edit", eventCh, abortCh)
}

func (s *Service) latestVisibleTurnAndMessage(ctx context.Context, sessionID, messageID string) (messagepkg.HistoryTurn, messagepkg.Message, error) {
	target, err := s.messageService.GetByIDBySession(ctx, sessionID, messageID)
	if err != nil {
		return messagepkg.HistoryTurn{}, messagepkg.Message{}, fmt.Errorf("load message: %w", err)
	}
	turn, err := s.messageService.GetVisibleTurnByMessage(ctx, sessionID, messageID)
	if err != nil {
		return messagepkg.HistoryTurn{}, messagepkg.Message{}, fmt.Errorf("load visible turn: %w", err)
	}
	return turn, target, nil
}

func (s *Service) ensureLatestVisibleTurn(ctx context.Context, sessionID, turnID string) error {
	latest, err := s.messageService.GetLatestVisibleTurnBySession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load latest visible turn: %w", err)
	}
	if strings.TrimSpace(latest.ID) != strings.TrimSpace(turnID) {
		return errors.New("turn is not latest")
	}
	return nil
}

func (s *Service) streamReplacementWS(
	ctx context.Context,
	req ChatRequest,
	oldTurnID string,
	requestMessageID string,
	reason string,
	eventCh chan<- WSStreamEvent,
	abortCh <-chan struct{},
) error {
	_, err := s.streamChatWSResultWithHooks(
		ctx,
		req,
		eventCh,
		abortCh,
		func(ctx context.Context) error {
			return s.ensureLatestVisibleTurn(ctx, req.ThreadID, oldTurnID)
		},
		func(ctx context.Context, persisted []messagepkg.Message) error {
			return s.replacePersistedTurn(ctx, req, oldTurnID, requestMessageID, reason, persisted)
		},
	)
	return err
}

func (s *Service) replacePersistedTurn(
	ctx context.Context,
	req ChatRequest,
	oldTurnID string,
	requestMessageID string,
	reason string,
	persisted []messagepkg.Message,
) error {
	replacementID := firstAssistantID(persisted)
	if replacementID == "" {
		replacementID = latestPersistedID(persisted)
	}
	if replacementID == "" {
		return errors.New("replacement message was not persisted")
	}
	if strings.TrimSpace(requestMessageID) == "" {
		requestMessageID = firstUserID(persisted)
	}
	forkAnchorUpdate := s.prepareForkAnchorUpdate(ctx, req.ThreadID, req.HistoryCutoffBeforeMessageID)
	if _, err := s.messageService.ReplaceTurn(context.WithoutCancel(ctx), req.ThreadID, oldTurnID, requestMessageID, replacementID, reason); err != nil {
		s.logger.ErrorContext(ctx, "replace history turn failed", slog.String("reason", reason), slog.Any("error", err))
		s.cleanupReplacementMessages(ctx, persisted)
		return fmt.Errorf("replace history turn: %w", err)
	}
	s.applyForkAnchorUpdate(context.WithoutCancel(ctx), req.ThreadID, forkAnchorUpdate)
	s.publishReplacementMessageCreated(req.BotID, persisted)
	return nil
}

func (s *Service) publishReplacementMessageCreated(botID string, persisted []messagepkg.Message) {
	if s == nil || s.eventPublisher == nil || len(persisted) == 0 {
		return
	}
	var latest messagepkg.Message
	for i := len(persisted) - 1; i >= 0; i-- {
		if strings.TrimSpace(persisted[i].ID) == "" {
			continue
		}
		latest = persisted[i]
		break
	}
	if strings.TrimSpace(latest.ID) == "" {
		return
	}
	eventBotID := strings.TrimSpace(botID)
	if eventBotID == "" {
		eventBotID = strings.TrimSpace(latest.BotID)
	}
	data, err := json.Marshal(latest)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("marshal replacement message event failed", slog.Any("error", err))
		}
		return
	}
	s.eventPublisher.Publish(messageevent.Event{
		Type:  messageevent.EventTypeMessageCreated,
		BotID: eventBotID,
		Data:  data,
	})
}

type forkAnchorUpdate struct {
	metadata map[string]any
}

func (s *Service) prepareForkAnchorUpdate(ctx context.Context, sessionID string, replacedTailStartMessageID string) *forkAnchorUpdate {
	sessionID = strings.TrimSpace(sessionID)
	replacedTailStartMessageID = strings.TrimSpace(replacedTailStartMessageID)
	if s == nil || s.sessionService == nil || s.messageService == nil || sessionID == "" || replacedTailStartMessageID == "" {
		return nil
	}

	sess, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		s.logForkAnchorUpdateWarning("load session for fork anchor update failed", sessionID, err)
		return nil
	}
	fork, ok := forkedFromMetadata(sess.Metadata)
	if !ok {
		return nil
	}
	currentAnchor := forkMetadataString(fork, "fork_message_id")
	if currentAnchor == "" {
		return nil
	}

	replacedTail, err := s.messageService.ListVisibleFromBySession(ctx, sessionID, replacedTailStartMessageID)
	if err != nil {
		s.logForkAnchorUpdateWarning("load replaced tail for fork anchor update failed", sessionID, err)
		return nil
	}
	if !messagesContainAnchor(replacedTail, currentAnchor) {
		return nil
	}

	nextAnchor := s.latestInheritedAssistantBefore(ctx, sessionID, replacedTailStartMessageID, sess.CreatedAt)
	if nextAnchor == currentAnchor {
		return nil
	}

	nextFork := cloneMetadataMap(fork)
	if nextAnchor != "" {
		nextFork["fork_message_id"] = nextAnchor
	} else {
		delete(nextFork, "fork_message_id")
	}

	nextMetadata := cloneMetadataMap(sess.Metadata)
	nextMetadata["forked_from"] = nextFork
	return &forkAnchorUpdate{metadata: nextMetadata}
}

func (s *Service) applyForkAnchorUpdate(ctx context.Context, sessionID string, update *forkAnchorUpdate) {
	sessionID = strings.TrimSpace(sessionID)
	if s == nil || s.sessionService == nil || update == nil || sessionID == "" {
		return
	}
	if _, err := s.sessionService.UpdateMetadata(ctx, sessionID, update.metadata); err != nil {
		s.logForkAnchorUpdateWarning("persist fork anchor update failed", sessionID, err)
	}
}

func (s *Service) latestInheritedAssistantBefore(ctx context.Context, sessionID string, beforeMessageID string, sessionCreatedAt time.Time) string {
	messages, err := s.messageService.ListBeforeMessageBySession(ctx, sessionID, beforeMessageID, 10000)
	if err != nil {
		s.logForkAnchorUpdateWarning("load previous messages for fork anchor update failed", sessionID, err)
		return ""
	}
	hasCutoff := !sessionCreatedAt.IsZero()
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			continue
		}
		if hasCutoff && (msg.CreatedAt.IsZero() || msg.CreatedAt.After(sessionCreatedAt)) {
			continue
		}
		return strings.TrimSpace(msg.ID)
	}
	return ""
}

func messagesContainAnchor(messages []messagepkg.Message, anchor string) bool {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return false
	}
	for _, msg := range messages {
		if strings.TrimSpace(msg.ID) == anchor || strings.TrimSpace(msg.ExternalMessageID) == anchor {
			return true
		}
	}
	return false
}

func forkedFromMetadata(metadata map[string]any) (map[string]any, bool) {
	raw, ok := metadata["forked_from"]
	if !ok || raw == nil {
		return nil, false
	}
	switch value := raw.(type) {
	case map[string]any:
		return value, true
	case map[string]string:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func forkMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func cloneMetadataMap(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func (s *Service) logForkAnchorUpdateWarning(message string, sessionID string, err error) {
	if s != nil && s.logger != nil {
		s.logger.Warn("fork anchor update skipped", slog.String("reason", message), slog.String("session_id", sessionID), slog.Any("error", err))
	}
}

func (s *Service) cleanupReplacementMessages(ctx context.Context, persisted []messagepkg.Message) {
	if s == nil || s.messageService == nil || len(persisted) == 0 {
		return
	}
	ids := make([]string, 0, len(persisted))
	for _, msg := range persisted {
		if id := strings.TrimSpace(msg.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := s.messageService.DeleteByIDs(context.WithoutCancel(ctx), ids); err != nil {
		s.logger.ErrorContext(ctx, "cleanup replacement messages failed", slog.Any("error", err), slog.Int("message_count", len(ids)))
	}
}

func visibleMessageText(msg messagepkg.Message) string {
	if text := strings.TrimSpace(msg.DisplayContent); text != "" {
		return text
	}
	var modelMessage agentdomain.ModelMessage
	if err := json.Unmarshal(msg.Content, &modelMessage); err == nil {
		if text := strings.TrimSpace(modelMessage.TextContent()); text != "" {
			return text
		}
	}
	return strings.TrimSpace(string(msg.Content))
}

func firstAssistantID(messages []messagepkg.Message) string {
	for _, msg := range messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
			return strings.TrimSpace(msg.ID)
		}
	}
	return ""
}

func firstUserID(messages []messagepkg.Message) string {
	for _, msg := range messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			return strings.TrimSpace(msg.ID)
		}
	}
	return ""
}

func latestPersistedID(messages []messagepkg.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(messages[i].ID); id != "" {
			return id
		}
	}
	return ""
}
