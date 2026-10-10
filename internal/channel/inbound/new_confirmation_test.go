package inbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/command"
	"github.com/felinics/memoh/internal/errlog"
	"github.com/felinics/memoh/internal/i18n"
	"github.com/felinics/memoh/internal/slash"
)

func mustCommandInvocation(t *testing.T, text string) command.Invocation {
	t.Helper()
	invocation, err := command.ParseInvocation(command.InvocationInput{Text: text, Directed: true})
	if err != nil {
		t.Fatalf("ParseInvocation(%q) error = %v", text, err)
	}
	return invocation
}

type testACPProfiles struct{}

func (testACPProfiles) ResolveACPProfile(agentID string) turn.ACPAgentProfile {
	agentID = strings.ToLower(strings.TrimSpace(agentID))
	names := map[string]string{
		"custom-agent": "Custom Agent",
	}
	displayName, ok := names[agentID]
	return turn.ACPAgentProfile{
		ID:          agentID,
		DisplayName: displayName,
		Known:       ok,
	}
}

func (testACPProfiles) ResolveACPSetupPreflight(_ context.Context, _, _, agentID string, metadata map[string]any) (turn.ACPSetupPreflight, error) {
	acp, _ := metadata["acp"].(map[string]any)
	agents, _ := acp["agents"].(map[string]any)
	config, _ := agents[strings.ToLower(strings.TrimSpace(agentID))].(map[string]any)
	enabled, _ := config["enabled"].(bool)
	result := turn.ACPSetupPreflight{Enabled: enabled}
	mode, modeSet := config["setup_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if !modeSet || mode == "" || mode == "self" {
		return result, nil
	}
	managed, _ := config["managed"].(map[string]any)
	if value, _ := managed["api_key"].(string); strings.TrimSpace(value) == "" {
		result.MissingManagedField = &turn.ACPManagedField{ID: "api_key", Label: "API key"}
	}
	return result, nil
}

const (
	testACPBotAgentID   = "aaaaaaaa-0000-0000-0000-000000000001"
	testCodexBotAgentID = "aaaaaaaa-0000-0000-0000-000000000002"
)

type fakeBotAgentReader []BotAgent

func (f fakeBotAgentReader) BotAgents(context.Context, string) ([]BotAgent, error) {
	return f, nil
}

type unreadableBotAgents struct{}

func (unreadableBotAgents) BotAgents(context.Context, string) ([]BotAgent, error) {
	return nil, errors.New("the bot's agents were read")
}

var testBotAgents = fakeBotAgentReader{
	{ID: testACPBotAgentID, Name: "Custom Agent", Runtime: "acp", ACPAgentID: "custom-agent", Enabled: true},
	{ID: testCodexBotAgentID, Name: "Codex", Runtime: sessionpkg.RuntimeCodex, Enabled: true},
	{ID: "aaaaaaaa-0000-0000-0000-000000000003", Name: "Retired", Runtime: sessionpkg.RuntimeCodex},
}

func resolveNewSessionTypeForTest(t *testing.T, text string, msg channel.InboundMessage) (string, error) {
	t.Helper()
	spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, text).Parsed, msg, testBotAgents)
	if err != nil {
		return "", err
	}
	return spec.Type, nil
}

// TestResolveNewSessionType_BareConfirmFlag guards the hand-typed "/new
// --confirm" edge: extractFlags doesn't recognize --confirm, so it lands as the
// first positional (the mode slot). It must NOT be read as a session type —
// resolveNewSessionType should fall through to context defaults exactly like a
// bare "/new", not error with `unknown session type "--confirm"`.
func TestResolveNewSessionType_BareConfirmFlag(t *testing.T) {
	msg := channel.InboundMessage{Channel: channel.ChannelTypeTelegram}

	bare, errBare := resolveNewSessionTypeForTest(t, "/new", msg)
	if errBare != nil {
		t.Fatalf("/new returned error: %v", errBare)
	}
	withFlag, err := resolveNewSessionTypeForTest(t, "/new --confirm", msg)
	if err != nil {
		t.Fatalf("/new --confirm should not error, got: %v", err)
	}
	if withFlag != bare {
		t.Errorf("/new --confirm resolved to %q, want same as bare /new (%q)", withFlag, bare)
	}
	// Explicit modes must still resolve normally.
	if got, err := resolveNewSessionTypeForTest(t, "/new chat", msg); err != nil || got != sessionpkg.TypeChat {
		t.Errorf("/new chat = (%q, %v), want (%q, nil)", got, err, sessionpkg.TypeChat)
	}
	if got, err := resolveNewSessionTypeForTest(t, "/new discuss", msg); err != nil || got != sessionpkg.TypeDiscuss {
		t.Errorf("/new discuss = (%q, %v), want (%q, nil)", got, err, sessionpkg.TypeDiscuss)
	}
}

