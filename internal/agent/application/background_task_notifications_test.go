package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/background"
	agentview "github.com/felinics/memoh/internal/agent/view"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type backgroundNotificationWriter struct {
	inputs []messagepkg.PersistInput
	err    error
}

func (w *backgroundNotificationWriter) Persist(_ context.Context, input messagepkg.PersistInput) (messagepkg.Message, error) {
	if w.err != nil {
		return messagepkg.Message{}, w.err
	}
	w.inputs = append(w.inputs, input)
	return messagepkg.Message{BotID: input.BotID, SessionID: input.SessionID, Content: input.Content}, nil
}

type backgroundNotificationSessionLookup struct {
	row session.Thread
	err error
}

func (s backgroundNotificationSessionLookup) Get(context.Context, string) (session.Thread, error) {
	return s.row, s.err
}

func TestBackgroundTaskNotificationsPersistLifecycleBeforeChannelDelivery(t *testing.T) {
	writer := &backgroundNotificationWriter{}
	sess := session.Thread{ID: "session-1", BotID: "bot-1", Type: session.TypeChat, RouteID: "route-1"}
	var delivered []string
	notifications := NewBackgroundTaskNotifications(writer, backgroundNotificationSessionLookup{row: sess},
		func(_ context.Context, target session.Thread, text string) error {
			if target.ID != sess.ID || target.RouteID != sess.RouteID || target.BotID != sess.BotID {
				t.Fatalf("delivery targeted another conversation: %#v", target)
			}
			if len(writer.inputs) != len(delivered)+1 {
				t.Fatal("delivery ran before the notification was persisted")
			}
			delivered = append(delivered, text)
			return nil
		})
	for _, event := range []background.TaskEventType{
		background.TaskEventStarted, background.TaskEventCompleted, background.TaskEventFailed, background.TaskEventKilled, background.TaskEventUnknown,
	} {
		if err := notifications.Handle(context.Background(), background.TaskEvent{
			Kind: background.KindDependency, TaskID: "task-1", BotID: sess.BotID, SessionID: sess.ID,
			Event: event, Command: "Install Codex", Tail: "secret-script-token", Chunk: "secret-stdout",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(delivered) != 5 {
		t.Fatalf("lifecycle messages = %d, want every transition", len(delivered))
	}
	if !strings.Contains(delivered[4], "unconfirmed outcome") || strings.Contains(delivered[4], " failed") || strings.Contains(delivered[4], "was cancelled") {
		t.Fatalf("unknown execution claimed an outcome: %q", delivered[4])
	}
	for i, input := range writer.inputs {
		if input.BotID != sess.BotID || input.SessionID != sess.ID || input.SkipHistoryTurn {
			t.Fatalf("notification escaped durable conversation history: %#v", input)
		}
		if input.Metadata["background_task_id"] != "task-1" || input.Metadata["background_task_event"] == nil {
			t.Fatalf("notification lost task provenance: %#v", input.Metadata)
		}
		if strings.Contains(string(input.Content), "secret-") || strings.Contains(delivered[i], "secret-") {
			t.Fatal("script output leaked into chat notification")
		}
		// Exercise the real history projection, so a successful persistence
		// cannot mask a notification that disappears when the chat reloads.
		turns := agentview.ConvertMessagesToUITurns([]messagepkg.Message{{
			ID: "message-1", BotID: sess.BotID, SessionID: sess.ID,
			TurnID: "history-turn-1",
			Role:   input.Role, Content: input.Content, DisplayContent: input.DisplayText,
		}})
		if len(turns) != 1 || len(turns[0].Messages) != 1 || turns[0].Messages[0].Content != delivered[i] {
			t.Fatalf("notification did not survive chat history projection: %#v", turns)
		}
	}
}

func TestBackgroundTaskNotificationsDoNotBroadcastUnscopedOrOutputEvents(t *testing.T) {
	var notifications *BackgroundTaskNotifications
	for _, evt := range []background.TaskEvent{
		{Kind: background.KindDependency, Event: background.TaskEventStarted, BotID: "bot-1"},
		{Kind: background.KindDependency, Event: background.TaskEventOutput, BotID: "bot-1", SessionID: "session-1"},
		{Kind: background.KindExec, Event: background.TaskEventCompleted, BotID: "bot-1", SessionID: "session-1"},
	} {
		if err := notifications.Handle(context.Background(), evt); err != nil {
			t.Fatalf("an event without a dependency conversation lifecycle attempted delivery: %v", err)
		}
	}
}

func TestBackgroundTaskNotificationsRejectOtherBotAndInternalSessions(t *testing.T) {
	for _, row := range []session.Thread{
		{ID: "session-1", BotID: "other-bot", Type: session.TypeChat},
		{ID: "session-1", BotID: "bot-1", Type: session.TypeSubagent},
		{ID: "another-session", BotID: "bot-1", Type: session.TypeChat},
	} {
		t.Run(row.BotID+"/"+row.Type+"/"+row.ID, func(t *testing.T) {
			writer := &backgroundNotificationWriter{}
			notifications := NewBackgroundTaskNotifications(writer, backgroundNotificationSessionLookup{row: row},
				func(context.Context, session.Thread, string) error {
					t.Fatal("out-of-scope notification delivered")
					return nil
				})
			if err := notifications.Handle(context.Background(), background.TaskEvent{
				Kind: background.KindDependency, Event: background.TaskEventFailed,
				TaskID: "task-1", BotID: "bot-1", SessionID: "session-1",
			}); err == nil {
				t.Fatal("out-of-scope session accepted")
			}
			if len(writer.inputs) != 0 {
				t.Fatal("out-of-scope notification persisted")
			}
		})
	}
}

func TestBackgroundTaskNotificationsKeepDurableResultWhenChannelFails(t *testing.T) {
	writer := &backgroundNotificationWriter{}
	sess := session.Thread{ID: "session-1", BotID: "bot-1", Type: session.TypeChat}
	deliveryErr := errors.New("channel unavailable")
	notifications := NewBackgroundTaskNotifications(writer, backgroundNotificationSessionLookup{row: sess},
		func(context.Context, session.Thread, string) error { return deliveryErr })
	evt := background.TaskEvent{
		Kind: background.KindDependency, Event: background.TaskEventCompleted,
		TaskID: "task-1", BotID: sess.BotID, SessionID: sess.ID,
	}
	if err := notifications.Handle(context.Background(), evt); !errors.Is(err, deliveryErr) {
		t.Fatalf("delivery failure was lost: %v", err)
	}
	if len(writer.inputs) != 1 {
		t.Fatal("channel failure discarded the durable notification")
	}

	writer.err = errors.New("database unavailable")
	notifications.deliver = func(context.Context, session.Thread, string) error {
		t.Fatal("notification delivered without durable history")
		return nil
	}
	if err := notifications.Handle(context.Background(), evt); !errors.Is(err, writer.err) {
		t.Fatalf("persistence failure was lost: %v", err)
	}
}
