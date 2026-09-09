package qq

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/felinics/memoh/internal/channel"
)

func TestEventToInboundMessageC2C(t *testing.T) {
	t.Parallel()

	msg, ok := eventToInboundMessage(InboundEvent{
		Type: "C2C_MESSAGE_CREATE",
		C2CMessage: &C2CMessageEvent{
			ID:        "msg-1",
			Content:   "hello",
			Timestamp: "2026-03-06T12:00:00Z",
			Author: C2CAuthor{
				UserOpenID: "user-openid",
			},
			Attachments: []MessageAttachment{{
				ContentType: "image/png",
				URL:         "//cdn.qq.com/image.png",
				FileName:    "a.png",
				Width:       120,
				Height:      80,
				Size:        2048,
			}},
		},
	}, "bot-1")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Channel != Type {
		t.Fatalf("unexpected channel: %s", msg.Channel)
	}
	if msg.BotID != "bot-1" {
		t.Fatalf("unexpected bot id: %s", msg.BotID)
	}
	if msg.ReplyTarget != "c2c:user-openid" {
		t.Fatalf("unexpected reply target: %s", msg.ReplyTarget)
	}
	if msg.Conversation.Type != channel.ConversationTypePrivate {
		t.Fatalf("unexpected conversation type: %s", msg.Conversation.Type)
	}
	if msg.Sender.SubjectID != "user-openid" {
		t.Fatalf("unexpected sender subject: %s", msg.Sender.SubjectID)
	}
	if len(msg.Message.Attachments) != 1 {
		t.Fatalf("unexpected attachments: %d", len(msg.Message.Attachments))
	}
	att := msg.Message.Attachments[0]
	if att.Type != channel.AttachmentImage {
		t.Fatalf("unexpected attachment type: %s", att.Type)
	}
	if att.URL != "https://cdn.qq.com/image.png" {
		t.Fatalf("unexpected attachment url: %s", att.URL)
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); mentioned {
		t.Fatal("direct message should not be marked mentioned")
	}
}

func TestEventToInboundMessageGroupAt(t *testing.T) {
	t.Parallel()

	msg, ok := eventToInboundMessage(InboundEvent{
		Type: "GROUP_AT_MESSAGE_CREATE",
		GroupMessage: &GroupMessageEvent{
			ID:          "msg-2",
			Content:     "@bot hi",
			Timestamp:   "2026-03-06T12:00:00Z",
			GroupOpenID: "group-openid",
			Author: GroupAuthor{
				MemberOpenID: "member-openid",
			},
		},
	}, "bot-2")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.ReplyTarget != "group:group-openid" {
		t.Fatalf("unexpected reply target: %s", msg.ReplyTarget)
	}
	if msg.Conversation.ID != "group-openid" {
		t.Fatalf("unexpected conversation id: %s", msg.Conversation.ID)
	}
	if msg.Conversation.Type != channel.ConversationTypeGroup {
		t.Fatalf("unexpected conversation type: %s", msg.Conversation.Type)
	}
	if msg.Sender.SubjectID != "member-openid" {
		t.Fatalf("unexpected sender subject: %s", msg.Sender.SubjectID)
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); !mentioned {
		t.Fatal("group at message should be marked mentioned")
	}
}

func TestEventToInboundMessageReplyReference(t *testing.T) {
	t.Parallel()

	msg, ok := eventToInboundMessage(InboundEvent{
		Type: "GROUP_AT_MESSAGE_CREATE",
		GroupMessage: &GroupMessageEvent{
			ID:          "msg-2",
			Content:     "@bot hi",
			GroupOpenID: "group-openid",
			Author:      GroupAuthor{MemberOpenID: "member-openid"},
			MessageReference: &MessageReference{
				MessageID: "source-msg",
			},
		},
	}, "bot-2")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Message.Reply == nil {
		t.Fatal("expected reply ref")
	}
	if msg.Message.Reply.MessageID != "source-msg" || msg.Message.Reply.Target != "group:group-openid" {
		t.Fatalf("unexpected reply ref: %#v", msg.Message.Reply)
	}
}

