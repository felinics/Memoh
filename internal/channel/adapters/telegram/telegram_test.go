package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/command"
)

// newStubTelegramBot builds a telebot bot suitable for tests that need a
// concrete *tele.Bot (e.g. for Me access) without making network calls. The
// URL points at a closed local port so any accidental API call fails fast
// instead of hanging.
func newStubTelegramBot(t *testing.T) *tele.Bot {
	t.Helper()
	bot, err := tele.NewBot(tele.Settings{
		Token:   "test",
		URL:     "http://127.0.0.1:1",
		Offline: true,
	})
	if err != nil {
		t.Fatalf("create stub telegram bot: %v", err)
	}
	return bot
}

func TestResolveTelegramSender(t *testing.T) {
	t.Parallel()

	externalID, displayName, attrs := resolveTelegramSender(nil)
	if externalID != "" || displayName != "" || len(attrs) != 0 {
		t.Fatalf("expected empty sender")
	}
	msg := &tele.Message{
		Sender: &tele.User{ID: 123, Username: "alice"},
	}
	externalID, displayName, attrs = resolveTelegramSender(msg)
	if externalID != "123" || displayName != "@alice" {
		t.Fatalf("unexpected sender: %s %s", externalID, displayName)
	}
	if attrs["user_id"] != "123" || attrs["username"] != "alice" {
		t.Fatalf("unexpected attrs: %#v", attrs)
	}
}

func TestIsTelegramBotMentioned(t *testing.T) {
	t.Parallel()

	t.Run("text mention", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{
			Text: "hello @MemohBot",
		}
		if !isTelegramBotMentioned(msg, "memohbot") {
			t.Fatalf("expected bot mention from text")
		}
	})

	t.Run("entity text mention matching bot", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{
			Entities: []tele.MessageEntity{
				{
					Type: tele.EntityTMention,
					User: &tele.User{IsBot: true, Username: "memohbot"},
				},
			},
		}
		if !isTelegramBotMentioned(msg, "memohbot") {
			t.Fatalf("expected bot mention from text_mention entity")
		}
	})

	t.Run("entity text mention other bot", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{
			Entities: []tele.MessageEntity{
				{
					Type: tele.EntityTMention,
					User: &tele.User{IsBot: true, Username: "otherbot"},
				},
			},
		}
		if isTelegramBotMentioned(msg, "memohbot") {
			t.Fatalf("expected no mention for different bot")
		}
	})

	t.Run("not mentioned", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{
			Text: "hello everyone",
		}
		if isTelegramBotMentioned(msg, "memohbot") {
			t.Fatalf("expected no mention")
		}
	})

	t.Run("username prefix collision", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{Text: "/new discuss @memohbot_extra"}
		if isTelegramBotMentioned(msg, "memohbot") {
			t.Fatal("longer username must not count as the current bot mention")
		}
	})

	t.Run("email local part", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{Text: "send this to alice@memohbot"}
		if isTelegramBotMentioned(msg, "memohbot") {
			t.Fatal("email address must not count as a bot mention")
		}
	})

	t.Run("punctuated exact mention", func(t *testing.T) {
		t.Parallel()
		msg := &tele.Message{Text: "(@MemohBot), please respond"}
		if !isTelegramBotMentioned(msg, "memohbot") {
			t.Fatal("exact mention with punctuation should count")
		}
	})
}

func TestTelegramDescriptorIncludesStreaming(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	caps := adapter.Descriptor().Capabilities
	if !caps.Streaming {
		t.Fatal("expected streaming capability")
	}
	if !caps.Media {
		t.Fatal("expected media capability")
	}
}

func TestParseTelegramUserInputCallback(t *testing.T) {
	t.Parallel()

	id, answer, ok := parseTelegramUserInputCallback("respond:input-1")
	if !ok || id != "input-1" || answer != "" {
		t.Fatalf("parseTelegramUserInputCallback() = %q, %q, %v", id, answer, ok)
	}
	id, answer, ok = parseTelegramUserInputCallback("respond:input-1:q1.o2")
	if !ok || id != "input-1" || answer != "q1.o2" {
		t.Fatalf("selected option callback = %q, %q, %v", id, answer, ok)
	}
	if _, _, ok := parseTelegramUserInputCallback("approve:input-1"); ok {
		t.Fatal("approval callback should not parse as user input")
	}
	if _, _, ok := parseTelegramUserInputCallback("respond: "); ok {
		t.Fatal("empty user input id should not parse")
	}
	if _, _, ok := parseTelegramUserInputCallback("respond:input-1: "); ok {
		t.Fatal("empty selected option should not parse")
	}
}

func TestHandleTelegramUserInputCallbackBareRespondDoesNotDispatch(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	dispatched := false
	update := &tele.Update{
		ID: 99,
		Callback: &tele.Callback{
			ID:     "callback-1",
			Data:   "respond:input-1",
			Sender: &tele.User{ID: 123, Username: "alice"},
			Message: &tele.Message{
				ID:   456,
				Text: "Question",
				Chat: &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
			},
		},
	}

	adapter.handleTelegramCallback(context.Background(), channel.ChannelConfig{}, func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		dispatched = true
		return nil
	}, bot, update)
	if dispatched {
		// Bare respond:id used to dump a /respond command hint. Native text
		// capture is force-reply via the wizard; bare callbacks stay silent.
		t.Fatal("respond callback without answer text should not dispatch an inbound /respond command")
	}
}

func TestBuildTelegramRespondCallbackInboundMessageMarksDirected(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	update := &tele.Update{
		ID: 100,
		Callback: &tele.Callback{
			ID:     "callback-2",
			Data:   "respond:input-1:q1.o2",
			Sender: &tele.User{ID: 123, Username: "alice"},
			Message: &tele.Message{
				ID:   456,
				Text: "Question",
				Chat: &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
			},
		},
	}

	msg, ok := adapter.buildTelegramCallbackInboundMessage(channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected callback inbound message")
	}
	if got := msg.Message.PlainText(); got != "/respond q1.o2" {
		t.Fatalf("callback text = %q", got)
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); !mentioned {
		t.Fatalf("metadata = %#v, want directed callback", msg.Metadata)
	}
	if got, _ := msg.Metadata["user_input_id"].(string); got != "input-1" {
		t.Fatalf("user_input_id metadata = %q", got)
	}

	// Bare respond:<id> has no answer to submit: build must reject it (the
	// callback handler acks it as a no-op before ever dispatching).
	update.Callback.Data = "respond:input-1"
	if _, ok := adapter.buildTelegramCallbackInboundMessage(channel.ChannelConfig{}, update); ok {
		t.Fatal("bare respond callback must not build an inbound message")
	}
}

func TestBuildTelegramSelectedOptionCallbackSubmitsAnswer(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	update := &tele.Update{
		ID: 101,
		Callback: &tele.Callback{
			ID:     "callback-3",
			Data:   "respond:input-1:q1.o2",
			Sender: &tele.User{ID: 123, Username: "alice"},
			Message: &tele.Message{
				ID:   456,
				Text: "Question",
				Chat: &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
			},
		},
	}

	msg, ok := adapter.buildTelegramCallbackInboundMessage(channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected callback inbound message")
	}
	if got := msg.Message.PlainText(); got != "/respond q1.o2" {
		t.Fatalf("callback text = %q", got)
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); !mentioned {
		t.Fatalf("metadata = %#v, want directed callback", msg.Metadata)
	}
}

