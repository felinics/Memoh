package inbound

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
	"github.com/felinics/memoh/internal/channel/route"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/command"
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

func (testACPProfiles) ResolveACPSetupPreflight(agentID string, metadata map[string]any) turn.ACPSetupPreflight {
	acp, _ := metadata["acp"].(map[string]any)
	agents, _ := acp["agents"].(map[string]any)
	config, _ := agents[strings.ToLower(strings.TrimSpace(agentID))].(map[string]any)
	enabled, _ := config["enabled"].(bool)
	result := turn.ACPSetupPreflight{Enabled: enabled}
	mode, modeSet := config["setup_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if !modeSet || mode == "" || mode == "self" {
		return result
	}
	managed, _ := config["managed"].(map[string]any)
	if value, _ := managed["api_key"].(string); strings.TrimSpace(value) == "" {
		result.MissingManagedField = &turn.ACPManagedField{ID: "api_key", Label: "API key"}
	}
	return result
}

func resolveNewSessionTypeForTest(t *testing.T, text string, msg channel.InboundMessage) (string, error) {
	t.Helper()
	spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, text).Parsed, msg, testACPProfiles{})
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
	// A genuinely unknown mode still errors.
	if _, err := resolveNewSessionTypeForTest(t, "/new bogus", msg); err == nil {
		t.Errorf("/new bogus should error on unknown session type")
	}
}

func TestResolveNewSessionSpec_ACPAgent(t *testing.T) {
	dm := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "private"}}
	group := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "group"}}

	cases := []struct {
		name        string
		cmd         string
		msg         channel.InboundMessage
		wantMode    string
		wantRuntime string
		wantType    string
		wantAgent   string
	}{
		{"bare agent in dm", "/new custom-agent", dm, sessionpkg.TypeChat, sessionpkg.RuntimeACPAgent, sessionpkg.TypeACPAgent, "custom-agent"},
		{"chat agent in dm", "/new chat custom-agent", dm, sessionpkg.TypeChat, sessionpkg.RuntimeACPAgent, sessionpkg.TypeACPAgent, "custom-agent"},
		{"discuss agent", "/new discuss custom-agent", group, sessionpkg.TypeDiscuss, sessionpkg.RuntimeACPAgent, sessionpkg.TypeDiscuss, "custom-agent"},
		{"bare agent in group inherits discuss", "/new custom-agent", group, sessionpkg.TypeDiscuss, sessionpkg.RuntimeACPAgent, sessionpkg.TypeDiscuss, "custom-agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, tc.cmd).Parsed, tc.msg, testACPProfiles{})
			if err != nil {
				t.Fatalf("resolveNewSessionSpec(%q) error = %v", tc.cmd, err)
			}
			if spec.Mode != tc.wantMode || spec.Runtime != tc.wantRuntime || spec.Type != tc.wantType {
				t.Fatalf("spec = %#v, want mode/runtime/type %q/%q/%q", spec, tc.wantMode, tc.wantRuntime, tc.wantType)
			}
			if got := newSessionMetadataString(spec.Metadata, "acp_agent_id"); got != tc.wantAgent {
				t.Fatalf("agent = %q, want %q", got, tc.wantAgent)
			}
			if got := newSessionMetadataString(spec.Metadata, "project_path"); got != sessionpkg.DefaultACPProjectPath {
				t.Fatalf("project_path = %q, want default", got)
			}
		})
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
			spec, err := resolveNewSessionSpecParsed(invocation.Parsed, group, testACPProfiles{})
			if err != nil {
				t.Fatalf("resolveNewSessionSpecParsed() error = %v", err)
			}
			if spec.Mode != sessionpkg.TypeDiscuss || spec.Runtime != sessionpkg.RuntimeModel || acpNewSessionAgentID(spec) != "" {
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
			processor.SetCommandHandler(command.NewHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
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
	_, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new chat codex").Parsed, group, testACPProfiles{})
	if err == nil {
		t.Fatal("resolveNewSessionSpec error = nil, want group chat ACP unsupported")
	}
	var feedback *agentfeedback.Error
	if !errors.As(err, &feedback) || feedback.Code != agentfeedback.CodeGroupChatUnsupported {
		t.Fatalf("feedback = %#v, want code %s", feedback, agentfeedback.CodeGroupChatUnsupported)
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
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new chat custom-agent"},
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
	if got := newSessionMetadataString(spec.Metadata, "acp_agent_id"); got != "custom-agent" {
		t.Fatalf("agent = %q, want custom-agent", got)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent replies = %d, want 1", len(sender.sent))
	}
}

func TestHandleNewSessionCommandCreatesNativeSessionWithCreator(t *testing.T) {
	creatorID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{}
	p := &ChannelInboundProcessor{
		routeResolver:  chatSvc,
		sessionEnsurer: ensurer,
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
			Runtime:     sessionpkg.RuntimeACPAgent,
			ACPAgentID:  "custom-agent",
			ProjectPath: "/workspace/default",
			ProjectMode: sessionpkg.DefaultACPProjectMode,
		}},
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new custom-agent"},
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
	}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1", Text: "/new custom-agent"},
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
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
	}
	if ensurer.lastSpec.Runtime != "" {
		t.Fatalf("session should not be created with incomplete setup, got spec %#v", ensurer.lastSpec)
	}
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].Message.PlainText(), "setup is incomplete") {
		t.Fatalf("expected setup feedback, got %+v", sender.sent)
	}
}