// /new names an Agent by its name, whatever the case, and the session is
// bound to that Agent rather than to its kind.
func TestResolveNewSessionSpecNamesAgent(t *testing.T) {
	dm := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "private"}}
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "group"}}

	cases := []struct {
		cmd         string
		msg         channel.InboundMessage
		wantMode    string
		wantRuntime string
		wantType    string
		wantAgentID string
	}{
		{"/new custom agent", dm, sessionpkg.TypeChat, sessionpkg.RuntimeACPAgent, sessionpkg.TypeACPAgent, testACPBotAgentID},
		{"/new chat Custom  Agent", dm, sessionpkg.TypeChat, sessionpkg.RuntimeACPAgent, sessionpkg.TypeACPAgent, testACPBotAgentID},
		{"/new discuss custom agent", group, sessionpkg.TypeDiscuss, sessionpkg.RuntimeACPAgent, sessionpkg.TypeDiscuss, testACPBotAgentID},
		{"/new custom agent", group, sessionpkg.TypeDiscuss, sessionpkg.RuntimeACPAgent, sessionpkg.TypeDiscuss, testACPBotAgentID},
		{"/new CODEX", dm, sessionpkg.TypeChat, sessionpkg.RuntimeCodex, sessionpkg.TypeChat, testCodexBotAgentID},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, tc.cmd).Parsed, tc.msg, testBotAgents)
			if err != nil {
				t.Fatalf("resolveNewSessionSpec(%q) error = %v", tc.cmd, err)
			}
			if spec.Mode != tc.wantMode || spec.Runtime != tc.wantRuntime || spec.Type != tc.wantType || spec.BotAgentID != tc.wantAgentID {
				t.Fatalf("spec = %#v, want mode/runtime/type/agent %q/%q/%q/%q", spec, tc.wantMode, tc.wantRuntime, tc.wantType, tc.wantAgentID)
			}
		})
	}
}

// A kind is not a name: with no Agent called "acp" the operand matches
// nothing, and a disabled Agent is refused rather than reported missing.
func TestResolveNewSessionSpecRejectsUnusableAgent(t *testing.T) {
	dm := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "private"}}

	for _, cmd := range []string{"/new acp", "/new chat --id aaaaaaaa-0000-0000-0000-00000000dead"} {
		if _, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, cmd).Parsed, dm, testBotAgents); !errors.Is(err, errNewSessionAgentNotFound) {
			t.Fatalf("resolveNewSessionSpec(%q) error = %v, want agent not found", cmd, err)
		}
	}
	_, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new retired").Parsed, dm, testBotAgents)
	if got := apperror.CodeOf(err); got != apperror.CodeACPAgentNotEnabled {
		t.Fatalf("disabled agent code = %q, want %s", got, apperror.CodeACPAgentNotEnabled)
	}
}

// Words that begin with a mode are tried as one name first, so "Chat GPT" is
// reachable; a mode word on its own stays the mode even when an Agent has it
// for a name.
func TestResolveNewSessionSpecTriesWholeNameBeforeModeWord(t *testing.T) {
	agents := []BotAgent{
		{ID: testCodexBotAgentID, Name: "Chat GPT", Runtime: sessionpkg.RuntimeCodex, Enabled: true},
		{ID: testACPBotAgentID, Name: "chat", Runtime: "acp", ACPAgentID: "custom-agent", Enabled: true},
	}
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "group"}}

	spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new chat gpt").Parsed, group, agents)
	if err != nil || spec.BotAgentID != testCodexBotAgentID || spec.Mode != sessionpkg.TypeDiscuss {
		t.Fatalf("/new chat gpt = %#v, %v, want the Agent in the group's default mode", spec, err)
	}
	spec, err = resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new chat").Parsed, group, agents)
	if err != nil || spec.BotAgentID != "" || spec.Mode != sessionpkg.TypeChat {
		t.Fatalf("/new chat = %#v, %v, want chat mode with no Agent named", spec, err)
	}
}

func TestResolveNewSessionSpecCanonicalBotMentionIsNotAnAgent(t *testing.T) {
	t.Parallel()
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: channel.ConversationTypeGroup}}

	for _, text := range []string{
		"/new @memoh1bot",
		"/new discuss @memoh1bot",
		"/new @alice @memoh1bot",
		"/new discuss @alice @memoh1bot",
		"@memoh1bot /new discuss",
		"/new@memoh1bot discuss",
		"/new discuss@memoh1bot",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			invocation, err := command.ParseInvocation(command.InvocationInput{Text: text, BotAliases: []string{"memoh1bot"}})
			if err != nil {
				t.Fatalf("ParseInvocation() error = %v", err)
			}
			spec, err := resolveNewSessionSpecParsed(invocation.Parsed, group, testBotAgents)
			if err != nil {
				t.Fatalf("resolveNewSessionSpecParsed() error = %v", err)
			}
			if spec.Mode != sessionpkg.TypeDiscuss || spec.Runtime != sessionpkg.RuntimeModel || spec.BotAgentID != "" {
				t.Fatalf("spec = %#v, want native discuss session", spec)
			}
		})
	}
}