func TestBuildTelegramAttachmentIncludesPlatformReference(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	att := adapter.buildTelegramAttachment(nil, channel.AttachmentFile, "file_1", "doc.txt", "text/plain", 10)
	if att.PlatformKey != "file_1" {
		t.Fatalf("unexpected platform key: %s", att.PlatformKey)
	}
	if att.SourcePlatform != Type.String() {
		t.Fatalf("unexpected source platform: %s", att.SourcePlatform)
	}
}

func TestBuildTelegramAttachmentInfersTypeFromMime(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	att := adapter.buildTelegramAttachment(nil, channel.AttachmentFile, "file_2", "photo.jpg", "IMAGE/JPEG; charset=utf-8", 10)
	if att.Type != channel.AttachmentImage {
		t.Fatalf("expected image type, got: %s", att.Type)
	}
	if att.Mime != "image/jpeg" {
		t.Fatalf("expected normalized mime image/jpeg, got: %s", att.Mime)
	}
}

func TestTelegramResolveAttachmentRequiresReference(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	_, err := adapter.ResolveAttachment(context.Background(), channel.ChannelConfig{}, channel.Attachment{})
	if err == nil {
		t.Fatal("expected error when attachment has no platform_key/url")
	}
	if !strings.Contains(err.Error(), "platform_key") {
		t.Fatalf("expected platform_key error, got: %v", err)
	}
}

func TestParseReplyToMessageID(t *testing.T) {
	t.Parallel()

	if got := parseReplyToMessageID(nil); got != 0 {
		t.Fatalf("nil reply should return 0: %d", got)
	}
	if got := parseReplyToMessageID(&channel.ReplyRef{}); got != 0 {
		t.Fatalf("empty MessageID should return 0: %d", got)
	}
	if got := parseReplyToMessageID(&channel.ReplyRef{MessageID: "  123  "}); got != 123 {
		t.Fatalf("expected 123: %d", got)
	}
	if got := parseReplyToMessageID(&channel.ReplyRef{MessageID: "abc"}); got != 0 {
		t.Fatalf("invalid number should return 0: %d", got)
	}
}

func TestBuildTelegramReplyRef(t *testing.T) {
	t.Parallel()

	if buildTelegramReplyRef(nil, "123") != nil {
		t.Fatal("nil msg should return nil")
	}
	msg := &tele.Message{}
	if buildTelegramReplyRef(msg, "123") != nil {
		t.Fatal("msg without ReplyToMessage should return nil")
	}
	msg.ReplyTo = &tele.Message{ID: 42}
	ref := buildTelegramReplyRef(msg, "  -100  ")
	if ref == nil {
		t.Fatal("expected non-nil ref")
		return
	}
	if ref.MessageID != "42" || ref.Target != "-100" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
}

func TestBuildTelegramForwardRefFromChannel(t *testing.T) {
	t.Parallel()

	ref := buildTelegramForwardRef(&tele.Message{
		OriginalChat:      &tele.Chat{ID: -10001, Title: "Source Channel", Username: "source_channel"},
		OriginalMessageID: 99,
		OriginalUnixtime:  1710000000,
	})
	if ref == nil {
		t.Fatal("expected forward ref")
		return
	}
	if ref.MessageID != "99" || ref.FromConversationID != "-10001" {
		t.Fatalf("unexpected forward ids: %+v", ref)
	}
	if ref.Sender != "Source Channel (@source_channel)" || ref.Date != 1710000000 {
		t.Fatalf("unexpected forward metadata: %+v", ref)
	}
	if !ref.AttachmentsKnown {
		t.Fatal("expected telegram forward attachment state to be known")
	}
}

func TestTelegramInboundKeepsForwardOutOfText(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	inbound, ok := adapter.toInboundTelegramMessage(nil, channel.ChannelConfig{}, &tele.Message{
		ID:                10,
		Unixtime:          1710000000,
		Chat:              &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
		Sender:            &tele.User{ID: 42, Username: "sender"},
		Text:              "forwarded body",
		OriginalChat:      &tele.Chat{ID: -10002, Title: "Source Channel"},
		OriginalMessageID: 11,
	}, "forwarded body", nil, nil)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if inbound.Message.Text != "forwarded body" {
		t.Fatalf("expected original text without forward prefix, got %q", inbound.Message.Text)
	}
	if inbound.Message.Forward == nil || inbound.Message.Forward.MessageID != "11" {
		t.Fatalf("expected structured forward ref, got %+v", inbound.Message.Forward)
	}
}

func TestBuildTelegramMediaGroupInboundMessageAggregatesAttachments(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 1001, Username: "memohbot"}
	cfg := channel.ChannelConfig{}
	first := &tele.Message{
		ID:       101,
		AlbumID:  "group-1",
		Unixtime: 1710000000,
		Chat:     &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "G1"},
		Sender:   &tele.User{ID: 10, Username: "alice"},
		Photo:    &tele.Photo{File: tele.File{FileID: "photo-1", FileSize: 10}, Width: 320, Height: 240},
	}
	second := &tele.Message{
		ID:       102,
		AlbumID:  "group-1",
		Unixtime: 1710000001,
		Chat:     &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "G1"},
		Sender:   &tele.User{ID: 10, Username: "alice"},
		Caption:  "album caption",
		Photo:    &tele.Photo{File: tele.File{FileID: "photo-2", FileSize: 20}, Width: 640, Height: 480},
	}

	inbound, ok := adapter.buildTelegramMediaGroupInboundMessage(bot, cfg, []*tele.Message{first, second})
	if !ok {
		t.Fatal("expected grouped inbound message")
	}
	if inbound.Message.Text != "album caption" {
		t.Fatalf("unexpected grouped text: %q", inbound.Message.Text)
	}
	if len(inbound.Message.Attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(inbound.Message.Attachments))
	}
	if inbound.Message.Attachments[0].PlatformKey != "photo-1" || inbound.Message.Attachments[1].PlatformKey != "photo-2" {
		t.Fatalf("unexpected attachment order: %#v", inbound.Message.Attachments)
	}
	if inbound.Message.ID != "102" {
		t.Fatalf("expected anchor message id 102, got %s", inbound.Message.ID)
	}
	if inbound.ReplyTarget != "-10001" {
		t.Fatalf("unexpected reply target: %q", inbound.ReplyTarget)
	}
	if got := inbound.Metadata["media_group_id"]; got != "group-1" {
		t.Fatalf("unexpected media_group_id metadata: %#v", got)
	}
	if got := inbound.Metadata["media_group_size"]; got != 2 {
		t.Fatalf("unexpected media_group_size metadata: %#v", got)
	}
}

