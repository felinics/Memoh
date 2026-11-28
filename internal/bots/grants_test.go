package bots

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

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

type fakeGrantCreateQueries struct {
	dbstore.Queries
	bot       sqlc.GetBotByIDRow
	createErr error
}

func (f fakeGrantCreateQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return f.bot, nil
}

func (f fakeGrantCreateQueries) CreateBotUserGrant(context.Context, sqlc.CreateBotUserGrantParams) (sqlc.BotUserGrant, error) {
	return sqlc.BotUserGrant{}, f.createErr
}

func TestCreateUserGrantUniqueConstraintFiltering(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-4111-8111-111111111111"
	ownerID := "22222222-2222-4222-8222-222222222222"
	pgBotID, _ := db.ParseUUID(botID)
	pgOwnerID, _ := db.ParseUUID(ownerID)

	for _, constraint := range []string{"idx_bot_user_grants_unique_user", "idx_bot_user_grants_unique_everyone"} {
		t.Run("matches_"+constraint, func(t *testing.T) {
			t.Parallel()
			pgErr := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
			svc := NewService(nil, fakeGrantCreateQueries{
				bot:       sqlc.GetBotByIDRow{ID: pgBotID, OwnerUserID: pgOwnerID},
				createErr: pgErr,
			})
			_, err := svc.CreateUserGrant(context.Background(), botID, ownerID, CreateUserGrantRequest{
				SubjectType: GrantSubjectEveryone,
				Permissions: []string{PermissionChat},
			})
			if !errors.Is(err, ErrGrantExists) {
				t.Fatalf("CreateUserGrant() error = %v, want ErrGrantExists", err)
			}
			var gotPG *pgconn.PgError
			if !errors.As(err, &gotPG) || gotPG.ConstraintName != constraint {
				t.Fatalf("CreateUserGrant() lost pgconn.PgError cause: %v", err)
			}
		})
	}

	t.Run("unrelated_constraint_is_not_ErrGrantExists", func(t *testing.T) {
		t.Parallel()
		pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "bot_user_grants_pkey"}
		svc := NewService(nil, fakeGrantCreateQueries{
			bot:       sqlc.GetBotByIDRow{ID: pgBotID, OwnerUserID: pgOwnerID},
			createErr: pgErr,
		})
		_, err := svc.CreateUserGrant(context.Background(), botID, ownerID, CreateUserGrantRequest{
			SubjectType: GrantSubjectEveryone,
			Permissions: []string{PermissionChat},
		})
		if err == nil || errors.Is(err, ErrGrantExists) {
			t.Fatalf("CreateUserGrant() error = %v, want raw error without ErrGrantExists", err)
		}
	})
}