func TestChannelSlashAliasesExcludeSenderAndReplyTarget(t *testing.T) {
	t.Parallel()
	aliases := channelSlashAliases(channel.InboundMessage{
		BotID:       "bot-1",
		ReplyTarget: "group-1",
		Metadata: map[string]any{
			"bot_username": "memoh1bot",
			"bot_aliases":  []string{"@memoh:example.com", "@memoh"},
		},
	}, InboundIdentity{BotID: "bot-1", DisplayName: "Alice"})
	joined := strings.Join(aliases, ",")
	if strings.Contains(joined, "Alice") || strings.Contains(joined, "group-1") {
		t.Fatalf("aliases = %#v, sender and reply target must not address the bot", aliases)
	}
	if !strings.Contains(joined, "memoh1bot") {
		t.Fatalf("aliases = %#v, want adapter-provided bot username", aliases)
	}
	seenAliases := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		seenAliases[alias] = true
	}
	if !seenAliases["memoh:example.com"] || !seenAliases["memoh"] {
		t.Fatalf("aliases = %#v, want adapter-provided alias list", aliases)
	}
}

func TestClassifySlackAppMentionCommandUsesAdapterBotAlias(t *testing.T) {
	t.Parallel()
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelTypeSlack,
		Conversation: channel.Conversation{
			ID:   "C123",
			Type: channel.ConversationTypeGroup,
		},
		Metadata: map[string]any{
			"bot_alias":    "UBOT",
			"is_mentioned": true,
		},
	}
	decision := (&ChannelInboundProcessor{}).classifyChannelSlash("<@UBOT> /new discuss", msg, InboundIdentity{BotID: "bot-1"})
	if decision.Kind != slash.DecisionCommandAction || !decision.Directed || decision.Invocation == nil {
		t.Fatalf("decision = %#v, want directed Slack command", decision)
	}
	if decision.Invocation.CommandText != "/new discuss" {
		t.Fatalf("command text = %q, want /new discuss", decision.Invocation.CommandText)
	}
}

func TestClassifyMatrixLocalpartMentionUsesAdapterAliases(t *testing.T) {
	t.Parallel()
	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelTypeMatrix,
		Conversation: channel.Conversation{
			ID:   "!room:example.com",
			Type: channel.ConversationTypeGroup,
		},
		Metadata: map[string]any{
			"bot_alias":    "@memoh:example.com",
			"bot_aliases":  []string{"@memoh:example.com", "@memoh"},
			"is_mentioned": true,
		},
	}
	decision := (&ChannelInboundProcessor{}).classifyChannelSlash("@memoh /new discuss", msg, InboundIdentity{BotID: "bot-1"})
	if decision.Kind != slash.DecisionCommandAction || !decision.Directed || decision.Invocation == nil {
		t.Fatalf("decision = %#v, want directed Matrix command", decision)
	}
	if decision.Invocation.CommandText != "/new discuss" {
		t.Fatalf("command text = %q, want /new discuss", decision.Invocation.CommandText)
	}
}

func TestHandleInboundNewCommandIgnoresCurrentBotMentionArguments(t *testing.T) {
	for _, text := range []string{"/new @memoh1bot", "/new discuss @memoh1bot", "/new discuss@memoh1bot"} {
		t.Run(text, func(t *testing.T) {
			channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channel-identity-1"}}
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "route-1"}}
			gateway := &fakeChatGateway{}
			ensurer := &fakeSessionEnsurer{}
			processor := NewChannelInboundProcessor(slog.Default(), nil, chatSvc, chatSvc, gateway, channelIdentitySvc, &fakePolicyService{}, "", 0)
			processor.SetACLService(&fakeChatACL{allowed: true})
			processor.SetSessionEnsurer(ensurer)
			processor.SetCommandHandler(command.NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
			sender := &fakeReplySender{}

			msg := channel.InboundMessage{
				BotID:       "bot-1",
				Channel:     channel.ChannelTypeTelegram,
				Message:     channel.Message{ID: "msg-1", Text: text},
				ReplyTarget: "group-1",
				Sender:      channel.Identity{SubjectID: "user-1", DisplayName: "Alice"},
				Conversation: channel.Conversation{
					ID:   "group-1",
					Type: channel.ConversationTypeGroup,
				},
				Metadata: map[string]any{
					"raw_text":     text,
					"is_mentioned": true,
					"bot_username": "memoh1bot",
				},
			}
			cfg := channel.ChannelConfig{TeamID: "team-test", ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelTypeTelegram}
			if err := processor.HandleInbound(context.Background(), cfg, msg, sender); err != nil {
				t.Fatalf("HandleInbound() error = %v", err)
			}
			if ensurer.lastSpec.Mode != sessionpkg.TypeDiscuss || ensurer.lastSpec.Runtime != sessionpkg.RuntimeModel || acpNewSessionAgentID(ensurer.lastSpec) != "" {
				t.Fatalf("created spec = %#v, want native discuss session", ensurer.lastSpec)
			}
		})
	}
}

