package access

import (
	"context"
	"testing"

	"github.com/memohai/memoh/domains/api/bot"
	aclpersistence "github.com/memohai/memoh/domains/api/bot/access/acl/persistence"
	accesspersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
)

func TestListManagersEveryoneUsesScopedBindings(t *testing.T) {
	ctx := context.Background()
	botID := "00000000-0000-0000-0000-000000000010"
	channelIdentityID := "00000000-0000-0000-0000-000000000020"

	svc := &Service{
		links: &fakeAccessStore{
			botScopedBindings: []accesspersistence.Binding{{
				ID:                "00000000-0000-0000-0000-000000000021",
				UserID:            "00000000-0000-0000-0000-000000000022",
				ChannelIdentityID: channelIdentityID,
			}},
		},
		identities: &fakeChannelIdentityReader{items: []accesspersistence.ChannelIdentity{{
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
		links: &fakeAccessStore{},
		acl: &fakeManageOverrides{
			overrides: []aclpersistence.ManageOverride{{
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

type fakeAccessStore struct {
	botScopedBindings []accesspersistence.Binding
	userBindings      map[string][]accesspersistence.Binding
}

func (*fakeAccessStore) CreateLinkCode(context.Context, accesspersistence.CreateLinkCodeCommand) (accesspersistence.LinkCode, error) {
	return accesspersistence.LinkCode{}, nil
}

func (*fakeAccessStore) FindLinkCode(context.Context, string) (accesspersistence.LinkCodeState, bool, error) {
	return accesspersistence.LinkCodeState{}, false, nil
}

func (*fakeAccessStore) RedeemLinkCode(context.Context, string, string) (accesspersistence.Binding, bool, error) {
	return accesspersistence.Binding{}, false, nil
}

func (f *fakeAccessStore) ListBindingsForUser(_ context.Context, userID string) ([]accesspersistence.Binding, error) {
	if f.userBindings == nil {
		return nil, nil
	}
	return f.userBindings[userID], nil
}

func (f *fakeAccessStore) ListBindingsForBot(context.Context, string) ([]accesspersistence.Binding, error) {
	return f.botScopedBindings, nil
}
func (*fakeAccessStore) DeleteBinding(context.Context, string, string) error { return nil }
func (*fakeAccessStore) ListUserIDsByChannelIdentity(context.Context, string) ([]string, error) {
	return nil, nil
}

type fakeChannelIdentityReader struct {
	items []accesspersistence.ChannelIdentity
	ids   []string
	calls int
}

func (f *fakeChannelIdentityReader) ListChannelIdentities(_ context.Context, ids []string) ([]accesspersistence.ChannelIdentity, error) {
	f.calls++
	f.ids = append([]string(nil), ids...)
	return f.items, nil
}

type fakeManageOverrides struct {
	overrides []aclpersistence.ManageOverride
}

func (*fakeManageOverrides) GetManageOverride(context.Context, string, string) (bool, bool, error) {
	return false, false, nil
}

func (f *fakeManageOverrides) ListManageOverrides(context.Context, string) ([]aclpersistence.ManageOverride, error) {
	return f.overrides, nil
}

func (*fakeManageOverrides) SetManageOverride(context.Context, string, string, bool, string) (aclpersistence.ManageOverride, error) {
	return aclpersistence.ManageOverride{}, nil
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
