package inbound

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/identities"
)

type fakeChannelIdentityService struct {
	channelIdentity identities.ChannelIdentity
	bySubject       map[string]identities.ChannelIdentity
	linkedUserIDs   map[string][]string
	err             error
	canonical       map[string]string
	calls           int
	lastDisplayName string
	lastMeta        map[string]any
}

func (f *fakeChannelIdentityService) ResolveByChannelIdentity(_ context.Context, _, externalID, displayName string, meta map[string]any) (identities.ChannelIdentity, error) {
	f.calls++
	f.lastDisplayName = displayName
	f.lastMeta = meta
	if f.err != nil {
		return identities.ChannelIdentity{}, f.err
	}
	if f.bySubject != nil {
		if identity, ok := f.bySubject[externalID]; ok {
			return identity, nil
		}
		return identities.ChannelIdentity{}, nil
	}
	return f.channelIdentity, nil
}

func (f *fakeChannelIdentityService) Canonicalize(_ context.Context, channelIdentityID string) (string, error) {
	if f.canonical != nil {
		if value, ok := f.canonical[channelIdentityID]; ok {
			return value, nil
		}
	}
	return channelIdentityID, nil
}

func (f *fakeChannelIdentityService) ListUserIDsByChannelIdentity(_ context.Context, channelIdentityID string) ([]string, error) {
	if f.linkedUserIDs == nil {
		return nil, nil
	}
	return f.linkedUserIDs[channelIdentityID], nil
}

type fakePolicyService struct {
	ownerUserID string
	err         error
}

func (f *fakePolicyService) BotOwnerUserID(_ context.Context, _ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.ownerUserID, nil
}

type fakeDirectoryAdapter struct {
	channelType channel.ChannelType
	resolveFn   func(ctx context.Context, cfg channel.ChannelConfig, input string, kind channel.DirectoryEntryKind) (channel.DirectoryEntry, error)
}

func (f *fakeDirectoryAdapter) Type() channel.ChannelType {
	return f.channelType
}

func (f *fakeDirectoryAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:         f.channelType,
		DisplayName:  "FakeDirectory",
		Capabilities: channel.ChannelCapabilities{},
	}
}

func (*fakeDirectoryAdapter) ListPeers(_ context.Context, _ channel.ChannelConfig, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (*fakeDirectoryAdapter) ListGroups(_ context.Context, _ channel.ChannelConfig, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (*fakeDirectoryAdapter) ListGroupMembers(_ context.Context, _ channel.ChannelConfig, _ string, _ channel.DirectoryQuery) ([]channel.DirectoryEntry, error) {
	return nil, nil
}

func (f *fakeDirectoryAdapter) ResolveEntry(ctx context.Context, cfg channel.ChannelConfig, input string, kind channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
	if f.resolveFn != nil {
		return f.resolveFn(ctx, cfg, input, kind)
	}
	return channel.DirectoryEntry{}, errors.New("resolve not implemented")
}

func TestIdentityResolverAllowGuestWithoutMembershipSideEffect(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-1"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender:      channel.Identity{SubjectID: "ext-1", DisplayName: "Guest"},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Identity.ChannelIdentityID != "channelIdentity-1" {
		t.Fatalf("expected channelIdentity-1, got: %s", state.Identity.ChannelIdentityID)
	}
	if state.Decision != nil {
		t.Fatal("expected no decision for allowed guest")
	}
}

func TestIdentityResolverResolveDisplayNameFromDirectory(t *testing.T) {
	registry := channel.NewRegistry()
	directoryAdapter := &fakeDirectoryAdapter{
		channelType: channel.ChannelType("feishu"),
		resolveFn: func(_ context.Context, _ channel.ChannelConfig, input string, kind channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
			if kind != channel.DirectoryEntryUser {
				t.Fatalf("expected kind user, got %s", kind)
			}
			if input != "ou-directory" {
				t.Fatalf("expected subject id ou-directory, got %s", input)
			}
			return channel.DirectoryEntry{
				Kind: channel.DirectoryEntryUser,
				Name: "Directory Name",
			}, nil
		},
	}
	if err := registry.Register(directoryAdapter); err != nil {
		t.Fatalf("register directory adapter failed: %v", err)
	}

	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-directory"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), registry, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender: channel.Identity{
			SubjectID: "ou-directory",
			Attributes: map[string]string{
				"open_id": "ou-directory",
			},
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Identity.DisplayName != "Directory Name" {
		t.Fatalf("expected directory display name, got %q", state.Identity.DisplayName)
	}
	if channelIdentitySvc.lastDisplayName != "Directory Name" {
		t.Fatalf("expected upsert display name Directory Name, got %q", channelIdentitySvc.lastDisplayName)
	}
}

func TestIdentityResolverMapsLinkedChannelIdentityToAccountUser(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-linked"},
		linkedUserIDs:   map[string][]string{"channelIdentity-linked": {"account-user-1"}},
	}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, nil, "")

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Sender:  channel.Identity{SubjectID: "ext-1"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if state.Identity.UserID != "account-user-1" {
		t.Fatalf("UserID = %q, want linked account user", state.Identity.UserID)
	}
}