func TestResolveNewSessionSpec_GroupChatACPUnsupported(t *testing.T) {
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "group"}}
	_, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new chat codex").Parsed, group, testBotAgents)
	if err == nil {
		t.Fatal("resolveNewSessionSpec error = nil, want group chat ACP unsupported")
	}
	if got := apperror.CodeOf(err); got != apperror.CodeGroupChatACPUnsupported {
		t.Fatalf("code = %q, want %s", got, apperror.CodeGroupChatACPUnsupported)
	}
}

func TestCurrentContextForNewSessionSpecUsesACPDisplayName(t *testing.T) {
	cc := currentContextForNewSessionSpec(command.CurrentContext{ChatModel: "gpt-4.1"}, NewSessionSpec{
		Runtime: sessionpkg.RuntimeACPAgent,
		Metadata: map[string]any{
			"acp_agent_id": "custom-agent",
		},
	}, testACPProfiles{})
	if cc.ChatModel != "Custom Agent / ACP" {
		t.Fatalf("ChatModel = %q, want ACP display label", cc.ChatModel)
	}
}

func TestHandleNewSessionCommandCreatesACPChatSpec(t *testing.T) {
	ownerID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	channelIdentityID := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: true},
		acpProfiles:       testACPProfiles{},
		botAgents:         testBotAgents,
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new chat custom agent"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: channelIdentityID,
		UserID:            ownerID,
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	spec := ensurer.lastSpec
	if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeACPAgent || spec.Type != sessionpkg.TypeACPAgent {
		t.Fatalf("spec = %#v, want chat/acp_agent/acp_agent", spec)
	}
	if spec.RuntimeOwnerAccountID != ownerID {
		t.Fatalf("runtime owner = %q, want authenticated channel identity", spec.RuntimeOwnerAccountID)
	}
	if spec.CreatedByUserID != ownerID {
		t.Fatalf("created_by_user_id = %q, want authenticated channel identity", spec.CreatedByUserID)
	}
	if got := newSessionMetadataString(spec.Metadata, "acp_agent_id"); got != "custom-agent" || spec.BotAgentID != testACPBotAgentID {
		t.Fatalf("agent = %q bound to %q, want custom-agent bound to the named Agent", got, spec.BotAgentID)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent replies = %d, want 1", len(sender.sent))
	}
}

// A name that matches nothing is answered with the Agents to choose from and
// creates no session; it is the sender's typo, not a failure. A sender who may
// not run an Agent is refused without learning their names.
func TestHandleNewSessionCommandListsAgentsForUnknownName(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		ensurer := &fakeSessionEnsurer{}
		p := &ChannelInboundProcessor{
			sessionEnsurer:    ensurer,
			botAgents:         testBotAgents,
			permissionChecker: &fakeBotPermissionChecker{allowed: allowed},
		}
		sender := &fakeReplySender{}
		msg := channel.InboundMessage{
			Channel:      channel.ChannelTypeTelegram,
			Message:      channel.Message{ID: "msg-1", Text: "/new grok"},
			ReplyTarget:  "target-1",
			Conversation: channel.Conversation{ID: "dm-1", Type: channel.ConversationTypePrivate},
		}

		err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
			BotID:  "bot-1",
			UserID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		}, mustCommandInvocation(t, msg.Message.PlainText()))
		if ensurer.lastSpec.Mode != "" || len(sender.sent) != 1 {
			t.Fatalf("allowed=%v: spec = %#v, replies = %d, want one reply and no session", allowed, ensurer.lastSpec, len(sender.sent))
		}
		text := sender.sent[0].Message.PlainText()
		listed := strings.Contains(text, "Custom Agent") && strings.Contains(text, "Codex") && !strings.Contains(text, "Retired")
		if allowed && (err != nil || !listed) {
			t.Fatalf("reply = %q, error = %v, want the enabled agents and an answered command", text, err)
		}
		if !allowed && (apperror.CodeOf(err) != apperror.CodeNoWorkspaceExec || strings.Contains(text, "Codex")) {
			t.Fatalf("reply = %q, error = %v, want a refusal that names no agent", text, err)
		}
	}
}