func TestEventToInboundMessageChannelAt(t *testing.T) {
	t.Parallel()

	msg, ok := eventToInboundMessage(InboundEvent{
		Type: "AT_MESSAGE_CREATE",
		GuildMessage: &GuildMessageEvent{
			ID:        "msg-3",
			Content:   "<@bot> hi",
			Timestamp: "2026-03-06T12:00:00Z",
			ChannelID: "channel-1",
			GuildID:   "guild-1",
			Author: GuildAuthor{
				ID:       "author-1",
				Username: "alice",
			},
		},
	}, "bot-3")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.ReplyTarget != "channel:channel-1" {
		t.Fatalf("unexpected reply target: %s", msg.ReplyTarget)
	}
	if msg.Conversation.Type != channel.ConversationTypeThread {
		t.Fatalf("unexpected conversation type: %s", msg.Conversation.Type)
	}
	if msg.Conversation.ID != "guild-1" {
		t.Fatalf("unexpected conversation id: %s", msg.Conversation.ID)
	}
	if msg.Conversation.ThreadID != "channel-1" {
		t.Fatalf("unexpected thread id: %s", msg.Conversation.ThreadID)
	}
	if msg.Sender.DisplayName != "alice" {
		t.Fatalf("unexpected sender display name: %s", msg.Sender.DisplayName)
	}
	if msg.Sender.Attribute("channel_id") != "channel-1" {
		t.Fatalf("unexpected channel_id attribute: %s", msg.Sender.Attribute("channel_id"))
	}
	if msg.Metadata["guild_id"] != "guild-1" {
		t.Fatalf("unexpected guild_id metadata: %#v", msg.Metadata["guild_id"])
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); !mentioned {
		t.Fatal("channel at message should be marked mentioned")
	}
}

func TestEventToInboundMessageIgnoresUnsupportedType(t *testing.T) {
	t.Parallel()

	if _, ok := eventToInboundMessage(InboundEvent{Type: "READY"}, "bot-1"); ok {
		t.Fatal("unexpected inbound message for READY")
	}
}

func TestEventToInboundMessagePreservesGIFType(t *testing.T) {
	t.Parallel()

	msg, ok := eventToInboundMessage(InboundEvent{
		Type: "C2C_MESSAGE_CREATE",
		C2CMessage: &C2CMessageEvent{
			ID:        "msg-gif",
			Content:   "gif",
			Timestamp: "2026-03-06T12:00:00Z",
			Author: C2CAuthor{
				UserOpenID: "user-openid",
			},
			Attachments: []MessageAttachment{{
				ContentType: "image/gif",
				URL:         "https://cdn.qq.com/animated.gif",
				FileName:    "animated.gif",
			}},
		},
	}, "bot-gif")
	if !ok {
		t.Fatal("expected inbound message")
	}
	if len(msg.Message.Attachments) != 1 {
		t.Fatalf("unexpected attachments: %d", len(msg.Message.Attachments))
	}
	if msg.Message.Attachments[0].Type != channel.AttachmentGIF {
		t.Fatalf("unexpected attachment type: %s", msg.Message.Attachments[0].Type)
	}
}

func TestAdjustSessionAfterInvalidKeepsIntentLevel(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	session := sessionState{
		SessionID:   "session-1",
		LastSeq:     42,
		IntentLevel: 0,
	}

	adapter.adjustSessionAfterInvalid("cfg-1", &session)

	if session.SessionID != "" {
		t.Fatalf("unexpected session id: %q", session.SessionID)
	}
	if session.LastSeq != 0 {
		t.Fatalf("unexpected seq: %d", session.LastSeq)
	}
	if session.IntentLevel != 0 {
		t.Fatalf("unexpected intent level: %d", session.IntentLevel)
	}

	saved := adapter.loadSession("cfg-1")
	if saved.IntentLevel != 0 {
		t.Fatalf("unexpected saved intent level: %d", saved.IntentLevel)
	}
}

