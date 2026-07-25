package access

import (
	"context"
	"testing"

	"github.com/memohai/memoh/domains/api/access/acl"
	"github.com/memohai/memoh/domains/api/bot"
)

func TestListManagersEveryoneUsesScopedBindings(t *testing.T) {
	ctx := context.Background()
	botID := "00000000-0000-0000-0000-000000000010"
	channelIdentityID := "00000000-0000-0000-0000-000000000020"

	svc := &Service{
		store: &fakeAccessStore{
			botScopedBindings: []Binding{{
				ID:                "00000000-0000-0000-0000-000000000021",
				UserID:            "00000000-0000-0000-0000-000000000022",
				ChannelIdentityID: channelIdentityID,
			}},
		},
		identities: &fakeChannelIdentityReader{items: []ChannelIdentity{{
			ID: channelIdentityID, Channel: "telegram", ChannelSubjectID: "tg-1", DisplayName: "Alice",
		}}},
		acl: &fakeManageOverrides{},
		bots: &fakeBotPermissions{
			grants: []bot.UserGrant{{
				BotID:       botID,
				SubjectType: bot.GrantSubjectEveryone,
				Permissions: []string{bot.PermissionManage},
			}},
		},
	}

	items, err := svc.ListManagers(ctx, botID)
	if err != nil {
		t.Fatalf("list managers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 manager from scoped bindings, got %d: %#v", len(items), items)
	}
	item := items[0]
	if !item.Bound || !item.Inherited || !item.Manage {
		t.Fatalf("expected bound inherited manage, got %#v", item)
	}
	if item.ChannelIdentityDisplayName != "Alice" {
		t.Fatalf("expected display name Alice, got %q", item.ChannelIdentityDisplayName)
	}
}

func TestListManagersLocalOverrideAppliedWithoutBinding(t *testing.T) {
	ctx := context.Background()
	botID := "00000000-0000-0000-0000-000000000010"
	channelIdentityID := "00000000-0000-0000-0000-000000000020"

	svc := &Service{
		store: &fakeAccessStore{},
		acl: &fakeManageOverrides{
			overrides: []acl.ManageOverride{{
				BotID:             botID,
				ChannelIdentityID: channelIdentityID,
				Granted:           false,
			}},
		},
		bots: &fakeBotPermissions{
			grants: []bot.UserGrant{{
				BotID:       botID,
				SubjectType: bot.GrantSubjectEveryone,
				Permissions: []string{bot.PermissionManage},
			}},
		},
	}

	items, err := svc.ListManagers(ctx, botID)
	if err != nil {
		t.Fatalf("list managers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 override-only manager, got %d: %#v", len(items), items)
	}
	item := items[0]
	if !item.HasOverride || item.Manage {
		t.Fatalf("expected local deny override, got %#v", item)
	}
}

func TestListUserBindingsBatchEnrichesCurrentIdentityDetails(t *testing.T) {
	const (
		userID    = "00000000-0000-0000-0000-000000000010"
		identity1 = "00000000-0000-0000-0000-000000000020"
		identity2 = "00000000-0000-0000-0000-000000000021"
	)
	store := &fakeAccessStore{userBindings: map[string][]Binding{
		userID: {
			{ID: "binding-1", UserID: userID, ChannelIdentityID: identity1},
			{ID: "binding-2", UserID: userID, ChannelIdentityID: identity1},
			{ID: "binding-3", UserID: userID, ChannelIdentityID: identity2},
		},
	}}
	reader := &fakeChannelIdentityReader{items: []ChannelIdentity{{
		ID: identity1, Channel: "telegram", ChannelSubjectID: "subject", DisplayName: "Alice", AvatarURL: "avatar",
	}}}

	items, err := NewService(nil, store, reader, nil, nil).ListUserBindings(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListUserBindings() error = %v", err)
	}
	if reader.calls != 1 || len(reader.ids) != 2 {
		t.Fatalf("identity reader calls = %d, ids = %v", reader.calls, reader.ids)
	}
	for _, item := range items[:2] {
		if item.ChannelType != "telegram" || item.ChannelSubjectID != "subject" || item.ChannelIdentityDisplayName != "Alice" || item.ChannelIdentityAvatarURL != "avatar" {
			t.Fatalf("enriched binding = %#v", item)
		}
	}
	if items[2].ChannelType != "" || items[2].ChannelIdentityDisplayName != "" {
		t.Fatalf("missing identity binding = %#v", items[2])
	}
}

func TestListUserBindingsEmptySkipsIdentityReader(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000010"
	reader := &fakeChannelIdentityReader{}
	store := &fakeAccessStore{userBindings: map[string][]Binding{userID: {}}}

	items, err := NewService(nil, store, reader, nil, nil).ListUserBindings(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListUserBindings() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("ListUserBindings() = %#v", items)
	}
	if reader.calls != 0 {
		t.Fatalf("identity reader calls = %d", reader.calls)
	}
}

type fakeAccessStore struct {
	botScopedBindings []Binding
	userBindings      map[string][]Binding
}

func (*fakeAccessStore) CreateLinkCode(context.Context, CreateLinkCodeCommand) (LinkCode, error) {
	return LinkCode{}, nil
}

func (*fakeAccessStore) FindLinkCode(context.Context, string) (LinkCodeState, bool, error) {
	return LinkCodeState{}, false, nil
}

func (*fakeAccessStore) RedeemLinkCode(context.Context, string, string) (Binding, bool, error) {
	return Binding{}, false, nil
}

func (f *fakeAccessStore) ListBindingsForUser(_ context.Context, userID string) ([]Binding, error) {
	if f.userBindings == nil {
		return nil, nil
	}
	return f.userBindings[userID], nil
}

func (f *fakeAccessStore) ListBindingsForBot(context.Context, string) ([]Binding, error) {
	return f.botScopedBindings, nil
}
func (*fakeAccessStore) DeleteBinding(context.Context, string, string) error { return nil }
func (*fakeAccessStore) ListUserIDsByChannelIdentity(context.Context, string) ([]string, error) {
	return nil, nil
}

type fakeChannelIdentityReader struct {
	items []ChannelIdentity
	ids   []string
	calls int
}

func (f *fakeChannelIdentityReader) ListChannelIdentities(_ context.Context, ids []string) ([]ChannelIdentity, error) {
	f.calls++
	f.ids = append([]string(nil), ids...)
	return f.items, nil
}

type fakeManageOverrides struct {
	overrides []acl.ManageOverride
}

func (*fakeManageOverrides) GetManageOverride(context.Context, string, string) (bool, bool, error) {
	return false, false, nil
}

func (f *fakeManageOverrides) ListManageOverrides(context.Context, string) ([]acl.ManageOverride, error) {
	return f.overrides, nil
}

func (*fakeManageOverrides) SetManageOverride(context.Context, string, string, bool, string) (acl.ManageOverride, error) {
	return acl.ManageOverride{}, nil
}

func (*fakeManageOverrides) DeleteManageOverride(context.Context, string, string) error {
	return nil
}

type fakeBotPermissions struct {
	grants []bot.UserGrant
}

func (*fakeBotPermissions) ResolveUserPermissions(context.Context, string, string, bool) ([]string, error) {
	return nil, nil
}

func (f *fakeBotPermissions) ListUserGrants(context.Context, string) ([]bot.UserGrant, error) {
	return f.grants, nil
}