func TestBuildTelegramInboundMessageIncludesUpdateIDMetadata(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 1001, Username: "memohbot"}
	update := &tele.Update{
		ID: 777,
		Message: &tele.Message{
			ID:       101,
			Unixtime: 1710000000,
			Text:     "hello",
			Chat:     &tele.Chat{ID: 123, Type: tele.ChatPrivate},
			Sender:   &tele.User{ID: 10, Username: "alice"},
		},
	}

	inbound, ok := adapter.buildTelegramInboundMessage(bot, channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if got := inbound.Metadata["update_id"]; got != 777 {
		t.Fatalf("unexpected update_id metadata: %#v", got)
	}
}

func TestBuildTelegramInboundMessage_PlainFormatWhenNoEntities(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 1, Username: "memohbot"}
	update := &tele.Update{
		ID: 1,
		Message: &tele.Message{
			ID:     1,
			Text:   "plain text only",
			Chat:   &tele.Chat{ID: 1, Type: tele.ChatPrivate},
			Sender: &tele.User{ID: 1, Username: "alice"},
		},
	}
	inbound, ok := adapter.buildTelegramInboundMessage(bot, channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if inbound.Message.Format != channel.MessageFormatPlain {
		t.Fatalf("expected plain format when no entities, got %q", inbound.Message.Format)
	}
	if len(inbound.Message.Parts) != 0 {
		t.Fatalf("expected no Parts, got %+v", inbound.Message.Parts)
	}
}

func TestBuildTelegramInboundMessage_RichFormatWhenEntitiesPopulateParts(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 1, Username: "memohbot"}
	update := &tele.Update{
		ID: 1,
		Message: &tele.Message{
			ID:     1,
			Text:   "hi shout bye",
			Chat:   &tele.Chat{ID: 1, Type: tele.ChatPrivate},
			Sender: &tele.User{ID: 1, Username: "alice"},
			Entities: tele.Entities{
				{Type: tele.EntityBold, Offset: 3, Length: 5},
			},
		},
	}
	inbound, ok := adapter.buildTelegramInboundMessage(bot, channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if len(inbound.Message.Parts) == 0 {
		t.Fatalf("expected Parts populated from bold entity, got empty")
	}
	if inbound.Message.Format != channel.MessageFormatRich {
		t.Fatalf("expected rich format when Parts populate, got %q", inbound.Message.Format)
	}
	if inbound.Message.Text != "" {
		t.Fatalf("rich inbound message should not keep duplicate Text, got %q", inbound.Message.Text)
	}
}

func TestBuildTelegramInboundMessage_RichMentionDoesNotDuplicatePlainText(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 1, Username: "memoh1bot"}
	const body = "@memoh1bot telegram 新的bot api除了latex还支持什么富文本"
	update := &tele.Update{
		ID: 1,
		Message: &tele.Message{
			ID:     71890,
			Text:   body,
			Chat:   &tele.Chat{ID: -1001, Type: tele.ChatGroup, Title: "Project Memoh"},
			Sender: &tele.User{ID: 7583740083, Username: "softboil"},
			Entities: tele.Entities{
				{Type: tele.EntityMention, Offset: 0, Length: len("@memoh1bot")},
			},
		},
	}

	inbound, ok := adapter.buildTelegramInboundMessage(bot, channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected inbound message")
	}
	if inbound.Message.Text != "" {
		t.Fatalf("rich inbound message should not keep duplicate Text, got %q", inbound.Message.Text)
	}
	plain := inbound.Message.PlainText()
	if strings.Count(plain, "telegram 新的bot api除了latex还支持什么富文本") != 1 {
		t.Fatalf("PlainText should contain the question once, got %q", plain)
	}
	if got, _ := inbound.Metadata["raw_text"].(string); got != body {
		t.Fatalf("raw_text = %q, want %q", got, body)
	}
	if got, _ := inbound.Metadata["bot_username"].(string); got != "memoh1bot" {
		t.Fatalf("bot_username = %q, want memoh1bot", got)
	}
}

func TestSeenTelegramUpdate(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	now := time.Unix(1710000000, 0)

	if adapter.seenTelegramUpdate("cfg-1", 42, now) {
		t.Fatal("first update should not be treated as duplicate")
	}
	if !adapter.seenTelegramUpdate("cfg-1", 42, now.Add(time.Second)) {
		t.Fatal("second update should be treated as duplicate")
	}
	if adapter.seenTelegramUpdate("cfg-2", 42, now.Add(time.Second)) {
		t.Fatal("same update_id under different config should not collide")
	}
	if adapter.seenTelegramUpdate("cfg-1", 43, now.Add(time.Second)) {
		t.Fatal("different update_id should not collide")
	}
	if adapter.seenTelegramUpdate("cfg-1", 0, now.Add(time.Second)) {
		t.Fatal("zero update_id should bypass dedupe")
	}

	later := now.Add(telegramUpdateDedupeTTL + time.Second)
	if adapter.seenTelegramUpdate("cfg-1", 42, later) {
		t.Fatal("expired dedupe entry should be accepted again")
	}
}

func TestTelegramBotCacheKeyIsScopedByConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{BotToken: "shared-token"}
	first := telegramBotCacheKey(cfg, "config-a")
	if got := telegramBotCacheKey(cfg, "config-a"); got != first {
		t.Fatalf("same config cache key changed: %q != %q", got, first)
	}
	if second := telegramBotCacheKey(cfg, "config-b"); second == first {
		t.Fatal("different configs must not share a Telegram bot cache key")
	}
}

func TestIsTelegramMediaGroupForChat(t *testing.T) {
	t.Parallel()

	if isTelegramMediaGroupForChat("12:group-a", 12) == false {
		t.Fatal("expected same chat key to match")
	}
	if isTelegramMediaGroupForChat("123:group-a", 12) {
		t.Fatal("expected different chat key to not match")
	}
	if isTelegramMediaGroupForChat("", 12) {
		t.Fatal("expected empty key to not match")
	}
	if isTelegramMediaGroupForChat("12:group-a", 0) {
		t.Fatal("expected zero chat id to not match")
	}
}

func TestTelegramAdapter_Type(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	if adapter.Type() != Type {
		t.Fatalf("Type should return telegram: %s", adapter.Type())
	}
}

func TestTelegramAdapter_OpenStreamEmptyTarget(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	ctx := context.Background()
	cfg := channel.ChannelConfig{}
	_, err := adapter.OpenStream(ctx, cfg, "", channel.StreamOptions{})
	if err == nil {
		t.Fatal("empty target should return error")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Fatalf("expected target in error: %v", err)
	}
}

func TestResolveTelegramSender_SenderChat(t *testing.T) {
	t.Parallel()

	msg := &tele.Message{
		SenderChat: &tele.Chat{ID: 456, Username: "group", Title: "My Group"},
	}
	externalID, displayName, attrs := resolveTelegramSender(msg)
	if externalID != "456" {
		t.Fatalf("unexpected externalID: %s", externalID)
	}
	if displayName != "My Group" {
		t.Fatalf("unexpected displayName: %s", displayName)
	}
	if attrs["sender_chat_id"] != "456" || attrs["sender_chat_username"] != "group" {
		t.Fatalf("unexpected attrs: %#v", attrs)
	}
}

func TestTelegramRecipient_NumericChatID(t *testing.T) {
	t.Parallel()

	r, chatID, err := telegramRecipient("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Recipient() != "12345" {
		t.Fatalf("unexpected recipient: %q", r.Recipient())
	}
	if chatID != 12345 {
		t.Fatalf("unexpected chatID: %d", chatID)
	}
}

