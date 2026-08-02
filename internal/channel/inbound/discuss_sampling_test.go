package inbound

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/channel/discuss"
	"github.com/memohai/memoh/internal/channel/identities"
	"github.com/memohai/memoh/internal/channel/route"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/chat/timeline"
)

type fakeTelegramDiscussPolicyReader struct {
	policy TelegramDiscussPolicy
	err    error
}

func (r fakeTelegramDiscussPolicyReader) TelegramDiscussPolicy(
	context.Context,
	string,
) (TelegramDiscussPolicy, error) {
	return r.policy, r.err
}

func TestShouldNotifyDiscussSamplesOnlyPassiveTelegramMessages(t *testing.T) {
	tests := []struct {
		name   string
		msg    channel.InboundMessage
		sample float64
		want   bool
	}{
		{
			name: "telegram passive hit",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
			},
			sample: 0.24,
			want:   true,
		},
		{
			name: "telegram passive miss",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
			},
			sample: 0.25,
			want:   false,
		},
		{
			name: "telegram mention bypasses sampling",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
				Metadata:     map[string]any{"is_mentioned": true},
			},
			sample: 0.99,
			want:   true,
		},
		{
			name: "telegram reply bypasses sampling",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
				Metadata:     map[string]any{"is_reply_to_bot": true},
			},
			sample: 0.99,
			want:   true,
		},
		{
			name: "telegram private bypasses sampling",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Conversation: channel.Conversation{Type: channel.ConversationTypePrivate},
			},
			sample: 0.99,
			want:   true,
		},
		{
			name: "other channel bypasses sampling",
			msg: channel.InboundMessage{
				Channel:      channel.ChannelType("feishu"),
				Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
			},
			sample: 0.99,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := &ChannelInboundProcessor{
				discussSample: func() float64 { return tt.sample },
			}
			force := shouldTriggerAssistantResponse(tt.msg)
			got, _, _, _ := processor.shouldNotifyDiscuss(context.Background(), "bot-1", tt.msg, force)
			if got != tt.want {
				t.Fatalf("shouldNotifyDiscuss() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldNotifyDiscussUsesBotSampleRate(t *testing.T) {
	tests := []struct {
		name     string
		reader   TelegramDiscussPolicyReader
		sample   float64
		want     bool
		wantRate float64
	}{
		{
			name: "custom rate hit",
			reader: fakeTelegramDiscussPolicyReader{policy: TelegramDiscussPolicy{
				PassiveSampleRate: 0.75,
			}},
			sample:   0.74,
			want:     true,
			wantRate: 0.75,
		},
		{
			name: "custom rate miss",
			reader: fakeTelegramDiscussPolicyReader{policy: TelegramDiscussPolicy{
				PassiveSampleRate: 0.10,
			}},
			sample:   0.10,
			want:     false,
			wantRate: 0.10,
		},
		{
			name:     "reader error uses default",
			reader:   fakeTelegramDiscussPolicyReader{err: errors.New("read failed")},
			sample:   0.24,
			want:     true,
			wantRate: channel.DefaultTelegramDiscussPassiveSampleRate,
		},
		{
			name: "invalid rate uses default",
			reader: fakeTelegramDiscussPolicyReader{policy: TelegramDiscussPolicy{
				PassiveSampleRate: 1.5,
			}},
			sample:   0.25,
			want:     false,
			wantRate: channel.DefaultTelegramDiscussPassiveSampleRate,
		},
	}

	msg := channel.InboundMessage{
		Channel:      channel.ChannelTypeTelegram,
		Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := &ChannelInboundProcessor{
				telegramDiscussPolicy: tt.reader,
				discussSample:         func() float64 { return tt.sample },
			}
			got, rate, _, _ := processor.shouldNotifyDiscuss(context.Background(), "bot-1", msg, false)
			if got != tt.want {
				t.Fatalf("shouldNotifyDiscuss() = %v, want %v", got, tt.want)
			}
			if rate != tt.wantRate {
				t.Fatalf("sample rate = %v, want %v", rate, tt.wantRate)
			}
		})
	}
}

func TestShouldNotifyDiscussKeywordForcesReply(t *testing.T) {
	processor := &ChannelInboundProcessor{
		telegramDiscussPolicy: fakeTelegramDiscussPolicyReader{policy: TelegramDiscussPolicy{
			PassiveSampleRate:  0,
			ForceReplyKeywords: []string{"小雪", "HELLO"},
		}},
		discussSample: func() float64 { return 0.99 },
	}

	for _, text := range []string{"小雪在吗", "say hello please"} {
		msg := channel.InboundMessage{
			Channel:      channel.ChannelTypeTelegram,
			Message:      channel.Message{Text: text},
			Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
		}
		notify, rate, forceReply, _ := processor.shouldNotifyDiscuss(context.Background(), "bot-1", msg, false)
		if !notify || !forceReply {
			t.Fatalf("message %q notify/forceReply = %v/%v, want true/true", text, notify, forceReply)
		}
		if rate != 0 {
			t.Fatalf("message %q rate = %v, want 0", text, rate)
		}
	}

	msg := channel.InboundMessage{
		Channel:      channel.ChannelTypeTelegram,
		Message:      channel.Message{Text: "ordinary chatter"},
		Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
	}
	notify, _, forceReply, _ := processor.shouldNotifyDiscuss(context.Background(), "bot-1", msg, false)
	if notify || forceReply {
		t.Fatalf("non-matching message notify/forceReply = %v/%v, want false/false", notify, forceReply)
	}
}