func TestHandleNewSessionCommandCreatesNativeSessionWithCreator(t *testing.T) {
	creatorID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{}
	p := &ChannelInboundProcessor{
		routeResolver:  chatSvc,
		sessionEnsurer: ensurer,
		// A /new that names no Agent must not depend on the bot's Agents.
		botAgents: unreadableBotAgents{},
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new chat"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
		UserID:            creatorID,
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	spec := ensurer.lastSpec
	if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeModel || spec.Type != sessionpkg.TypeChat {
		t.Fatalf("spec = %#v, want native chat session", spec)
	}
	if spec.CreatedByUserID != creatorID {
		t.Fatalf("created_by_user_id = %q, want authenticated channel identity", spec.CreatedByUserID)
	}
	if spec.RuntimeOwnerAccountID != "" {
		t.Fatalf("runtime owner = %q, want empty for native session", spec.RuntimeOwnerAccountID)
	}
}

func TestHandleNewSessionCommandCancelsActiveStream(t *testing.T) {
	routeID := "11111111-1111-1111-1111-111111111111"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: routeID}}
	ensurer := &fakeSessionEnsurer{}
	p := &ChannelInboundProcessor{
		routeResolver:  chatSvc,
		sessionEnsurer: ensurer,
	}
	cancelled := make(chan struct{})
	p.activeStreams.Store("bot-1:"+routeID, context.CancelFunc(func() { close(cancelled) }))

	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new --confirm"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID: "bot-1",
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("/new did not cancel the active stream for the route")
	}
	if _, ok := p.activeStreams.Load("bot-1:" + routeID); ok {
		t.Fatal("active stream remained registered after /new")
	}
}

func TestHandleNewSessionCommandBareNewInheritsDefaultACP(t *testing.T) {
	ownerID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: true},
		acpProfiles:       testACPProfiles{},
		defaultChatRuntime: fakeDefaultChatRuntimeReader{settings: DefaultChatRuntimeSettings{
			Runtime:     sessionpkg.RuntimeACPAgent,
			ACPAgentID:  "custom-agent",
			ProjectPath: "/workspace",
			ProjectMode: sessionpkg.DefaultACPProjectMode,
		}},
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
		UserID:            ownerID,
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	spec := ensurer.lastSpec
	if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeACPAgent || spec.Type != sessionpkg.TypeACPAgent {
		t.Fatalf("spec = %#v, want default chat ACP", spec)
	}
	if got := newSessionMetadataString(spec.Metadata, "acp_agent_id"); got != "custom-agent" {
		t.Fatalf("agent = %q, want custom-agent", got)
	}
	if got := newSessionMetadataString(spec.Metadata, "project_path"); got != "/workspace" {
		t.Fatalf("project_path = %q, want /workspace", got)
	}
}

func TestHandleNewSessionCommandExplicitACPInheritsDefaultProject(t *testing.T) {
	ownerID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: true},
		acpProfiles:       testACPProfiles{},
		defaultChatRuntime: fakeDefaultChatRuntimeReader{settings: DefaultChatRuntimeSettings{
			BotAgentID:  testACPBotAgentID,
			Runtime:     sessionpkg.RuntimeACPAgent,
			ACPAgentID:  "custom-agent",
			ProjectPath: "/workspace/default",
			ProjectMode: sessionpkg.DefaultACPProjectMode,
		}},
		botAgents: testBotAgents,
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new custom agent"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
		UserID:            ownerID,
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	spec := ensurer.lastSpec
	if got := newSessionMetadataString(spec.Metadata, "project_path"); got != "/workspace/default" {
		t.Fatalf("project_path = %q, want bot default", got)
	}
}

func TestHandleNewSessionCommandPreflightsACPSetup(t *testing.T) {
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: true},
		acpProfiles:       testACPProfiles{},
		acpAgentSetup: fakeACPAgentSetupReader{metadata: map[string]any{
			"acp": map[string]any{
				"agents": map[string]any{
					"custom-agent": map[string]any{
						"enabled":    true,
						"setup_mode": "api_key",
						"managed":    map[string]any{},
					},
				},
			},
		}},
		botAgents: testBotAgents,
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new custom agent"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
		UserID:            "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if apperror.CodeOf(err) != apperror.CodeACPAgentNotConfigured {
		t.Fatalf("handleNewSessionCommand() error = %v, want the failure for the result record", err)
	}
	if ensurer.lastSpec.Runtime != "" {
		t.Fatalf("session should not be created with incomplete setup, got spec %#v", ensurer.lastSpec)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "setup is incomplete") {
		t.Fatalf("expected setup feedback, got %+v", sender.sent)
	}
}