func TestTelegramRecipient_ChannelUsername(t *testing.T) {
	t.Parallel()

	r, chatID, err := telegramRecipient("@channel")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Recipient() != "@channel" {
		t.Fatalf("unexpected recipient: %q", r.Recipient())
	}
	if chatID != 0 {
		t.Fatalf("expected zero chatID for channel username: %d", chatID)
	}
}

func TestTelegramRecipient_Invalid(t *testing.T) {
	t.Parallel()

	_, _, err := telegramRecipient("invalid")
	if err == nil {
		t.Fatal("expected error for invalid target")
	}
	if !strings.Contains(err.Error(), "chat_id") {
		t.Fatalf("expected chat_id in error: %v", err)
	}
}

func TestTelegramAdapter_NormalizeAndResolve(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	norm, err := adapter.NormalizeConfig(map[string]any{"botToken": "t1"})
	if err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if norm["botToken"] != "t1" {
		t.Fatalf("unexpected normalized: %#v", norm)
	}
	userNorm, err := adapter.NormalizeUserConfig(map[string]any{"username": "u1"})
	if err != nil {
		t.Fatalf("NormalizeUserConfig: %v", err)
	}
	if userNorm["username"] != "u1" {
		t.Fatalf("unexpected user config: %#v", userNorm)
	}
	if got := adapter.NormalizeTarget("https://t.me/x"); got != "@x" {
		t.Fatalf("NormalizeTarget: %s", got)
	}
	target, err := adapter.ResolveTarget(map[string]any{"chat_id": "123"})
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	if target != "123" {
		t.Fatalf("ResolveTarget: %s", target)
	}
}

func TestTelegramAdapter_SendRichPartsUsesRichMessage(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/sendChatAction") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":true}`)),
			}, nil
		}
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("expected sendRichMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		resp := `{"ok":true,"result":{"message_id":77,"chat":{"id":123}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(resp)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "hello <world>", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
				{Type: channel.MessagePartLink, Text: "docs & more", URL: "https://example.test/path?a=1&b=2"},
				{Type: channel.MessagePartCodeBlock, Text: "fmt.Println(\"<ok>\")", Language: "go"},
			},
			Reply: &channel.ReplyRef{MessageID: "42"},
			Actions: []channel.Action{
				{Label: "Approve", Value: "approve:1"},
			},
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send rich parts: %v", err)
	}

	if got, _ := gotBody["chat_id"].(string); got != "123" {
		t.Fatalf("expected chat_id=123, got %q", got)
	}
	reply, _ := gotBody["reply_parameters"].(map[string]any)
	if reply == nil || reply["message_id"] != float64(42) {
		t.Fatalf("expected reply_parameters message id, got %#v", reply)
	}
	if _, ok := gotBody["reply_markup"]; !ok {
		t.Fatalf("expected inline keyboard reply_markup, got %v", gotBody)
	}
	html := telegramRichHTMLFromBody(t, gotBody)
	for _, want := range []string{
		"<p><b>hello &lt;world&gt;</b></p>",
		`<p><a href="https://example.test/path?a=1&amp;b=2">docs &amp; more</a></p>`,
		`<pre><code class="language-go">fmt.Println("&lt;ok&gt;")</code></pre>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected rich html to contain %q, got:\n%s", want, html)
		}
	}
	rich, _ := gotBody["rich_message"].(map[string]any)
	if rich == nil || rich["skip_entity_detection"] != true {
		t.Fatalf("expected skip_entity_detection=true, got %#v", rich)
	}
}

func TestTelegramAdapter_SendMarkdownMathUsesRichMarkdown(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("expected sendRichMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":78,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	text := "Inline $x^2 + y^2$ and block:\n\n$$E = mc^2$$"
	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Text:   text,
			Format: channel.MessageFormatMarkdown,
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send markdown math: %v", err)
	}
	if got := telegramRichMarkdownFromBody(t, gotBody); got != text {
		t.Fatalf("unexpected rich markdown:\n got: %q\nwant: %q", got, text)
	}
	rich, _ := gotBody["rich_message"].(map[string]any)
	if _, ok := rich["html"]; ok {
		t.Fatalf("markdown math should not also send rich HTML: %#v", rich)
	}
	if _, ok := gotBody["parse_mode"]; ok {
		t.Fatalf("sendRichMessage payload should not include parse_mode: %#v", gotBody)
	}
}

func TestTelegramAdapter_SendPlainMathUsesRichMarkdown(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("expected sendRichMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":80,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	text := "麦克斯韦方程组：\n\n$$\n\\nabla \\cdot \\mathbf{E} = \\frac{\\rho}{\\varepsilon_0}\n$$"
	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Text:   text,
			Format: channel.MessageFormatPlain,
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send plain math: %v", err)
	}
	if got := telegramRichMarkdownFromBody(t, gotBody); got != text {
		t.Fatalf("unexpected rich markdown:\n got: %q\nwant: %q", got, text)
	}
}

func TestTelegramAdapter_SendBracketLatexNormalizesToRichMarkdown(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("expected sendRichMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":79,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Text:   `Use \(\alpha+\beta\), then \[E = mc^2\].`,
			Format: channel.MessageFormatMarkdown,
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send bracket latex: %v", err)
	}
	if got, want := telegramRichMarkdownFromBody(t, gotBody), `Use $\alpha+\beta$, then $$E = mc^2$$.`; got != want {
		t.Fatalf("unexpected normalized rich markdown:\n got: %q\nwant: %q", got, want)
	}
}

func TestTelegramAdapter_MediumRichPartsUsesRichMessage(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("expected sendRichMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":80,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: strings.Repeat("你", telegramMaxMessageLength+100), Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			},
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send medium rich parts: %v", err)
	}
	html := telegramRichHTMLFromBody(t, gotBody)
	if got := utf8.RuneCountInString(html); got <= telegramMaxMessageLength {
		t.Fatalf("test must exercise rich payloads above normal text limit, got %d", got)
	}
}

func TestTelegramAdapter_LongRichPartsFallsBackToPlainText(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("long rich parts should not call sendRichMessage")
		}
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("expected sendMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":80,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: strings.Repeat("你", telegramMaxMessageLength*9), Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
			},
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send long rich parts: %v", err)
	}
	text, _ := gotBody["text"].(string)
	if utf8.RuneCountInString(text) > telegramMaxMessageLength {
		t.Fatalf("fallback text should be truncated to %d runes, got %d", telegramMaxMessageLength, utf8.RuneCountInString(text))
	}
	if got, _ := gotBody["parse_mode"].(string); got != "" {
		t.Fatalf("long rich fallback should use plain text parse mode, got body %v", gotBody)
	}
}

func TestTelegramAdapter_RichHTMLOverflowFallbackSplitsPlainText(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var bodies []map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			t.Fatalf("HTML-overflow rich parts should not call sendRichMessage")
		}
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("expected sendMessage, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		bodies = append(bodies, decodeTelegramBody(t, body))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":80,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	parts := telegramHTMLOverflowParts()
	plain := channel.RenderPartsAsPlain(parts)
	rich := renderTelegramMessagePartsRichMessage(channel.Message{Parts: parts})
	if utf8.RuneCountInString(plain) > telegramMaxRichMessageLength {
		t.Fatalf("test plain fallback must be within rich limit, got %d", utf8.RuneCountInString(plain))
	}
	if utf8.RuneCountInString(rich.HTML) <= telegramMaxRichMessageLength {
		t.Fatalf("test rich HTML must exceed rich limit, got %d", utf8.RuneCountInString(rich.HTML))
	}

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts:  parts,
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send HTML-overflow rich parts: %v", err)
	}
	if len(bodies) < 2 {
		t.Fatalf("expected fallback plain text to split, got %d sendMessage call(s)", len(bodies))
	}
	var joined strings.Builder
	for i, body := range bodies {
		text, _ := body["text"].(string)
		if got := utf8.RuneCountInString(text); got > telegramMaxMessageLength {
			t.Fatalf("fallback chunk %d exceeds Telegram text limit: %d", i, got)
		}
		joined.WriteString(text)
	}
	if got := utf8.RuneCountInString(joined.String()); got <= telegramMaxMessageLength {
		t.Fatalf("fallback sent only one truncated text window, got %d runes", got)
	}
}

func TestTelegramDescriptorUsesRichTextChunkLimit(t *testing.T) {
	t.Parallel()

	policy := channel.NormalizeOutboundPolicy(NewTelegramAdapter(nil).Descriptor().OutboundPolicy)
	if policy.RichTextChunkLimit <= policy.TextChunkLimit {
		t.Fatalf("Telegram rich text limit should exceed text limit, got text=%d rich=%d", policy.TextChunkLimit, policy.RichTextChunkLimit)
	}
}

func TestTelegramAttachmentSendOptionsCarriesActions(t *testing.T) {
	t.Parallel()

	opts := telegramAttachmentSendOptions("", 0, []channel.Action{{
		Label: "Open",
		URL:   "https://example.com",
	}})
	if opts == nil || opts.ReplyMarkup == nil || len(opts.ReplyMarkup.InlineKeyboard) != 1 {
		t.Fatalf("expected attachment send options to include inline keyboard, got %+v", opts)
	}
	btn := opts.ReplyMarkup.InlineKeyboard[0][0]
	if btn.Text != "Open" || btn.URL != "https://example.com" {
		t.Fatalf("unexpected inline keyboard button: %+v", btn)
	}
}

func TestSendTelegramTextWithActionsUsesLabelFallbackForActionOnlyMessage(t *testing.T) {
	var body map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("expected sendMessage, got %s", req.URL.Path)
		}
		raw, _ := io.ReadAll(req.Body)
		body = decodeTelegramBody(t, raw)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":95,"chat":{"id":123}}}`)),
		}, nil
	}))

	_, _, err := sendTelegramTextWithActionsReturnMessage(bot, "123", "", 0, "", []channel.Action{{
		Label: "Open",
		URL:   "https://example.com",
	}})
	if err != nil {
		t.Fatalf("sendTelegramTextWithActionsReturnMessage: %v", err)
	}
	if got, _ := body["text"].(string); got != "Open" {
		t.Fatalf("expected action label fallback text, got body=%v", body)
	}
	if _, ok := body["reply_markup"]; !ok {
		t.Fatalf("expected reply_markup, got body=%v", body)
	}
}