func TestShouldNotifyDiscussDirectedTelegramForcesReply(t *testing.T) {
	processor := &ChannelInboundProcessor{discussSample: func() float64 { return 0.99 }}
	tests := []channel.InboundMessage{
		{
			Channel:      channel.ChannelTypeTelegram,
			Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
			Metadata:     map[string]any{"is_mentioned": true},
		},
		{
			Channel:      channel.ChannelTypeTelegram,
			Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
			Metadata:     map[string]any{"is_reply_to_bot": true},
		},
		{
			Channel:      channel.ChannelTypeTelegram,
			Conversation: channel.Conversation{Type: channel.ConversationTypePrivate},
		},
	}
	for _, msg := range tests {
		directed := shouldTriggerAssistantResponse(msg)
		notify, _, forceReply, _ := processor.shouldNotifyDiscuss(context.Background(), "bot-1", msg, directed)
		if !notify || !forceReply {
			t.Fatalf("directed message notify/forceReply = %v/%v, want true/true", notify, forceReply)
		}
	}
}

func TestShouldNotifyDiscussDirectedTelegramStillLoadsFallbackPolicy(t *testing.T) {
	processor := &ChannelInboundProcessor{
		telegramDiscussPolicy: fakeTelegramDiscussPolicyReader{policy: TelegramDiscussPolicy{
			PassiveSampleRate:   0,
			SendFallbackEnabled: true,
		}},
	}
	msg := channel.InboundMessage{
		Channel:      channel.ChannelTypeTelegram,
		Conversation: channel.Conversation{Type: channel.ConversationTypeGroup},
		Metadata:     map[string]any{"is_mentioned": true},
	}
	notify, _, forceReply, fallback := processor.shouldNotifyDiscuss(
		context.Background(), "bot-1", msg, shouldTriggerAssistantResponse(msg),
	)
	if !notify || !forceReply || !fallback {
		t.Fatalf("directed policy = notify %v, force %v, fallback %v", notify, forceReply, fallback)
	}
}

func TestTelegramDiscussSamplingMissThenHitIncludesAccumulatedContext(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channel-identity-1"},
	}
	chatSvc := &fakeChatService{
		resolveResult: route.ResolveConversationResult{BotID: "bot-1", RouteID: "route-1"},
	}
	turns := make(chan turn.StartTurnCommand, 1)
	gateway := &fakeChatGateway{
		onChat: func(cmd turn.StartTurnCommand) {
			turns <- cmd
		},
	}
	driver := discuss.NewDiscussDriver(discuss.DiscussDriverDeps{Turn: gateway})
	defer driver.StopAll()

	processor := NewChannelInboundProcessor(
		slog.Default(),
		nil,
		chatSvc,
		chatSvc,
		gateway,
		channelIdentitySvc,
		&fakePolicyService{},
		"",
		0,
	)
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{
		ID:      "session-1",
		Type:    sessionpkg.TypeDiscuss,
		Runtime: sessionpkg.RuntimeModel,
	}})
	processor.SetPipeline(timeline.NewPipeline(timeline.RenderParams{}), nil, driver)

	samples := []float64{0.90, 0.10}
	sampleCalls := 0
	processor.discussSample = func() float64 {
		value := samples[sampleCalls]
		sampleCalls++
		return value
	}

	cfg := channel.ChannelConfig{
		TeamID:      "team-1",
		ID:          "config-1",
		BotID:       "bot-1",
		ChannelType: channel.ChannelTypeTelegram,
	}
	message := func(id, text string) channel.InboundMessage {
		return channel.InboundMessage{
			BotID:       "bot-1",
			Channel:     channel.ChannelTypeTelegram,
			Message:     channel.Message{ID: id, Text: text},
			ReplyTarget: "telegram-group-1",
			Sender:      channel.Identity{SubjectID: "telegram-user-1", DisplayName: "Alice"},
			Conversation: channel.Conversation{
				ID:   "telegram-group-1",
				Type: channel.ConversationTypeGroup,
				Name: "Test Group",
			},
		}
	}

	if err := processor.HandleInbound(context.Background(), cfg, message("msg-1", "first message"), &fakeReplySender{}); err != nil {
		t.Fatalf("first HandleInbound() error = %v", err)
	}
	if driver.HasSession("session-1") {
		t.Fatal("sampling miss unexpectedly notified discuss driver")
	}
	select {
	case cmd := <-turns:
		t.Fatalf("sampling miss unexpectedly started turn: %+v", cmd)
	default:
	}

	if err := processor.HandleInbound(context.Background(), cfg, message("msg-2", "second message"), &fakeReplySender{}); err != nil {
		t.Fatalf("second HandleInbound() error = %v", err)
	}

	var cmd turn.StartTurnCommand
	select {
	case cmd = <-turns:
	case <-time.After(time.Second):
		t.Fatal("sampling hit did not start discuss turn")
	}

	if sampleCalls != 2 {
		t.Fatalf("sample calls = %d, want 2", sampleCalls)
	}
	var contextText strings.Builder
	for _, msg := range cmd.DiscussMessages {
		contextText.WriteString(msg.Content)
		contextText.WriteByte('\n')
	}
	fullContext := contextText.String()
	first := strings.Index(fullContext, `id="msg-1"`)
	second := strings.Index(fullContext, `id="msg-2"`)
	if first < 0 || second < 0 {
		t.Fatalf("accumulated context is missing messages:\n%s", fullContext)
	}
	if first >= second {
		t.Fatalf("accumulated context is out of order:\n%s", fullContext)
	}
	if len(chatSvc.persisted) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(chatSvc.persisted))
	}
}