func TestIdentityResolverDirectoryLookupFailureDoesNotFallbackToOpenID(t *testing.T) {
	registry := channel.NewRegistry()
	directoryAdapter := &fakeDirectoryAdapter{
		channelType: channel.ChannelType("feishu"),
		resolveFn: func(_ context.Context, _ channel.ChannelConfig, _ string, _ channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
			return channel.DirectoryEntry{}, errors.New("lookup failed")
		},
	}
	if err := registry.Register(directoryAdapter); err != nil {
		t.Fatalf("register directory adapter failed: %v", err)
	}

	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-directory-fail"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), registry, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender: channel.Identity{
			SubjectID: "ou-directory-fail",
			Attributes: map[string]string{
				"open_id": "ou-directory-fail",
				"user_id": "u-directory-fail",
			},
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Identity.DisplayName != "" {
		t.Fatalf("expected empty display name when directory lookup fails, got %q", state.Identity.DisplayName)
	}
	if channelIdentitySvc.lastDisplayName != "" {
		t.Fatalf("expected empty upsert display name on lookup failure, got %q", channelIdentitySvc.lastDisplayName)
	}
}

func TestIdentityResolverFeishuUsesOpenIDAsCanonicalSubject(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		bySubject: map[string]identities.ChannelIdentity{
			"ou-openid": {ID: "channelIdentity-openid"},
			"u-userid":  {ID: "channelIdentity-userid"},
		},
	}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender: channel.Identity{
			SubjectID: "ou-openid",
			Attributes: map[string]string{
				"open_id": "ou-openid",
				"user_id": "u-userid",
			},
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Identity.ChannelIdentityID != "channelIdentity-openid" {
		t.Fatalf("expected open_id identity, got %q", state.Identity.ChannelIdentityID)
	}
	if channelIdentitySvc.calls != 1 {
		t.Fatalf("expected only one identity resolution call for feishu, got %d", channelIdentitySvc.calls)
	}
}

func TestIdentityResolverDirectoryAvatarURLPropagated(t *testing.T) {
	registry := channel.NewRegistry()
	directoryAdapter := &fakeDirectoryAdapter{
		channelType: channel.ChannelType("feishu"),
		resolveFn: func(_ context.Context, _ channel.ChannelConfig, _ string, _ channel.DirectoryEntryKind) (channel.DirectoryEntry, error) {
			return channel.DirectoryEntry{
				Kind:      channel.DirectoryEntryUser,
				Name:      "Avatar User",
				AvatarURL: "https://example.com/avatar.png",
			}, nil
		},
	}
	if err := registry.Register(directoryAdapter); err != nil {
		t.Fatalf("register directory adapter failed: %v", err)
	}

	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-avatar"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), registry, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "target-id",
		Sender: channel.Identity{
			SubjectID:  "ou-avatar",
			Attributes: map[string]string{"open_id": "ou-avatar"},
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1", ChannelType: channel.ChannelType("feishu")}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Identity.DisplayName != "Avatar User" {
		t.Fatalf("expected display name Avatar User, got %q", state.Identity.DisplayName)
	}
	if state.Identity.AvatarURL != "https://example.com/avatar.png" {
		t.Fatalf("expected avatar url, got %q", state.Identity.AvatarURL)
	}
	if channelIdentitySvc.lastMeta == nil {
		t.Fatal("expected metadata with avatar_url to be passed to channel identity service")
	}
	if channelIdentitySvc.lastMeta["avatar_url"] != "https://example.com/avatar.png" {
		t.Fatalf("expected avatar_url in meta, got %v", channelIdentitySvc.lastMeta)
	}
}

func TestIdentityResolverExistingMemberPasses(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-2"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "chat-123",
		Sender:      channel.Identity{SubjectID: "tg-user-1"},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("existing member should pass without decision")
	}
}

func TestIdentityResolverPublicBotGuestPasses(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-5"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "Access denied.")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("telegram"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "chat-123",
		Sender:      channel.Identity{SubjectID: "stranger-1"},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("public bot guest should pass identity resolution and be handled by ACL later")
	}
}