func TestSendTelegramTextWithActionsRejectsActionOnlyInvalidButtons(t *testing.T) {
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid action-only message should fail before HTTP request: %s", req.URL.Path)
		return nil, nil
	}))

	_, _, err := sendTelegramTextWithActionsReturnMessage(bot, "123", "", 0, "", []channel.Action{{
		Label: "Too large",
		Value: strings.Repeat("x", telegramMaxCallbackDataBytes+1),
	}})
	if err == nil || !strings.Contains(err.Error(), "callback data") {
		t.Fatalf("expected invalid action error, got %v", err)
	}
}

func TestSendTelegramTextWithActionsRejectsTextInvalidButtons(t *testing.T) {
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid action should fail before HTTP request: %s", req.URL.Path)
		return nil, nil
	}))

	_, _, err := sendTelegramTextWithActionsReturnMessage(bot, "123", "Choose", 0, "", []channel.Action{{
		Label: "Unsafe",
		URL:   "javascript:alert(1)",
	}})
	if err == nil || !strings.Contains(err.Error(), "url must be http(s)") {
		t.Fatalf("expected invalid action error, got %v", err)
	}
}

func TestSendTelegramRichMessageRejectsInvalidActionsBeforeRequest(t *testing.T) {
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid rich action should fail before HTTP request: %s", req.URL.Path)
		return nil, nil
	}))

	_, _, err := sendTelegramRichMessageReturnMessage(bot, "123", telegramInputRichMessage{HTML: "<p>Choose</p>"}, 0, []channel.Action{{
		Label: "Too large",
		Value: strings.Repeat("x", telegramMaxCallbackDataBytes+1),
	}})
	if err == nil || !strings.Contains(err.Error(), "callback data") {
		t.Fatalf("expected invalid rich action error, got %v", err)
	}
}