func TestReplyFailureRendersChannelCopyForCode(t *testing.T) {
	p := &ChannelInboundProcessor{}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1"},
		ReplyTarget: "target-1",
	}

	failure := apperror.Wrap(apperror.CodeNoWorkspaceExec, errors.New("raw backend message"), nil)
	if err := p.replyFailure(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, failure, "fallback"); err != failure { //nolint:errorlint // identity: the failure is returned for the result record.
		t.Fatalf("replyFailure() error = %v, want the failure", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent replies = %d, want 1", len(sender.sent))
	}
	got := sender.sent[0].Message.PlainText()
	if strings.Contains(got, "raw backend message") || !strings.Contains(got, "workspace commands") {
		t.Fatalf("text = %q, want the channel copy for no_workspace_exec", got)
	}
}

func TestThreadErrorTranslatesThreadErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code apperror.Code
	}{
		{sessionpkg.ErrACPAgentIDRequired, apperror.CodeACPAgentNotConfigured},
		{sessionpkg.ErrACPAgentNotConfigured, apperror.CodeACPAgentNotConfigured},
		{sessionpkg.ErrACPUnknownAgent, apperror.CodeACPAgentNotFound},
		{sessionpkg.ErrACPAgentNotEnabled, apperror.CodeACPAgentNotEnabled},
		{sessionpkg.ErrACPRuntimeOwnerMissing, apperror.CodeACPRuntimeOwnerMissing},
	} {
		if got := apperror.CodeOf(threadError(tc.err)); got != tc.code {
			t.Errorf("threadError(%v) code = %q, want %s", tc.err, got, tc.code)
		}
	}
	for _, other := range []error{apperror.New(apperror.CodeAgentProviderRateLimited, nil), errors.New("synthetic failure")} {
		if got := threadError(other); got != other { //nolint:errorlint // identity: other errors pass unchanged.
			t.Errorf("threadError(%v) = %v, want it unchanged", other, got)
		}
	}
}

func TestRequireWorkspaceExecUnboundIdentityPointsToLink(t *testing.T) {
	// An owner whose IM identity isn't linked yet fails this gate on every
	// message; the reply must tell them how to link rather than just deny.
	p := &ChannelInboundProcessor{permissionChecker: &fakeBotPermissionChecker{}}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1"},
		ReplyTarget: "target-1",
	}

	err := p.requireWorkspaceExecForExternalAgent(context.Background(), InboundIdentity{BotID: "bot-1"})
	if got := p.replyFailure(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, err, ""); got != err { //nolint:errorlint // identity: the failure is returned for the result record.
		t.Fatalf("replyFailure() error = %v, want %v", got, err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent replies = %d, want 1", len(sender.sent))
	}
	got := sender.sent[0].Message.PlainText()
	if !strings.Contains(got, "/link") || !strings.Contains(got, "Connected Accounts") {
		t.Fatalf("feedback text = %q, want link guidance", got)
	}
}

// A sender who may not run an Agent is refused before anything about the
// named Agent is said, its incomplete setup included.
func TestHandleNewSessionCommandNamedAgentRequiresWorkspaceExec(t *testing.T) {
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: false},
		acpProfiles:       testACPProfiles{},
		botAgents:         testBotAgents,
		acpAgentSetup: fakeACPAgentSetupReader{metadata: map[string]any{
			"acp": map[string]any{"agents": map[string]any{
				"custom-agent": map[string]any{"enabled": true, "setup_mode": "api_key", "managed": map[string]any{}},
			}},
		}},
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new custom agent"},
		ReplyTarget: "target-1",
		Conversation: channel.Conversation{
			ID:   "dm-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
		BotID:             "bot-1",
		ChannelIdentityID: "user-no-exec",
		UserID:            "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if apperror.CodeOf(err) != apperror.CodeNoWorkspaceExec {
		t.Fatalf("handleNewSessionCommand() error = %v, want the failure for the result record", err)
	}
	// The inbound message record keeps the sender's refusal a client fault.
	if record := errlog.Finish(context.Background(), "channel.inbound", err, errlog.Options{}); record.Level != slog.LevelInfo || record.Report.Fault != apperror.FaultClient {
		t.Fatalf("inbound record level=%v fault=%q, want INFO client", record.Level, record.Report.Fault)
	}
	if ensurer.lastSpec.Runtime != "" {
		t.Fatalf("session should not be created without workspace_exec, got spec %#v", ensurer.lastSpec)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "permission to run workspace commands") {
		t.Fatalf("expected workspace_exec feedback, got %+v", sender.sent)
	}
}