func TestIdentityResolverNonOwnerGroupMessagePassesToACL(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-group"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner"}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{Text: "hello"},
		Sender:  channel.Identity{SubjectID: "ext-group-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: "group",
		},
	}

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("non-owner group message should pass identity resolution (ACL decides later)")
	}
}

func TestIdentityResolverPersonalBotAllowsOwnerInGroup(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-owner"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner"}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{Text: "hello from owner"},
		Sender:  channel.Identity{SubjectID: "ext-owner-1"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: "group",
		},
	}

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("owner group message should pass")
	}
	if state.Identity.ForceReply {
		t.Fatal("owner group message should not force reply")
	}
}

func TestIdentityResolverPersonalBotAllowsOwnerDirectWithoutMembership(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-owner-direct"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner-direct"}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{Text: "hello from owner"},
		Sender:  channel.Identity{SubjectID: "ext-owner-direct"},
		Conversation: channel.Conversation{
			ID:   "p2p-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("owner direct message should pass")
	}
	if state.Identity.ForceReply {
		t.Fatal("owner direct message should not force reply")
	}
}

func TestIdentityResolverFeishuUnlinkedOpenIDPassesToACL(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{
		bySubject: map[string]identities.ChannelIdentity{
			"ou-open-owner": {ID: "channelIdentity-open-owner"},
			"u-owner":       {ID: "channelIdentity-user-owner"},
		},
	}
	policySvc := &fakePolicyService{ownerUserID: "owner-user-1"}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "")

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{Text: "hello from owner"},
		Sender: channel.Identity{
			SubjectID: "ou-open-owner",
			Attributes: map[string]string{
				"open_id": "ou-open-owner",
				"user_id": "u-owner",
			},
		},
		Conversation: channel.Conversation{
			ID:   "p2p-1",
			Type: channel.ConversationTypePrivate,
		},
	}

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Without linked user, non-owner messages pass identity resolution; ACL decides later.
	if state.Decision != nil {
		t.Fatal("unlinked user should pass identity resolution (ACL decides later)")
	}
	if state.Identity.ChannelIdentityID != "channelIdentity-open-owner" {
		t.Fatalf("expected open_id identity, got: %s", state.Identity.ChannelIdentityID)
	}
	if channelIdentitySvc.calls != 1 {
		t.Fatalf("expected only open_id resolution attempt, got calls=%d", channelIdentitySvc.calls)
	}
}

func TestIdentityResolverNonOwnerDirectPassesToACL(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-non-owner"}}
	policySvc := &fakePolicyService{ownerUserID: "channelIdentity-owner"}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "Access denied.")

	msg := channel.InboundMessage{
		BotID:   "bot-1",
		Channel: channel.ChannelType("feishu"),
		Message: channel.Message{Text: "hello from non-owner"},
		Sender:  channel.Identity{SubjectID: "ext-non-owner"},
		Conversation: channel.Conversation{
			ID:   "p2p-2",
			Type: channel.ConversationTypePrivate,
		},
	}

	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("non-owner direct message should pass identity resolution (ACL decides later)")
	}
}

func TestIdentityResolverPublicBotGroupGuestPasses(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-group-denied"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "Access denied.")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "group-target",
		Sender:      channel.Identity{SubjectID: "stranger-group"},
		Conversation: channel.Conversation{
			ID:   "group-1",
			Type: "group",
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("public bot group guest should pass identity resolution")
	}
}

func TestIdentityResolverPublicBotDirectGuestPasses(t *testing.T) {
	channelIdentitySvc := &fakeChannelIdentityService{channelIdentity: identities.ChannelIdentity{ID: "channelIdentity-direct-denied"}}
	policySvc := &fakePolicyService{}
	resolver := NewIdentityResolver(slog.Default(), nil, channelIdentitySvc, policySvc, "Access denied.")

	msg := channel.InboundMessage{
		BotID:       "bot-1",
		Channel:     channel.ChannelType("feishu"),
		Message:     channel.Message{Text: "hello"},
		ReplyTarget: "direct-target",
		Sender:      channel.Identity{SubjectID: "stranger-direct"},
		Conversation: channel.Conversation{
			ID:   "p2p-1",
			Type: channel.ConversationTypePrivate,
		},
	}
	state, err := resolver.Resolve(context.Background(), channel.ChannelConfig{TeamID: "team-test", BotID: "bot-1"}, msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Decision != nil {
		t.Fatal("public bot direct guest should pass identity resolution")
	}
}