func TestTelegramPreparedOutboundValidationRejectsInvalidActions(t *testing.T) {
	t.Parallel()

	err := NewTelegramAdapter(nil).ValidatePreparedOutbound(context.Background(), channel.ChannelConfig{}, "123", channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Text: "Choose",
			Actions: []channel.Action{{
				Label: "Too large",
				Value: strings.Repeat("x", telegramMaxCallbackDataBytes+1),
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "callback data") {
		t.Fatalf("expected invalid action preflight error, got %v", err)
	}
}

func telegramHTMLOverflowParts() []channel.MessagePart {
	parts := make([]channel.MessagePart, 0, 3000)
	for range 3000 {
		parts = append(parts, channel.MessagePart{
			Type:   channel.MessagePartText,
			Text:   "x",
			Styles: []channel.MessageTextStyle{channel.MessageStyleBold},
		})
	}
	return parts
}

func TestTelegramAdapter_SendRichPartsFallsBackToText(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var paths []string
	var fallbackBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		body, _ := io.ReadAll(req.Body)
		decoded := decodeTelegramBody(t, body)
		if strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			resp := `{"ok":false,"error_code":404,"description":"Not Found: method not found"}`
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(resp)),
			}, nil
		}
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("expected fallback sendMessage, got %s", req.URL.Path)
		}
		fallbackBody = decoded
		resp := `{"ok":true,"result":{"message_id":78,"chat":{"id":123}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(resp)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "hello"},
				{Type: channel.MessagePartLink, Text: "docs", URL: "https://example.test"},
			},
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send rich parts with fallback: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected rich send plus text fallback, got paths %v", paths)
	}
	if got, _ := fallbackBody["text"].(string); got != "hello\n\n<a href=\"https://example.test\">docs</a>" {
		t.Fatalf("expected plain text fallback, got body %v", fallbackBody)
	}
	if got, _ := fallbackBody["parse_mode"].(string); got != tele.ModeHTML {
		t.Fatalf("expected HTML parse mode for rich fallback, got body %v", fallbackBody)
	}
}

func TestTelegramAdapter_SendRichPartsFallbackEscapesLiteralMarkdown(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var fallbackBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		decoded := decodeTelegramBody(t, body)
		if strings.HasSuffix(req.URL.Path, "/sendRichMessage") {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error_code":404,"description":"Not Found: method not found"}`)),
			}, nil
		}
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("expected fallback sendMessage, got %s", req.URL.Path)
		}
		fallbackBody = decoded
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":79,"chat":{"id":123}}}`)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	msg := channel.PreparedOutboundMessage{
		Target: "123",
		Message: channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: `literal [evil](https://evil.test) <tag>`},
				{Type: channel.MessagePartLink, Text: `docs "quoted"`, URL: `https://example.test/?q="x"&ok=1`},
			},
		}},
	}

	if err := adapter.Send(context.Background(), channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}}, msg); err != nil {
		t.Fatalf("send rich parts with fallback: %v", err)
	}
	text, _ := fallbackBody["text"].(string)
	if strings.Contains(text, `<a href="https://evil.test">`) {
		t.Fatalf("literal markdown text became a link in fallback: %q", text)
	}
	for _, want := range []string{
		`literal [evil](https://evil.test) &lt;tag&gt;`,
		`<a href="https://example.test/?q=&quot;x&quot;&amp;ok=1">docs "quoted"</a>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected fallback text to contain %q, got %q", want, text)
		}
	}
	if got, _ := fallbackBody["parse_mode"].(string); got != tele.ModeHTML {
		t.Fatalf("expected HTML parse mode for rich fallback, got body %v", fallbackBody)
	}
}

func TestTelegramAdapter_UpdateRichPartsUsesRichMessage(t *testing.T) {
	adapter := NewTelegramAdapter(nil)

	var gotBody map[string]any
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/editMessageText") {
			t.Fatalf("expected editMessageText, got %s", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = decodeTelegramBody(t, body)
		resp := `{"ok":true,"result":{"message_id":88,"chat":{"id":123}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(resp)),
		}, nil
	}))

	origGetBot := getOrCreateBotForTest
	getOrCreateBotForTest = func(_ *TelegramAdapter, _, _ string) (*tele.Bot, error) {
		return bot, nil
	}
	defer func() { getOrCreateBotForTest = origGetBot }()

	err := adapter.Update(context.Background(),
		channel.ChannelConfig{ID: "test", Credentials: map[string]any{"bot_token": "fake"}},
		"123",
		"88",
		channel.PreparedMessage{Message: channel.Message{
			Format: channel.MessageFormatRich,
			Parts: []channel.MessagePart{
				{Type: channel.MessagePartText, Text: "done", Styles: []channel.MessageTextStyle{channel.MessageStyleItalic}},
			},
		}},
	)
	if err != nil {
		t.Fatalf("update rich parts: %v", err)
	}

	chatID, _ := gotBody["chat_id"].(string)
	msgID, _ := gotBody["message_id"].(float64)
	if chatID != "123" || msgID != 88 {
		t.Fatalf("unexpected edit target: %v", gotBody)
	}
	if got, _ := gotBody["text"].(string); got != "" {
		t.Fatalf("rich edit should not send text field, got %q", got)
	}
	html := telegramRichHTMLFromBody(t, gotBody)
	if !strings.Contains(html, "<p><i>done</i></p>") {
		t.Fatalf("expected italic rich html, got:\n%s", html)
	}
}

func TestConfig_Endpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		baseURL  string
		wantAPI  string
		wantFile string
	}{
		{"default", "", "https://api.telegram.org/bot%s/%s", "https://api.telegram.org/file/bot%s/%s"},
		{"custom", "https://tg.example.com", "https://tg.example.com/bot%s/%s", "https://tg.example.com/file/bot%s/%s"},
		{"trailing slash", "https://tg.example.com/", "https://tg.example.com/bot%s/%s", "https://tg.example.com/file/bot%s/%s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{BotToken: "tok", APIBaseURL: tt.baseURL}
			if got := cfg.apiEndpoint(); got != tt.wantAPI {
				t.Fatalf("apiEndpoint() = %q, want %q", got, tt.wantAPI)
			}
			if got := cfg.fileEndpoint(); got != tt.wantFile {
				t.Fatalf("fileEndpoint() = %q, want %q", got, tt.wantFile)
			}
		})
	}
}

