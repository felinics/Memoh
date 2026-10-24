package inbound

import (
	"context"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/discord"
	"github.com/felinics/memoh/internal/channel/adapters/feishu"
	"github.com/felinics/memoh/internal/channel/adapters/line"
	"github.com/felinics/memoh/internal/channel/adapters/matrix"
	"github.com/felinics/memoh/internal/channel/adapters/misskey"
	"github.com/felinics/memoh/internal/channel/adapters/slack"
	"github.com/felinics/memoh/internal/channel/adapters/telegram"
	"github.com/felinics/memoh/internal/channel/discuss"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// policyOnlyAdapter is a real adapter's self-identity policy and nothing else,
// so resolving a sender neither looks the sender up on the platform nor
// depends on anything but what the policy declares.
type policyOnlyAdapter struct {
	channel.Adapter
}

func (a policyOnlyAdapter) SelfIdentityPolicy() channel.SelfIdentityPolicy {
	return a.Adapter.(channel.SelfIdentityPolicyProvider).SelfIdentityPolicy()
}

// ownAccountCase is one platform: what the adapter's DiscoverSelf records for
// the bot, and the sender the adapter's inbound code fills in for a message
// from the bot's own account and for one from somebody else.
type ownAccountCase struct {
	name    string
	adapter channel.Adapter
	self    map[string]any
	bot     channel.Identity
	other   channel.Identity
}

func ownAccountCases() []ownAccountCase {
	return []ownAccountCase{
		{
			name:    "telegram",
			adapter: telegram.NewTelegramAdapter(nil),
			self:    map[string]any{"user_id": "777000", "username": "memohbot"},
			bot:     channel.Identity{SubjectID: "777000", Attributes: map[string]string{"chat_id": "-1001", "user_id": "777000", "username": "memohbot"}},
			other:   channel.Identity{SubjectID: "42", Attributes: map[string]string{"chat_id": "-1001", "user_id": "42", "username": "alice"}},
		},
		{
			name:    "feishu",
			adapter: feishu.NewFeishuAdapter(nil),
			self:    map[string]any{"open_id": "ou_bot"},
			bot:     channel.Identity{SubjectID: "ou_bot", Attributes: map[string]string{"open_id": "ou_bot", "user_id": "u_bot"}},
			other:   channel.Identity{SubjectID: "ou_alice", Attributes: map[string]string{"open_id": "ou_alice", "user_id": "u_alice"}},
		},
		{
			name:    "discord",
			adapter: discord.NewDiscordAdapter(nil),
			self:    map[string]any{"user_id": "1100", "username": "memoh"},
			bot:     channel.Identity{SubjectID: "1100", Attributes: map[string]string{"user_id": "1100", "username": "memoh"}},
			other:   channel.Identity{SubjectID: "2200", Attributes: map[string]string{"user_id": "2200", "username": "alice"}},
		},
		{
			name:    "slack",
			adapter: slack.NewSlackAdapter(nil),
			self:    map[string]any{"user_id": "U0BOT", "bot_id": "B0BOT", "team_id": "T1"},
			bot:     channel.Identity{SubjectID: "U0BOT", Attributes: map[string]string{"user_id": "U0BOT"}},
			other:   channel.Identity{SubjectID: "U0ALICE", Attributes: map[string]string{"user_id": "U0ALICE"}},
		},
		{
			name:    "matrix",
			adapter: matrix.NewMatrixAdapter(nil),
			self:    map[string]any{"user_id": "@bot:example.org"},
			bot:     channel.Identity{SubjectID: "@bot:example.org", Attributes: map[string]string{"user_id": "@bot:example.org", "room_id": "!room:example.org"}},
			other:   channel.Identity{SubjectID: "@alice:example.org", Attributes: map[string]string{"user_id": "@alice:example.org", "room_id": "!room:example.org"}},
		},
		{
			name:    "misskey",
			adapter: misskey.NewMisskeyAdapter(nil),
			self:    map[string]any{"user_id": "9abc", "username": "memohbot"},
			bot:     channel.Identity{SubjectID: "9abc", Attributes: map[string]string{"user_id": "9abc", "username": "memohbot"}},
			other:   channel.Identity{SubjectID: "9xyz", Attributes: map[string]string{"user_id": "9xyz", "username": "alice"}},
		},
		{
			// LINE records the bot under bot_user_id and reports senders as
			// user_id; both are the same LINE user id.
			name:    "line",
			adapter: line.NewAdapter(nil),
			self:    map[string]any{"bot_user_id": "U0123456789abcdef0123456789abcdef", "basic_id": "@memoh"},
			bot:     channel.Identity{SubjectID: "U0123456789abcdef0123456789abcdef", Attributes: map[string]string{"user_id": "U0123456789abcdef0123456789abcdef"}},
			other:   channel.Identity{SubjectID: "Ufedcba9876543210fedcba9876543210", Attributes: map[string]string{"user_id": "Ufedcba9876543210fedcba9876543210"}},
		},
	}
}

func newOwnAccountResolver(t *testing.T, tc ownAccountCase) (*IdentityResolver, *fakeChannelIdentityService) {
	t.Helper()
	registry := channel.NewRegistry()
	registry.MustRegister(policyOnlyAdapter{Adapter: tc.adapter})
	identitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "ci-1"}}
	return NewIdentityResolver(nil, registry, identitySvc, &fakePolicyService{}, ""), identitySvc
}