func TestSendACPFeedbackErrorUsesI18nKey(t *testing.T) {
	p := &ChannelInboundProcessor{}
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{
		Channel:     channel.ChannelTypeTelegram,
		Message:     channel.Message{ID: "msg-1"},
		ReplyTarget: "target-1",
	}

	err := p.sendACPFeedbackError(context.Background(), sender, msg, InboundIdentity{BotID: "bot-1"}, agentfeedback.New(
		agentfeedback.CodeNoWorkspaceExec,
		"missing_workspace_exec",
		403,
		"chat.externalAgent.noWorkspaceExec",
		"raw backend message",
		nil,
	))
	if err != nil {
		t.Fatalf("sendACPFeedbackError() error = %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent replies = %d, want 1", len(sender.sent))
	}
	got := sender.sent[0].Message.PlainText()
	if got == "raw backend message" || !strings.Contains(got, "workspace commands") {
		t.Fatalf("feedback text = %q, want localized ACP feedback", got)
	}
}

func TestHandleNewSessionCommandACPRequiresWorkspaceExec(t *testing.T) {
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeACPAgent}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: false},
		acpProfiles:       testACPProfiles{},
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
		ChannelIdentityID: "user-no-exec",
		UserID:            "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
	}, mustCommandInvocation(t, msg.Message.PlainText()))
	if err != nil {
		t.Fatalf("handleNewSessionCommand() error = %v", err)
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

func TestSendNewConfirmationShowsACPRuntimeLabel(t *testing.T) {
	p := &ChannelInboundProcessor{acpProfiles: testACPProfiles{}}
	s := &fakeReplySender{}
	loc := i18n.New("en")
	spec := NewSessionSpec{
		Mode:    sessionpkg.TypeChat,
		Runtime: sessionpkg.RuntimeACPAgent,
		Metadata: map[string]any{
			"acp_agent_id": "custom-agent",
		},
	}

	err := p.sendNewConfirmation(
		context.Background(),
		channel.InboundMessage{ReplyTarget: "test-target"},
		s,
		loc,
		newSessionConfirmModeText(spec),
		newSessionDisplayModeLabel(loc, spec, p.acpProfiles),
		channel.ChannelCapabilities{Buttons: true, Markdown: true, Text: true},
	)
	if err != nil {
		t.Fatalf("sendNewConfirmation: %v", err)
	}
	if len(s.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(s.sent))
	}
	out := s.sent[0].Message
	if !strings.Contains(out.Text, "chat with Custom Agent / ACP") {
		t.Fatalf("confirmation text = %q, want ACP runtime label", out.Text)
	}
	if len(out.Actions) != 2 || out.Actions[0].Value != command.EncodeConfirmNewCallback("chat custom-agent") {
		t.Fatalf("actions = %#v, want callback to preserve /new chat custom-agent", out.Actions)
	}
}

// Direct external agents are addressed by runtime name in /new: no ACP
// profile lookup, a plain chat session on the direct runtime, and the same
// workspace-exec gate as ACP. This path used to bounce with unknown_agent.
func TestNewSessionCommandResolvesDirectAgents(t *testing.T) {
	dm := channel.InboundMessage{Channel: channel.ChannelTypeTelegram, Conversation: channel.Conversation{Type: "private"}}

	for _, cmd := range []string{"/new codex", "/new chat codex"} {
		spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, cmd).Parsed, dm, testACPProfiles{})
		if err != nil {
			t.Fatalf("resolveNewSessionSpec(%q) error = %v", cmd, err)
		}
		if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeCodex || spec.Type != sessionpkg.TypeChat {
			t.Fatalf("spec for %q = %#v, want chat/codex/chat", cmd, spec)
		}
	}

	spec, err := resolveNewSessionSpecParsed(mustCommandInvocation(t, "/new claude-code").Parsed, dm, testACPProfiles{})
	if err != nil {
		t.Fatalf("resolveNewSessionSpec(/new claude-code) error = %v", err)
	}
	if spec.Runtime != sessionpkg.RuntimeClaudeCode {
		t.Fatalf("spec = %#v, want claude-code runtime", spec)
	}
}

func TestHandleNewSessionCommandCreatesDirectAgentChatSpec(t *testing.T) {
	ownerID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	chatSvc := &fakeChatService{resolveResult: route.ResolveConversationResult{BotID: "chat-1", RouteID: "11111111-1111-1111-1111-111111111111"}}
	ensurer := &fakeSessionEnsurer{activeSession: SessionResult{ID: "22222222-2222-2222-2222-222222222222", Type: sessionpkg.TypeChat}}
	p := &ChannelInboundProcessor{
		routeResolver:     chatSvc,
		sessionEnsurer:    ensurer,
		permissionChecker: &fakeBotPermissionChecker{allowed: true},
		acpProfiles:       testACPProfiles{},
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
	if spec.Mode != sessionpkg.TypeChat || spec.Runtime != sessionpkg.RuntimeCodex || spec.Type != sessionpkg.TypeChat {
		t.Fatalf("spec = %#v, want chat/codex/chat", spec)
	}
	if spec.RuntimeOwnerAccountID != ownerID {
		t.Fatalf("runtime owner = %q, want authenticated channel identity", spec.RuntimeOwnerAccountID)
	}
}
