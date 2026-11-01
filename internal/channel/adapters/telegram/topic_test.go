package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v4"

	"github.com/felinics/memoh/internal/agent/adapter/channelmessaging"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/messaging"
)

type telegramCall struct {
	method string
	body   map[string]any
}

func captureTelegramCalls(t *testing.T) *[]telegramCall {
	t.Helper()
	calls := &[]telegramCall{}
	bot := newTestTelegramBot(telegramRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		*calls = append(*calls, telegramCall{method: method, body: decodeTelegramBody(t, body)})
		result := `{"message_id":77,"chat":{"id":-100123}}`
		switch method {
		case "sendChatAction", "sendMessageDraft", "setMessageReaction":
			result = "true"
		case "sendPhoto":
			result = `{"message_id":77,"chat":{"id":-100123},"photo":[{"file_id":"p","file_unique_id":"u","width":1,"height":1}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)),
		}, nil
	}))
	orig := getOrCreateBotForTest
	getOrCreateBotForTest = func(*TelegramAdapter, string, string) (*tele.Bot, error) { return bot, nil }
	t.Cleanup(func() { getOrCreateBotForTest = orig })
	return calls
}

func assertTelegramCallsTarget(t *testing.T, calls []telegramCall, wantChat, wantThread string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("no Telegram requests captured")
	}
	for _, call := range calls {
		if got := fmt.Sprint(call.body["chat_id"]); got != wantChat {
			t.Fatalf("%s chat_id = %q, want %q", call.method, got, wantChat)
		}
		thread, hasThread := call.body["message_thread_id"]
		addressesMessage := strings.HasPrefix(call.method, "edit") || call.method == "setMessageReaction"
		switch {
		case addressesMessage || wantThread == "":
			if hasThread {
				t.Fatalf("%s carries message_thread_id %v, want none", call.method, thread)
			}
		case fmt.Sprint(thread) != wantThread:
			t.Fatalf("%s message_thread_id = %v, want %s", call.method, thread, wantThread)
		}
	}
}

func TestTelegramInboundTopicThread(t *testing.T) {
	adapter := NewTelegramAdapter(nil)
	bot := newStubTelegramBot(t)
	bot.Me = &tele.User{ID: 999, Username: "memoh_bot"}
	forum := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup, Title: "Forum", IsForum: true}
	group := &tele.Chat{ID: -100456, Type: tele.ChatSuperGroup, Title: "Group"}
	private := &tele.Chat{ID: 555, Type: tele.ChatPrivate}
	topicRoot := &tele.Message{ID: 45, Sender: bot.Me, TopicCreated: &tele.Topic{Name: "A"}}

	cases := []struct {
		name           string
		raw            *tele.Message
		wantThread     string
		wantTarget     string
		wantReplyID    string
		wantReplyToBot bool
	}{
		{
			name:       "forum topic drops implicit reply to topic root",
			raw:        &tele.Message{Chat: forum, ThreadID: 45, TopicMessage: true, ReplyTo: topicRoot},
			wantThread: "45", wantTarget: "-100123/45",
		},
		{
			name:       "explicit reply inside topic",
			raw:        &tele.Message{Chat: forum, ThreadID: 45, TopicMessage: true, ReplyTo: &tele.Message{ID: 60, Sender: bot.Me}},
			wantThread: "45", wantTarget: "-100123/45", wantReplyID: "60", wantReplyToBot: true,
		},
		{
			name:       "general topic keeps chat target",
			raw:        &tele.Message{Chat: forum},
			wantTarget: "-100123",
		},
		{
			name:       "reply chain in plain supergroup is not a topic",
			raw:        &tele.Message{Chat: group, ThreadID: 70, ReplyTo: &tele.Message{ID: 70, Sender: &tele.User{ID: 8}}},
			wantTarget: "-100456", wantReplyID: "70",
		},
		{
			name:       "private chat topic",
			raw:        &tele.Message{Chat: private, ThreadID: 9, TopicMessage: true},
			wantThread: "9", wantTarget: "555/9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.raw.ID = 100
			tc.raw.Sender = &tele.User{ID: 7, FirstName: "Alice"}
			tc.raw.Text = "hi"
			msg, ok := adapter.toInboundTelegramMessage(bot, channel.ChannelConfig{}, tc.raw, "hi", nil, nil)
			if !ok {
				t.Fatal("expected inbound message")
			}
			if msg.Conversation.ID != strconv.FormatInt(tc.raw.Chat.ID, 10) {
				t.Fatalf("conversation id = %q, want chat id", msg.Conversation.ID)
			}
			if msg.Conversation.ThreadID != tc.wantThread {
				t.Fatalf("thread = %q, want %q", msg.Conversation.ThreadID, tc.wantThread)
			}
			if msg.ReplyTarget != tc.wantTarget {
				t.Fatalf("reply target = %q, want %q", msg.ReplyTarget, tc.wantTarget)
			}
			gotReplyID := ""
			if msg.Message.Reply != nil {
				gotReplyID = msg.Message.Reply.MessageID
			}
			if gotReplyID != tc.wantReplyID {
				t.Fatalf("reply id = %q, want %q", gotReplyID, tc.wantReplyID)
			}
			if got := msg.Metadata["is_reply_to_bot"]; got != tc.wantReplyToBot {
				t.Fatalf("is_reply_to_bot = %v, want %v", got, tc.wantReplyToBot)
			}
		})
	}
}

func TestTelegramNormalizeTargetKeepsTopic(t *testing.T) {
	adapter := NewTelegramAdapter(nil)
	for raw, want := range map[string]string{
		"-100123/45":     "-100123/45",
		"tg:-100123/45":  "-100123/45",
		"@forum/45":      "@forum/45",
		"-100123":        "-100123",
		"https://t.me/x": "@x",
	} {
		if got := adapter.NormalizeTarget(raw); got != want {
			t.Fatalf("NormalizeTarget(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTelegramTopicTargetOutbound(t *testing.T) {
	cfg := channel.ChannelConfig{ID: "cfg", Credentials: map[string]any{"bot_token": "fake"}}
	image := func(name string) channel.PreparedAttachment {
		return channel.PreparedAttachment{
			Kind:      channel.PreparedAttachmentPublicURL,
			PublicURL: "https://example.test/" + name,
			Logical:   channel.Attachment{Type: channel.AttachmentImage},
		}
	}
	for _, tc := range []struct {
		target, wantChat, wantThread string
	}{
		{target: "-100123/45", wantChat: "-100123", wantThread: "45"},
		{target: "-100123", wantChat: "-100123"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			calls := captureTelegramCalls(t)
			adapter := NewTelegramAdapter(nil)
			ctx := context.Background()
			reply := &channel.ReplyRef{MessageID: "100"}
			sends := []channel.PreparedMessage{
				{Message: channel.Message{Format: channel.MessageFormatPlain, Text: "short", Reply: reply}},
				{Message: channel.Message{
					Format: channel.MessageFormatRich,
					Parts:  []channel.MessagePart{{Type: channel.MessagePartText, Text: strings.Repeat("long ", telegramMaxRichMessageLength/5+10)}},
					Reply:  reply,
				}},
				{Message: channel.Message{Text: "caption", Reply: reply}, Attachments: []channel.PreparedAttachment{image("a.png"), image("b.png")}},
				{Message: channel.Message{
					Format: channel.MessageFormatRich,
					Parts:  []channel.MessagePart{{Type: channel.MessagePartText, Text: "bold", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}}},
				}},
			}
			for _, msg := range sends {
				if err := adapter.Send(ctx, cfg, channel.PreparedOutboundMessage{Target: tc.target, Message: msg}); err != nil {
					t.Fatalf("Send: %v", err)
				}
			}
			if err := adapter.ProcessingRefresh(ctx, cfg, channel.InboundMessage{}, channel.ProcessingStatusInfo{ReplyTarget: tc.target}, channel.ProcessingStatusHandle{}); err != nil {
				t.Fatalf("ProcessingRefresh: %v", err)
			}
			if err := adapter.Update(ctx, cfg, tc.target, "77", channel.PreparedMessage{Message: channel.Message{Text: "edited"}}); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if err := adapter.React(ctx, cfg, tc.target, "77", "👍"); err != nil {
				t.Fatalf("React: %v", err)
			}
			got := *calls
			methods := map[string]int{}
			for _, call := range got {
				methods[call.method]++
			}
			for _, method := range []string{"sendMessage", "sendRichMessage", "sendPhoto", "sendChatAction", "editMessageText", "setMessageReaction"} {
				if methods[method] == 0 {
					t.Fatalf("expected a %s request, got %v", method, methods)
				}
			}
			if methods["sendMessage"] < 3 || methods["sendPhoto"] != 2 {
				t.Fatalf("expected chunked text and two photos, got %v", methods)
			}
			assertTelegramCallsTarget(t, got, tc.wantChat, tc.wantThread)
		})
	}
}

func TestTelegramPrivateTopicStreamDraftsCarryThread(t *testing.T) {
	calls := captureTelegramCalls(t)
	adapter := NewTelegramAdapter(nil)
	cfg := channel.ChannelConfig{ID: "cfg", Credentials: map[string]any{"bot_token": "fake"}}
	ctx := context.Background()
	stream, err := adapter.OpenStream(ctx, cfg, "555/9", channel.StreamOptions{Metadata: map[string]any{"conversation_type": "private"}})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := stream.Push(ctx, mustPreparedTelegramEvent(t, channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "Hello"})); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got := *calls
	if len(got) != 1 || got[0].method != "sendMessageDraft" {
		t.Fatalf("expected one sendMessageDraft request, got %v", got)
	}
	assertTelegramCallsTarget(t, got, "555", "9")
}

type topicInteractionStore struct {
	askUserInteractionService
	result userinput.AdvanceInteractionResult
	err    error
}

func (s *topicInteractionStore) AdvanceInteraction(context.Context, userinput.AdvanceInteractionInput) (userinput.AdvanceInteractionResult, error) {
	return s.result, s.err
}

func TestTelegramAskUserMessagesStayInTopic(t *testing.T) {
	forum := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup, IsForum: true}
	pending := userinput.Request{ID: testAskUserRequestID, UIPayload: userinput.UIPayload{Questions: []userinput.UIQuestion{{ID: "q1", Text: "Notes?", Kind: userinput.QuestionKindText}}}}
	for _, tc := range []struct {
		name     string
		allow    bool
		store    *topicInteractionStore
		callback bool
	}{
		{name: "forbidden", store: &topicInteractionStore{}},
		{name: "unavailable", allow: true, store: &topicInteractionStore{err: errors.New("down")}},
		{name: "rejected", allow: true, store: &topicInteractionStore{result: userinput.AdvanceInteractionResult{Handled: true, Reject: userinput.RejectEmptyText}}},
		{name: "force-reply prompt", allow: true, callback: true, store: &topicInteractionStore{result: userinput.AdvanceInteractionResult{Handled: true, Request: pending}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := captureTelegramCalls(t)
			adapter := NewTelegramAdapter(nil)
			adapter.userInput = tc.store
			adapter.SetUserInputAuthorizer(func(context.Context, channel.ChannelConfig, channel.InboundMessage) (bool, error) {
				return tc.allow, nil
			})
			bot, _ := getOrCreateBotForTest(adapter, "", "")
			ctx := context.Background()
			cfg := channel.ChannelConfig{BotID: "bot-1"}
			sender := &tele.User{ID: 7}
			if tc.callback {
				card := &tele.Message{ID: 80, Chat: forum, ThreadID: 45, TopicMessage: true, Sender: sender}
				update := &tele.Update{Callback: &tele.Callback{ID: "cb", Sender: sender, Message: card, Data: encodeAskUserCallback("x", testAskUserRequestID, "en", 0)}}
				if !adapter.handleAskUserWizardCallback(ctx, cfg, nil, bot, update) {
					t.Fatal("expected ask_user callback to be handled")
				}
			} else {
				adapter.askUserPromptStore().put(forum.ID, 80, askUserTextPrompt{RequestID: testAskUserRequestID, QuestionID: "q1"})
				update := &tele.Update{Message: &tele.Message{
					ID: 81, Chat: forum, ThreadID: 45, TopicMessage: true, Text: "answer", Sender: sender,
					ReplyTo: &tele.Message{ID: 80, Chat: forum},
				}}
				if !adapter.tryHandleAskUserTextReply(ctx, cfg, nil, bot, update) {
					t.Fatal("expected ask_user reply to be handled")
				}
			}
			var sends []telegramCall
			for _, call := range *calls {
				if call.method == "sendMessage" {
					sends = append(sends, call)
				}
			}
			if len(sends) != 1 {
				t.Fatalf("expected one sendMessage, got %v", *calls)
			}
			assertTelegramCallsTarget(t, sends, "-100123", "45")
		})
	}
}

type telegramTopicStubStore struct {
	cfg channel.ChannelConfig
}

func (s telegramTopicStubStore) ListConfigsByType(context.Context, channel.ChannelType) ([]channel.ChannelConfig, error) {
	return []channel.ChannelConfig{s.cfg}, nil
}

func (s telegramTopicStubStore) ResolveEffectiveConfig(context.Context, string, channel.ChannelType) (channel.ChannelConfig, error) {
	return s.cfg, nil
}

func (telegramTopicStubStore) GetChannelIdentityConfig(context.Context, string, channel.ChannelType) (channel.ChannelIdentityBinding, error) {
	return channel.ChannelIdentityBinding{}, nil
}

// TestTelegramTopicSendToolLandsInInboundTopic feeds the adapter's inbound
// reply target to the discuss-mode send tool with no explicit target and
// checks delivery through the messaging executor, channel manager and adapter.
func TestTelegramTopicSendToolLandsInInboundTopic(t *testing.T) {
	forum := &tele.Chat{ID: -100123, Type: tele.ChatSuperGroup, Title: "Forum", IsForum: true}
	for _, tc := range []struct {
		name       string
		raw        *tele.Message
		wantThread string
	}{
		{name: "topic", raw: &tele.Message{Chat: forum, ThreadID: 45, TopicMessage: true, ReplyTo: &tele.Message{ID: 45}}, wantThread: "45"},
		{name: "general", raw: &tele.Message{Chat: forum}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := captureTelegramCalls(t)
			adapter := NewTelegramAdapter(nil)
			bot, _ := getOrCreateBotForTest(adapter, "", "")
			tc.raw.ID = 100
			tc.raw.Sender = &tele.User{ID: 7, FirstName: "Alice"}
			inbound, ok := adapter.toInboundTelegramMessage(bot, channel.ChannelConfig{}, tc.raw, "@memoh_bot hi", nil, nil)
			if !ok {
				t.Fatal("expected inbound message")
			}

			registry := channel.NewRegistry()
			if err := registry.Register(adapter); err != nil {
				t.Fatalf("register: %v", err)
			}
			cfg := channel.ChannelConfig{ID: "cfg", BotID: "bot-1", ChannelType: Type, Credentials: map[string]any{"bot_token": "fake"}}
			manager := channel.NewManager(nil, registry, telegramTopicStubStore{cfg: cfg}, nil)
			port := channelmessaging.New(manager, registry, nil)
			exec := &messaging.Executor{Sender: port, Reactor: port, Resolver: port}
			session := messaging.SessionContext{BotID: "bot-1", CanOmitTarget: true, CurrentPlatform: Type.String(), ReplyTarget: inbound.ReplyTarget}

			long := strings.Repeat("reply ", telegramMaxMessageLength/6+10)
			if _, err := exec.SendDirect(context.Background(), session, "", map[string]any{"text": long}); err != nil {
				t.Fatalf("SendDirect: %v", err)
			}
			got := *calls
			if len(got) < 2 {
				t.Fatalf("expected chunked delivery, got %d requests", len(got))
			}
			assertTelegramCallsTarget(t, got, "-100123", tc.wantThread)
		})
	}
}