func ownAccountMessage(tc ownAccountCase, sender channel.Identity) channel.InboundMessage {
	return channel.InboundMessage{
		BotID:        "bot-1",
		Channel:      tc.adapter.Type(),
		Message:      channel.Message{ID: "m1", Text: "hello"},
		ReplyTarget:  "target-1",
		Sender:       sender,
		Conversation: channel.Conversation{ID: "conv-1", Type: channel.ConversationTypeGroup},
	}
}

// A message from the account the bot runs as is dropped before the sender
// is given an identity, on every platform that records an id for it.
func TestResolveStopsMessagesFromTheBotsOwnAccount(t *testing.T) {
	t.Parallel()
	for _, tc := range ownAccountCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolver, identitySvc := newOwnAccountResolver(t, tc)
			cfg := channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: tc.adapter.Type(), SelfIdentity: tc.self}

			state, err := resolver.Resolve(context.Background(), cfg, ownAccountMessage(tc, tc.bot))
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if state.Decision == nil || !state.Decision.Stop || !state.Decision.Reply.IsEmpty() {
				t.Fatalf("Decision = %+v, want a silent stop", state.Decision)
			}
			if identitySvc.calls != 0 || state.Identity.ChannelIdentityID != "" {
				t.Fatalf("the bot's own account was given an identity (%d lookups, id %q)", identitySvc.calls, state.Identity.ChannelIdentityID)
			}

			state, err = resolver.Resolve(context.Background(), cfg, ownAccountMessage(tc, tc.other))
			if err != nil {
				t.Fatalf("Resolve(other sender) error = %v", err)
			}
			if state.Decision != nil || state.Identity.ChannelIdentityID != "ci-1" {
				t.Fatalf("another sender: Decision = %+v, identity %q, want resolved and not stopped", state.Decision, state.Identity.ChannelIdentityID)
			}
		})
	}
}

// A config that has not recorded the bot's id has nothing to compare with, so
// no sender is taken for the bot.
func TestResolveWithoutRecordedSelfIdentityStopsNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range ownAccountCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resolver, _ := newOwnAccountResolver(t, tc)
			cfg := channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: tc.adapter.Type(), SelfIdentity: map[string]any{}}

			state, err := resolver.Resolve(context.Background(), cfg, ownAccountMessage(tc, tc.bot))
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if state.Decision != nil {
				t.Fatalf("Decision = %+v, want none without a recorded id", state.Decision)
			}
		})
	}
}

// The bot's own message, delivered back by the platform, is not input to a
// discuss session: it opens no session on the driver, adds nothing to the
// timeline and persists no row. Whoever speaks next is still taken in.
func TestHandleInboundDiscussDropsTheBotsOwnMessage(t *testing.T) {
	tc := ownAccountCases()[0]
	registry := channel.NewRegistry()
	registry.MustRegister(policyOnlyAdapter{Adapter: tc.adapter})
	identitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "ci-1"}}
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "bot-1", RouteID: "route-1"}}
	gateway := &fakeChatGateway{}
	const sessionID = "discuss-session"
	processor := NewChannelInboundProcessor(slog.Default(), registry, chatSvc, chatSvc, gateway, identitySvc, &fakePolicyService{}, "", 0)
	processor.SetSessionEnsurer(&fakeSessionEnsurer{activeSession: SessionResult{ID: sessionID, Type: sessionpkg.TypeDiscuss}})
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	driver := discuss.NewDiscussDriver(discuss.DiscussDriverDeps{})
	defer driver.StopAll()
	processor.SetPipeline(pipeline, nil, driver)
	sender := &fakeReplySender{}
	cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: tc.adapter.Type(), SelfIdentity: tc.self}

	if err := processor.HandleInbound(context.Background(), cfg, ownAccountMessage(tc, tc.bot), sender); err != nil {
		t.Fatalf("HandleInbound(bot's own message) error = %v", err)
	}
	if driver.HasSession(sessionID) || pipeline.HasSession(sessionID) {
		t.Fatalf("the bot's own message reached the discuss session (driver %v, timeline %v)", driver.HasSession(sessionID), pipeline.HasSession(sessionID))
	}
	if identitySvc.calls != 0 || len(chatSvc.persistedIn) != 0 || len(sender.sent) != 0 || gateway.gotReq.Query != "" {
		t.Fatalf("the bot's own message had effects: identity lookups %d, persisted %d, sent %d, query %q",
			identitySvc.calls, len(chatSvc.persistedIn), len(sender.sent), gateway.gotReq.Query)
	}

	if err := processor.HandleInbound(context.Background(), cfg, ownAccountMessage(tc, tc.other), sender); err != nil {
		t.Fatalf("HandleInbound(other sender) error = %v", err)
	}
	if !driver.HasSession(sessionID) || !pipeline.HasSession(sessionID) {
		t.Fatalf("another sender's message did not reach the discuss session (driver %v, timeline %v)", driver.HasSession(sessionID), pipeline.HasSession(sessionID))
	}
}
