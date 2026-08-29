package channelaccess

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// TestListManagersEveryoneUsesScopedBindings verifies that when "everyone" carries
// Manage, ListManagers uses the bot-scoped query (ListChannelIdentityBindingsForBot)
// instead of the global ListChannelIdentityBindings. This prevents cross-team
// data leaks while still showing workspace members' bound identities.
func TestListManagersEveryoneUsesScopedBindings(t *testing.T) {
	ctx := context.Background()
	botID := "00000000-0000-0000-0000-000000000010"
	channelIdentityID := "00000000-0000-0000-0000-000000000020"

	svc := &Service{
		queries: &fakeChannelAccessQueries{
			botScopedBindings: []sqlc.ListChannelIdentityBindingsForBotRow{{
				ID:                         mustUUID(t, "00000000-0000-0000-0000-000000000021"),
				UserID:                     mustUUID(t, "00000000-0000-0000-0000-000000000022"),
				ChannelIdentityID:          mustUUID(t, channelIdentityID),
				ChannelType:                text("telegram"),
				ChannelSubjectID:           text("tg-1"),
				ChannelIdentityDisplayName: text("Alice"),
			}},
		},
		acl: &fakeManageOverrides{},
		bots: &fakeBotPermissions{
			grants: []bots.UserGrant{{
				BotID:       botID,
				SubjectType: bots.GrantSubjectEveryone,
				Permissions: []string{bots.PermissionManage},
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

// TestListManagersLocalOverrideAppliedWithoutBinding verifies that a local deny
// override appears in the manager list even when the identity has no per-user
// grant binding (the everyone-manage path no longer enumerates global bindings).
func TestListManagersLocalOverrideAppliedWithoutBinding(t *testing.T) {
	ctx := context.Background()
	botID := "00000000-0000-0000-0000-000000000010"
	channelIdentityID := "00000000-0000-0000-0000-000000000020"

	svc := &Service{
		queries: &fakeChannelAccessQueries{},
		acl: &fakeManageOverrides{
			overrides: []acl.ManageOverride{{
				BotID:             botID,
				ChannelIdentityID: channelIdentityID,
				Granted:           false,
			}},
		},
		bots: &fakeBotPermissions{
			grants: []bots.UserGrant{{
				BotID:       botID,
				SubjectType: bots.GrantSubjectEveryone,
				Permissions: []string{bots.PermissionManage},
			}},
		},
	}

	items, err := svc.ListManagers(ctx, botID)
	if err != nil {
		t.Fatalf("list managers: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 override entry, got %d: %#v", len(items), items)
	}
	item := items[0]
	if !item.HasOverride {
		t.Fatalf("expected HasOverride, got %#v", item)
	}
	if item.Manage {
		t.Fatalf("expected local deny override (Manage=false), got %#v", item)
	}
}

type fakeChannelAccessQueries struct {
	dbstore.Queries
	allBindings       []sqlc.ListChannelIdentityBindingsRow
	botScopedBindings []sqlc.ListChannelIdentityBindingsForBotRow
}

func (f *fakeChannelAccessQueries) ListChannelIdentityBindings(context.Context) ([]sqlc.ListChannelIdentityBindingsRow, error) {
	return f.allBindings, nil
}

func (f *fakeChannelAccessQueries) ListChannelIdentityBindingsForBot(context.Context, pgtype.UUID) ([]sqlc.ListChannelIdentityBindingsForBotRow, error) {
	return f.botScopedBindings, nil
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
	grants []bots.UserGrant
}

func (*fakeBotPermissions) ResolveUserPermissions(context.Context, string, string, bool) ([]string, error) {
	return nil, nil
}

func (f *fakeBotPermissions) ListUserGrants(context.Context, string) ([]bots.UserGrant, error) {
	return f.grants, nil
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", value, err)
	}
	return id
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}