func TestStartHeartbeatCancelStopsSessionLoop(t *testing.T) {
	t.Parallel()

	heartbeat := startHeartbeat(context.Background(), &gatewayWriter{}, time.Hour, func() int { return 0 })
	heartbeat.cancel()

	select {
	case <-heartbeat.done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after session cancel")
	}
}

func TestHandleDispatchMarksHealthySessionForReadyAndResumed(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	cfg := channel.ChannelConfig{ID: "cfg-healthy", BotID: "bot-healthy"}

	session := sessionState{}
	healthy, err := adapter.handleDispatch(context.Background(), cfg, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return nil
	}, "READY", []byte(`{"session_id":"session-1"}`), &session)
	if err != nil {
		t.Fatalf("handle ready: %v", err)
	}
	if !healthy {
		t.Fatal("expected READY to mark session healthy")
	}

	healthy, err = adapter.handleDispatch(context.Background(), cfg, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return nil
	}, "RESUMED", []byte(`{}`), &session)
	if err != nil {
		t.Fatalf("handle resumed: %v", err)
	}
	if !healthy {
		t.Fatal("expected RESUMED to mark session healthy")
	}
}

func TestNextReconnectDelayResetsAfterHealthySession(t *testing.T) {
	t.Parallel()

	backoffs := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second}
	delay, attempt := nextReconnectDelay(backoffs, 2, true)

	if delay != time.Second {
		t.Fatalf("unexpected delay: %v", delay)
	}
	if attempt != 1 {
		t.Fatalf("unexpected next attempt: %d", attempt)
	}
}

func TestHandleGatewayClose_IntentCodesRequireReconnect(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	session := sessionState{
		SessionID:   "session-1",
		LastSeq:     42,
		IntentLevel: 0,
	}

	healthy, err := adapter.handleGatewayClose(
		"cfg-intent",
		&qqClient{},
		&session,
		&websocket.CloseError{Code: 4914},
		true,
	)
	if !healthy {
		t.Fatal("expected healthy flag to be preserved")
	}
	if err == nil {
		t.Fatal("expected reconnect error")
	}
	if !strings.Contains(err.Error(), "intent code 4914") {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.SessionID != "" || session.LastSeq != 0 {
		t.Fatalf("session should be reset, got id=%q seq=%d", session.SessionID, session.LastSeq)
	}
	if session.IntentLevel != 1 {
		t.Fatalf("expected intent fallback level 1, got %d", session.IntentLevel)
	}

	saved := adapter.loadSession("cfg-intent")
	if saved.SessionID != "" || saved.LastSeq != 0 {
		t.Fatalf("saved session should be reset, got id=%q seq=%d", saved.SessionID, saved.LastSeq)
	}
	if saved.IntentLevel != session.IntentLevel {
		t.Fatalf("unexpected intent level: %d", saved.IntentLevel)
	}
}

func TestAdjustSessionAfterIntentCloseCapsIntentLevel(t *testing.T) {
	t.Parallel()

	adapter := NewQQAdapter(nil)
	session := sessionState{
		SessionID:   "session-2",
		LastSeq:     99,
		IntentLevel: len(qqIntentLevels) - 1,
	}

	adapter.adjustSessionAfterIntentClose("cfg-intent-cap", &session)

	if session.SessionID != "" || session.LastSeq != 0 {
		t.Fatalf("session should be reset, got id=%q seq=%d", session.SessionID, session.LastSeq)
	}
	if session.IntentLevel != len(qqIntentLevels)-1 {
		t.Fatalf("expected capped intent level %d, got %d", len(qqIntentLevels)-1, session.IntentLevel)
	}

	saved := adapter.loadSession("cfg-intent-cap")
	if saved.IntentLevel != len(qqIntentLevels)-1 {
		t.Fatalf("unexpected saved intent level: %d", saved.IntentLevel)
	}
}