func TestParseConfig_APIBaseURL(t *testing.T) {
	t.Parallel()

	t.Run("camelCase key", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig(map[string]any{"botToken": "t1", "apiBaseURL": "https://proxy.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIBaseURL != "https://proxy.example.com" {
			t.Fatalf("unexpected APIBaseURL: %q", cfg.APIBaseURL)
		}
	})

	t.Run("snake_case key", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig(map[string]any{"bot_token": "t2", "api_base_url": "https://proxy2.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIBaseURL != "https://proxy2.example.com" {
			t.Fatalf("unexpected APIBaseURL: %q", cfg.APIBaseURL)
		}
	})

	t.Run("empty base URL", func(t *testing.T) {
		t.Parallel()
		cfg, err := parseConfig(map[string]any{"botToken": "t3"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.APIBaseURL != "" {
			t.Fatalf("expected empty APIBaseURL, got %q", cfg.APIBaseURL)
		}
	})

	t.Run("http proxy config", func(t *testing.T) {
		t.Parallel()
		proxyURL := "http://memoh:" + "secret" + "@sztu.cc:3128"
		cfg, err := parseConfig(map[string]any{
			"botToken":     "t4",
			"httpProxyUrl": proxyURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.HTTPProxy.URL != proxyURL {
			t.Fatalf("unexpected httpProxyUrl: %q", cfg.HTTPProxy.URL)
		}
	})
}

func TestNormalizeConfig_APIBaseURL(t *testing.T) {
	t.Parallel()

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		norm, err := normalizeConfig(map[string]any{"botToken": "t1", "apiBaseURL": "https://proxy.example.com"})
		if err != nil {
			t.Fatal(err)
		}
		if norm["apiBaseURL"] != "https://proxy.example.com" {
			t.Fatalf("expected apiBaseURL in output: %#v", norm)
		}
	})

	t.Run("omitted when empty", func(t *testing.T) {
		t.Parallel()
		norm, err := normalizeConfig(map[string]any{"botToken": "t2"})
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := norm["apiBaseURL"]; exists {
			t.Fatalf("empty apiBaseURL should be omitted: %#v", norm)
		}
	})

	t.Run("includes http proxy config", func(t *testing.T) {
		t.Parallel()
		proxyURL := "http://memoh:" + "secret" + "@sztu.cc:3128"
		norm, err := normalizeConfig(map[string]any{
			"botToken":     "t3",
			"httpProxyUrl": proxyURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		if norm["httpProxyUrl"] != proxyURL {
			t.Fatalf("unexpected httpProxyUrl in output: %#v", norm)
		}
	})
}

func TestIsTelegramMessageNotModified(t *testing.T) {
	t.Parallel()

	// Exact production error from Telegram API (editMessageText when content unchanged).
	const productionMessageNotModified = "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message"

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("network error"), false},
		{"other api error", &tele.Error{Code: 400, Description: "Bad Request: chat not found"}, false},
		{"message is not modified", &tele.Error{Code: 400, Description: productionMessageNotModified}, true},
		{"production exact", &tele.Error{Code: 400, Description: productionMessageNotModified}, true},
		{"same text but code 500", &tele.Error{Code: 500, Description: "message is not modified"}, false},
		{"wrapped same", fmt.Errorf("wrapped: %w", &tele.Error{Code: 400, Description: "Bad Request: message is not modified"}), true},
		// telebot only types errors whose description matches a sentinel; longer or
		// novel variants of "message is not modified" arrive as fmt.Errorf strings
		// shaped "telegram: <description> (<code>)" with no *tele.Error in the chain.
		{"telebot fmt-wrapped variant", errors.New("telegram: Bad Request: message is not modified: caption is unchanged (400)"), true},
		{"telebot fmt-wrapped wrong code", errors.New("telegram: Bad Request: message is not modified (500)"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTelegramMessageNotModified(tt.err)
			if got != tt.want {
				t.Fatalf("isTelegramMessageNotModified() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTelegramEditUnrecoverable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("network error"), false},
		{"message to edit not found", &tele.Error{Code: 400, Description: "Bad Request: message to edit not found"}, true},
		{"message can't be edited", &tele.Error{Code: 400, Description: "Bad Request: message can't be edited"}, true},
		{"message_id_invalid", &tele.Error{Code: 400, Description: "Bad Request: MESSAGE_ID_INVALID"}, true},
		{"not modified is not this", &tele.Error{Code: 400, Description: "Bad Request: message is not modified"}, false},
		{"transient chat not found", &tele.Error{Code: 400, Description: "Bad Request: chat not found"}, false},
		{"rate limit not terminal", &tele.Error{Code: 429, Description: "Too Many Requests"}, false},
		{"same text but code 500", &tele.Error{Code: 500, Description: "message to edit not found"}, false},
		{"wrapped terminal", fmt.Errorf("wrapped: %w", &tele.Error{Code: 400, Description: "Bad Request: message to edit not found"}), true},
		// telebot has no sentinel for these; extractOk emits the raw fmt.Errorf
		// shape, which used to slip past the *tele.Error check and broke the
		// streaming final-edit recovery path.
		{"telebot fmt-wrapped not found", errors.New("telegram: Bad Request: message to edit not found (400)"), true},
		{"telebot fmt-wrapped message_id_invalid", errors.New("telegram: Bad Request: MESSAGE_ID_INVALID (400)"), true},
		{"telebot fmt-wrapped wrong code", errors.New("telegram: Bad Request: message to edit not found (500)"), false},
		{"telebot fmt-wrapped unrelated", errors.New("telegram: Bad Request: chat not found (400)"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTelegramEditUnrecoverable(tt.err)
			if got != tt.want {
				t.Fatalf("isTelegramEditUnrecoverable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsTelegramTooManyRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"429", &tele.Error{Code: 429, Description: "Too Many Requests"}, true},
		{"400", &tele.Error{Code: 400, Description: "Bad Request"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTelegramTooManyRequests(tt.err)
			if got != tt.want {
				t.Fatalf("isTelegramTooManyRequests() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetTelegramRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"nil", nil, 0},
		{"no retry_after", &tele.Error{Code: 429, Description: "Too Many Requests"}, 0},
		{"retry_after 2", tele.FloodError{RetryAfter: 2}, 2 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getTelegramRetryAfter(tt.err)
			if got != tt.want {
				t.Fatalf("getTelegramRetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruncateTelegramText(t *testing.T) {
	t.Parallel()

	short := "hello"
	if got := truncateTelegramText(short); got != short {
		t.Fatalf("short text should not be truncated: %q", got)
	}

	// Exactly at limit.
	exact := strings.Repeat("a", telegramMaxMessageLength)
	if got := truncateTelegramText(exact); got != exact {
		t.Fatalf("exact-limit text should not be truncated, len=%d", len(got))
	}

	// Over limit with ASCII.
	over := strings.Repeat("a", telegramMaxMessageLength+100)
	got := truncateTelegramText(over)
	if utf8.RuneCountInString(got) > telegramMaxMessageLength {
		t.Fatalf("truncated text should be <= %d chars: got %d", telegramMaxMessageLength, utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated text should end with '...': %q", got[len(got)-10:])
	}

	// Over limit with multi-byte characters (Chinese: 3 bytes each).
	multi := strings.Repeat("\u4f60", telegramMaxMessageLength+1)
	got = truncateTelegramText(multi)
	if utf8.RuneCountInString(got) > telegramMaxMessageLength {
		t.Fatalf("truncated multi-byte text should be <= %d chars: got %d", telegramMaxMessageLength, utf8.RuneCountInString(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatal("truncated multi-byte text should end with '...'")
	}
	if utf8.RuneCountInString(got) != telegramMaxMessageLength {
		t.Fatalf("truncated multi-byte text should keep exact char budget: got %d", utf8.RuneCountInString(got))
	}
	// Verify no broken runes.
	trimmed := strings.TrimSuffix(got, "...")
	for i := 0; i < len(trimmed); {
		r, size := utf8.DecodeRuneInString(trimmed[i:])
		if r == utf8.RuneError && size == 1 {
			t.Fatalf("truncated text contains invalid UTF-8 at byte %d", i)
		}
		i += size
	}
}

func TestSanitizeTelegramText(t *testing.T) {
	t.Parallel()

	valid := "hello world"
	if got := sanitizeTelegramText(valid); got != valid {
		t.Fatalf("valid text should not change: %q", got)
	}

	// Invalid UTF-8 byte sequence.
	invalid := "hello\xff\xfeworld"
	got := sanitizeTelegramText(invalid)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized text should be valid UTF-8: %q", got)
	}
	if got != "helloworld" {
		t.Fatalf("expected invalid bytes stripped: %q", got)
	}
}

func TestEditTelegramMessageText_429ReturnsError(t *testing.T) {
	t.Parallel()

	var sendCalls int
	origSend := sendEditForTest
	sendEditForTest = func(_ *tele.Bot, _ int64, _ int, _ string, _ string) error {
		sendCalls++
		return tele.FloodError{RetryAfter: 1}
	}
	defer func() { sendEditForTest = origSend }()

	bot := &tele.Bot{Token: "test"}
	err := editTelegramMessageText(bot, 1, 1, "hi", "")
	if err == nil {
		t.Fatal("editTelegramMessageText on 429 should return error for caller to handle")
	}
	if !isTelegramTooManyRequests(err) {
		t.Fatalf("expected 429 error: %v", err)
	}
	if sendCalls != 1 {
		t.Fatalf("send should be called once (no internal retry): got %d", sendCalls)
	}
}

func TestTelegramAdapter_ImplementsProcessingStatusNotifier(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	var _ channel.ProcessingStatusNotifier = adapter
}

func TestProcessingStarted_EmptyParams(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	ctx := context.Background()
	cfg := channel.ChannelConfig{}
	msg := channel.InboundMessage{}

	handle, err := adapter.ProcessingStarted(ctx, cfg, msg, channel.ProcessingStatusInfo{})
	if err != nil {
		t.Fatalf("empty params should not error: %v", err)
	}
	if handle.Token != "" {
		t.Fatalf("empty params should return empty handle: %q", handle.Token)
	}
}

func TestProcessingCompleted_EmptyHandle(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	ctx := context.Background()

	err := adapter.ProcessingCompleted(ctx, channel.ChannelConfig{}, channel.InboundMessage{}, channel.ProcessingStatusInfo{}, channel.ProcessingStatusHandle{})
	if err != nil {
		t.Fatalf("empty handle should be no-op: %v", err)
	}
}

func TestProcessingFailed_DelegatesToCompleted(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	ctx := context.Background()

	err := adapter.ProcessingFailed(ctx, channel.ChannelConfig{}, channel.InboundMessage{}, channel.ProcessingStatusInfo{}, channel.ProcessingStatusHandle{}, errors.New("test"))
	if err != nil {
		t.Fatalf("empty handle should be no-op: %v", err)
	}
}

func TestResolveTelegramFile_PlatformKey(t *testing.T) {
	t.Parallel()

	file, err := resolveTelegramFile(context.Background(), channel.PreparedAttachment{
		Logical:   channel.Attachment{Type: channel.AttachmentImage},
		Kind:      channel.PreparedAttachmentNativeRef,
		NativeRef: "file_id_123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if file.FileID != "file_id_123" {
		t.Fatalf("expected FileID populated, got %+v", file)
	}
}

func TestResolveTelegramFile_PublicURL(t *testing.T) {
	t.Parallel()

	file, err := resolveTelegramFile(context.Background(), channel.PreparedAttachment{
		Logical:   channel.Attachment{Type: channel.AttachmentImage},
		Kind:      channel.PreparedAttachmentPublicURL,
		PublicURL: "https://example.com/img.png",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if file.FileURL != "https://example.com/img.png" {
		t.Fatalf("expected FileURL populated, got %+v", file)
	}
}

func TestResolveTelegramFile_Upload(t *testing.T) {
	t.Parallel()

	file, err := resolveTelegramFile(context.Background(), channel.PreparedAttachment{
		Logical: channel.Attachment{Type: channel.AttachmentImage},
		Kind:    channel.PreparedAttachmentUpload,
		Mime:    "image/png",
		Name:    "test.png",
		Open: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("png-bytes")), nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if file.FileReader == nil {
		t.Fatalf("expected FileReader populated, got %+v", file)
	}
	body, readErr := io.ReadAll(file.FileReader)
	if readErr != nil {
		t.Fatalf("read upload bytes: %v", readErr)
	}
	if string(body) != "png-bytes" {
		t.Fatalf("expected png-bytes, got %q", string(body))
	}
}

func TestResolveTelegramFile_NoReference(t *testing.T) {
	t.Parallel()

	_, err := resolveTelegramFile(context.Background(), channel.PreparedAttachment{
		Logical: channel.Attachment{Type: channel.AttachmentImage},
	})
	if err == nil {
		t.Fatal("expected error when no reference available")
	}
}

func TestResolveTelegramFile_UploadRequiresOpen(t *testing.T) {
	t.Parallel()

	_, err := resolveTelegramFile(context.Background(), channel.PreparedAttachment{
		Logical: channel.Attachment{Type: channel.AttachmentImage},
		Kind:    channel.PreparedAttachmentUpload,
	})
	if err == nil {
		t.Fatal("expected missing upload opener to fail")
	}
}

func TestFileNameFromMime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mime         string
		fallbackType string
		want         string
	}{
		{"image/png", "image", "image.png"},
		{"image/jpeg", "image", "image.jpg"},
		{"image/gif", "image", "image.gif"},
		{"video/mp4", "video", "video.mp4"},
		{"", "image", "image.png"},
		{"application/octet-stream", "", "file.bin"},
	}
	for _, tt := range tests {
		if got := fileNameFromMime(tt.mime, tt.fallbackType); got != tt.want {
			t.Errorf("fileNameFromMime(%q, %q) = %q, want %q", tt.mime, tt.fallbackType, got, tt.want)
		}
	}
}

// TestBuildTelegramSkillActivateCallbackDispatchesFreshTurn pins the
// tap-to-activate contract on the adapter side: a skill-activation callback
// synthesizes the bare "/<name>" slash as a NEW directed message (no in-place
// edit_message_id, so the skill list card survives), and the reply reference
// to the tapped card asserts AttachmentsKnown so the skill-slash attachment
// fail-closed rule doesn't reject the tap.
func TestBuildTelegramSkillActivateCallbackDispatchesFreshTurn(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	update := &tele.Update{
		ID: 101,
		Callback: &tele.Callback{
			ID:     "callback-3",
			Data:   command.EncodeSkillActivateCallback("flutter-adding-home-screen-widgets"),
			Sender: &tele.User{ID: 123, Username: "alice"},
			Message: &tele.Message{
				ID:   456,
				Text: "Skills",
				Chat: &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
			},
		},
	}

	msg, ok := adapter.buildTelegramCallbackInboundMessage(channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected callback inbound message")
	}
	if got := msg.Message.PlainText(); got != "/flutter-adding-home-screen-widgets" {
		t.Fatalf("callback text = %q", got)
	}
	if mentioned, _ := msg.Metadata["is_mentioned"].(bool); !mentioned {
		t.Fatalf("metadata = %#v, want directed callback", msg.Metadata)
	}
	if editID, ok := msg.Metadata["edit_message_id"]; ok {
		t.Fatalf("edit_message_id = %v, want absent for a fresh activation turn", editID)
	}
	if msg.Message.Reply == nil || !msg.Message.Reply.AttachmentsKnown {
		t.Fatalf("reply = %+v, want AttachmentsKnown so the slash attachment rule doesn't fail closed", msg.Message.Reply)
	}
}

// TestBuildTelegramPaginationCallbackKeepsEditInPlace guards the existing
// pagination behavior against the skill-activation branch: list-page taps
// still edit the card in place, and the reply to the bot's own card asserts
// AttachmentsKnown so no slash fail-closed rule can misfire on a tap.
func TestBuildTelegramPaginationCallbackKeepsEditInPlace(t *testing.T) {
	t.Parallel()

	adapter := NewTelegramAdapter(nil)
	update := &tele.Update{
		ID: 102,
		Callback: &tele.Callback{
			ID:     "callback-4",
			Data:   command.EncodeListCallback("skill", "list", nil, 1),
			Sender: &tele.User{ID: 123, Username: "alice"},
			Message: &tele.Message{
				ID:   456,
				Text: "Skills",
				Chat: &tele.Chat{ID: -10001, Type: tele.ChatGroup, Title: "Test Group"},
			},
		},
	}

	msg, ok := adapter.buildTelegramCallbackInboundMessage(channel.ChannelConfig{}, update)
	if !ok {
		t.Fatal("expected callback inbound message")
	}
	if editID, _ := msg.Metadata["edit_message_id"].(string); editID != "456" {
		t.Fatalf("edit_message_id = %q, want 456", editID)
	}
	if msg.Message.Reply == nil || !msg.Message.Reply.AttachmentsKnown {
		t.Fatalf("reply = %+v, want AttachmentsKnown=true — the tapped card is the bot's own message", msg.Message.Reply)
	}
}