// TestSendNewConfirmation_LocalizesActionLabels guards the
// newSession.action.{confirm,cancel} key rename. /new on a button-capable
// channel posts a Confirm/Cancel gate; the labels must render in the user's
// command_ui_language with the correct callback data carrying through.
func TestSendNewConfirmation_LocalizesActionLabels(t *testing.T) {
	p := &ChannelInboundProcessor{}
	cases := []struct {
		locale      string
		wantConfirm string
		wantCancel  string
	}{
		{"en", "✅ Confirm", "✕ Cancel"},
		{"zh", "✅ 确认", "✕ 取消"},
	}
	for _, tc := range cases {
		t.Run(tc.locale, func(t *testing.T) {
			s := &fakeReplySender{}
			err := p.sendNewConfirmation(
				context.Background(),
				channel.InboundMessage{ReplyTarget: "test-target"},
				s,
				i18n.New(tc.locale),
				"chat",
				i18n.New(tc.locale).T("newSession.modeChat"),
				channel.ChannelCapabilities{Buttons: true, Markdown: true, Text: true},
			)
			if err != nil {
				t.Fatalf("sendNewConfirmation: %v", err)
			}
			if len(s.sent) != 1 {
				t.Fatalf("expected 1 sent message, got %d", len(s.sent))
			}
			out := s.sent[0].Message
			if len(out.Actions) != 2 {
				t.Fatalf("expected 2 actions (confirm + cancel), got %d", len(out.Actions))
			}
			var confirm, cancel channel.Action
			for _, a := range out.Actions {
				if a.Value == command.EncodeConfirmNewCallback("chat") {
					confirm = a
				} else if a.Value == command.DismissCallback() {
					cancel = a
				}
			}
			if confirm.Label != tc.wantConfirm {
				t.Errorf("[%s] confirm label = %q, want %q", tc.locale, confirm.Label, tc.wantConfirm)
			}
			if cancel.Label != tc.wantCancel {
				t.Errorf("[%s] cancel label = %q, want %q", tc.locale, cancel.Label, tc.wantCancel)
			}
			// Body must contain the bold confirm title (markup intact on the
			// Markdown-capable channel used in this test).
			if !strings.Contains(out.Text, "Confirm") && !strings.Contains(out.Text, "确认") {
				t.Errorf("[%s] confirmation body missing confirm token, got %q", tc.locale, out.Text)
			}
		})
	}
}

// The Confirm button carries a named Agent as its id, so the tap lands on the
// same Agent through the real command parser. The bot's default Agent is not
// carried: it is resolved again on confirm.
func TestNewConfirmationKeepsNamedAgent(t *testing.T) {
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "group"}}
	spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new discuss custom agent").Parsed, group, testBotAgents)
	if err != nil {
		t.Fatalf("resolveNewSessionSpec() error = %v", err)
	}
	callback := command.EncodeConfirmNewCallback(newSessionConfirmModeText(spec))
	parsedCallback, ok := command.DecodeCallback(callback)
	if !ok {
		t.Fatalf("callback = %q, want a decodable callback", callback)
	}
	confirmed, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, parsedCallback.SyntheticCommand()).Parsed, group, testBotAgents)
	if err != nil || confirmed.BotAgentID != testACPBotAgentID || confirmed.Mode != sessionpkg.TypeDiscuss {
		t.Fatalf("confirmed spec = %#v, %v, want the same Agent and mode", confirmed, err)
	}
	if label := newSessionDisplayModeLabel(i18n.New("en"), spec, testACPProfiles{}); label != "discussion with Custom Agent" {
		t.Fatalf("label = %q, want the Agent's name", label)
	}
	if got := newSessionConfirmModeText(NewSessionSpec{Mode: sessionpkg.TypeChat, BotAgentID: testACPBotAgentID}); got != sessionpkg.TypeChat {
		t.Fatalf("confirm text for the default Agent = %q, want the mode alone", got)
	}
}

