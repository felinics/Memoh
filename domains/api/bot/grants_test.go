package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type grantStoreFake struct {
	GrantStore
	list func(context.Context, string) ([]GrantRecord, error)
}

func (f grantStoreFake) ListGrants(ctx context.Context, botID string) ([]GrantRecord, error) {
	return f.list(ctx, botID)
}

func TestNormalizePermissionsExpandsWorkspaceWrite(t *testing.T) {
	got, err := normalizePermissions([]string{PermissionWorkspaceWrite})
	if err != nil {
		t.Fatalf("normalizePermissions() error = %v", err)
	}
	want := []string{PermissionWorkspaceRead, PermissionWorkspaceWrite}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizePermissions() = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissionsExpandsManage(t *testing.T) {
	got, err := normalizePermissions([]string{PermissionManage})
	if err != nil {
		t.Fatalf("normalizePermissions() error = %v", err)
	}
	want := []string{
		PermissionChat,
		PermissionWorkspaceRead,
		PermissionWorkspaceWrite,
		PermissionWorkspaceExec,
		PermissionManage,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizePermissions() = %#v, want %#v", got, want)
	}
}

func TestNormalizePermissionsRejectsInvalidPermission(t *testing.T) {
	_, err := normalizePermissions([]string{"workspace_admin"})
	if !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("normalizePermissions() error = %v, want ErrInvalidPermission", err)
	}
}

func TestListUserGrantsEnrichesUsersThroughReader(t *testing.T) {
	const granteeID = "00000000-0000-0000-0000-000000000003"

	grants := grantStoreFake{list: func(context.Context, string) ([]GrantRecord, error) {
		return []GrantRecord{{
			ID:          "grant-1",
			BotID:       testBotID,
			SubjectType: GrantSubjectUser,
			UserID:      granteeID,
			Permissions: []byte(`["chat"]`),
		}}, nil
	}}
	users := userReaderFake{get: func(_ context.Context, userID string) (UserRecord, error) {
		switch userID {
		case testOwnerID:
			return UserRecord{ID: userID, Username: "owner", DisplayName: "Owner"}, nil
		case granteeID:
			return UserRecord{ID: userID, Username: "member", DisplayName: "Member", AvatarURL: "avatar"}, nil
		default:
			return UserRecord{}, errors.New("unknown user")
		}
	}}
	service := NewService(nil, &botStoreFake{}, grants, users, nil)

	got, err := service.ListUserGrants(t.Context(), testBotID)
	if err != nil {
		t.Fatalf("ListUserGrants() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListUserGrants() len = %d, want 2", len(got))
	}
	if got[0].UserUsername != "owner" || got[1].UserUsername != "member" ||
		got[1].UserDisplayName != "Member" || got[1].UserAvatarURL != "avatar" {
		t.Fatalf("ListUserGrants() = %#v", got)
	}
}
