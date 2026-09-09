package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felinics/memoh/internal/agent/background"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type backgroundNotificationSessions interface {
	Get(context.Context, string) (session.Thread, error)
}

// BackgroundTaskNotifications persists dependency lifecycle feedback in the
// explicitly selected conversation and delivers it to that conversation's
// channel. Script output stays in the Manage-only operation log.
type BackgroundTaskNotifications struct {
	messages messagepkg.Writer
	sessions backgroundNotificationSessions
	deliver  func(context.Context, session.Thread, string) error
}

func NewBackgroundTaskNotifications(
	messages messagepkg.Writer,
	sessions backgroundNotificationSessions,
	deliver func(context.Context, session.Thread, string) error,
) *BackgroundTaskNotifications {
	return &BackgroundTaskNotifications{messages: messages, sessions: sessions, deliver: deliver}
}

// Handle ignores unscoped tasks: installing a bot dependency must never notify
// every historical conversation belonging to the bot. Callers authorize the
// optional session at operation admission; delivery verifies its bot again.
func (n *BackgroundTaskNotifications) Handle(ctx context.Context, evt background.TaskEvent) error {
	if evt.Kind != background.KindDependency || strings.TrimSpace(evt.SessionID) == "" {
		return nil
	}
	text := dependencyTaskNotificationText(evt)
	if text == "" {
		return nil
	}
	if n == nil || n.messages == nil || n.sessions == nil {
		return errors.New("background task notifications are not configured")
	}
	if strings.TrimSpace(evt.BotID) == "" || strings.TrimSpace(evt.TaskID) == "" {
		return errors.New("background task notification is missing its identity")
	}
	sess, err := n.sessions.Get(ctx, strings.TrimSpace(evt.SessionID))
	if err != nil {
		return fmt.Errorf("load background task notification session: %w", err)
	}
	if sess.BotID != evt.BotID || sess.ID != strings.TrimSpace(evt.SessionID) || !session.IsUserFacingType(sess.Type) {
		return errors.New("background task notification session is outside its bot conversation scope")
	}
	content, err := historyfrag.MarshalStoredModelMessage(ModelMessage{
		Role: "assistant", Content: newTextContent(text),
	})
	if err != nil {
		return fmt.Errorf("encode background task notification: %w", err)
	}
	_, err = n.messages.Persist(ctx, messagepkg.PersistInput{
		BotID:       sess.BotID,
		SessionID:   sess.ID,
		Role:        "assistant",
		Content:     content,
		DisplayText: text,
		SessionMode: sess.SessionMode,
		RuntimeType: sess.RuntimeType,
		Metadata: map[string]any{
			"background_task_id":    evt.TaskID,
			"background_task_event": string(evt.Event),
		},
	})
	if err != nil {
		return fmt.Errorf("persist background task notification: %w", err)
	}
	if n.deliver != nil {
		if err := n.deliver(ctx, sess, text); err != nil {
			return fmt.Errorf("deliver background task notification: %w", err)
		}
	}
	return nil
}

func dependencyTaskNotificationText(evt background.TaskEvent) string {
	label := strings.TrimSpace(evt.Command)
	if label == "" {
		label = "Workspace dependency operation"
	}
	switch evt.Event {
	case background.TaskEventStarted:
		return label + " is running."
	case background.TaskEventCompleted:
		return label + " completed. You can retry your message."
	case background.TaskEventFailed:
		return label + " failed. A bot manager can review the details in Workspace Dependencies."
	case background.TaskEventUnknown:
		return label + " has an unconfirmed outcome. A bot manager should refresh Workspace Dependencies before retrying; the operation may still be running."
	case background.TaskEventKilled:
		return label + " was cancelled. A bot manager can review it in Workspace Dependencies."
	default:
		return ""
	}
}