// A named direct Agent runs on its own runtime whatever the bot's default is,
// and takes the project the settings hold only when it is that default.
func TestHandleNewSessionCommandCreatesDirectAgentChatSpec(t *testing.T) {
	ownerID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	for defaultAgentID, wantProjectPath := range map[string]string{
		testACPBotAgentID:   "",
		testCodexBotAgentID: "/workspace/default",
	} {
		chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
		ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeChat}}
		p := &ChannelInboundProcessor{
			routeResolver:     chatSvc,
			sessionEnsurer:    ensurer,
			permissionChecker: &fakeBotPermissionChecker{allowed: true},
			acpProfiles:       testACPProfiles{},
			botAgents:         testBotAgents,
			defaultChatRuntime: fakeDefaultChatRuntimeReader{settings: DefaultChatRuntimeSettings{
				BotAgentID:  defaultAgentID,
				Runtime:     sessionpkg.RuntimeACPAgent,
				ACPAgentID:  "custom-agent",
				ProjectPath: "/workspace/default",
			}},
		}
		sender := &fakeReplySender{}
		msg := channel.InboundMessage{
			Channel:     channel.ChannelTypeTelegram,
			Message:     channel.Message{ID: "msg-1", Text: "/new chat codex"},
			ReplyTarget: "target-1",
			Conversation: channel.Conversation{
				ID:   "dm-1",
				Type: channel.ConversationTypePrivate,
			},
		}

		err := p.handleNewSessionCommand(context.Background(), channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{
			BotID:             "bot-1",
			ChannelIdentityID: "cccccccc-cccc-cccc-cccc-cccccccccccc",
			UserID:            ownerID,
		}, mustCommandInvocation(t, msg.Message.PlainText()))
		if err != nil {
			t.Fatalf("handleNewSessionCommand() error = %v", err)
		}
		spec := ensurer.lastSpec
		if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeCodex || spec.Type != sessionpkg.TypeChat || spec.BotAgentID != testCodexBotAgentID {
			t.Fatalf("spec = %#v, want chat/codex/chat bound to the named Agent", spec)
		}
		if got := newSessionMetadataString(spec.Metadata, "project_path"); got != wantProjectPath {
			t.Fatalf("default %s: project_path = %q, want %q", defaultAgentID, got, wantProjectPath)
		}
		if spec.RuntimeOwnerAccountID != ownerID {
			t.Fatalf("runtime owner = %q, want authenticated channel identity", spec.RuntimeOwnerAccountID)
		}
	}
}

// /new answers a failed session create with the copy for its specific public
// error, whatever that code is, or with the flow's own failure copy; a caller
// that has canceled gets no reply. The error is returned for the message's
// result record.
func TestHandleNewSessionCommandAnswersCreateFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	en := i18n.New("en")
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"specific code", context.Background(), apperror.New(apperror.CodeWorkspaceUnreachable, nil), en.T("errors.workspace.unreachable")},
		{"no public error", context.Background(), errors.New("synthetic insert failed"), friendlyOps(en, "ops.verb.startSession")},
		{"caller canceled", canceled, fmt.Errorf("create: %w", context.Canceled), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
			p := &ChannelInboundProcessor{routeResolver: chatSvc, sessionEnsurer: &fakeSessionEnsurer{createErr: tc.err}}
			sender := &fakeReplySender{}
			msg := channel.InboundMessage{
				Channel:      channel.ChannelTypeTelegram,
				Message:      channel.Message{ID: "msg-1", Text: "/new chat"},
				ReplyTarget:  "target-1",
				Conversation: channel.Conversation{ID: "dm-1", Type: channel.ConversationTypePrivate},
			}
			err := p.handleNewSessionCommand(tc.ctx, channel.ChannelConfig{TeamID: "team-test"}, msg, sender, InboundIdentity{BotID: "bot-1"}, mustCommandInvocation(t, msg.Message.PlainText()))
			if !errors.Is(err, tc.err) && apperror.CodeOf(err) != apperror.CodeOf(tc.err) {
				t.Fatalf("error = %v, want %v for the result record", err, tc.err)
			}
			switch {
			case tc.want == "" && len(sender.sent) != 0:
				t.Fatalf("canceled caller got %+v", sender.sent)
			case tc.want != "" && (len(sender.sent) != 1 || sender.sent[0].Message.PlainText() != tc.want):
				t.Fatalf("replies = %+v, want %q", sender.sent, tc.want)
			}
		})
	}
}

type failingReplySender struct {
	fakeReplySender
	err error
}

func (s *failingReplySender) Send(context.Context, channel.OutboundMessage) error { return s.err }

func TestReplyFailureRecordsUnsentReplyAsEventAndKeepsAttribution(t *testing.T) {
	var buf bytes.Buffer
	p := &ChannelInboundProcessor{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	sender := &failingReplySender{err: errors.New("telegram down")}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1"},
		ReplyTarget: "target-1",
	}

	failure := apperror.Wrap(apperror.CodeNoWorkspaceExec, errors.New("denied"), nil)
	got := p.replyFailure(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, failure, "fallback")
	if got != failure { //nolint:errorlint // identity: the flow's failure is returned unjoined.
		t.Fatalf("replyFailure() error = %v, want the flow's failure unchanged", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"msg":"failure reply not sent"`) || !strings.Contains(lines[0], `"level":"WARN"`) {
		t.Fatalf("records = %q, want one WARN event for the unsent reply", buf.String())
	}
}
