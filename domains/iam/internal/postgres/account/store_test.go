package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/iam/account/persistence"
	dbsqlc "github.com/memohai/memoh/domains/iam/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type queryFake struct {
	accountQueries
	countAccounts func(context.Context) (int64, error)
	getByUserID   func(context.Context, pgtype.UUID) (dbsqlc.IamTeamAccount, error)
	createAccount func(context.Context, dbsqlc.CreateAccountParams) (dbsqlc.CreateAccountRow, error)
	updateProfile func(context.Context, dbsqlc.UpdateAccountProfileParams) (dbsqlc.UpdateAccountProfileRow, error)
}

func (f queryFake) CountAccounts(ctx context.Context) (int64, error) {
	return f.countAccounts(ctx)
}

func (f queryFake) GetAccountByUserID(ctx context.Context, id pgtype.UUID) (dbsqlc.IamTeamAccount, error) {
	return f.getByUserID(ctx, id)
}

func (f queryFake) CreateAccount(ctx context.Context, input dbsqlc.CreateAccountParams) (dbsqlc.CreateAccountRow, error) {
	return f.createAccount(ctx, input)
}

func (f queryFake) UpdateAccountProfile(ctx context.Context, input dbsqlc.UpdateAccountProfileParams) (dbsqlc.UpdateAccountProfileRow, error) {
	return f.updateProfile(ctx, input)
}

func TestStoreGetByUserIDMapsPrincipalAndMembershipState(t *testing.T) {
	t.Parallel()

	userID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	modelID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	createdAt := time.Date(2026, time.July, 23, 10, 30, 0, 0, time.UTC)
	store := newStore(queryFake{getByUserID: func(_ context.Context, got pgtype.UUID) (dbsqlc.IamTeamAccount, error) {
		if got != userID {
			t.Fatalf("GetAccountByUserID() id = %v, want %v", got, userID)
		}
		return dbsqlc.IamTeamAccount{
			ID:                 userID,
			Username:           pgtype.Text{String: "alice", Valid: true},
			PasswordHash:       pgtype.Text{String: "hash", Valid: true},
			Role:               "admin",
			IsActive:           pgtype.Bool{Bool: false, Valid: true},
			PrincipalIsActive:  true,
			MembershipIsActive: false,
			Metadata:           []byte(`{"onboarding_completed":true}`),
			CreatedAt:          pgtype.Timestamptz{Time: createdAt, Valid: true},
			TitleModelID:       modelID,
		}, nil
	}})

	record, err := store.GetByUserID(context.Background(), userID.String())
	if err != nil {
		t.Fatalf("GetByUserID() error = %v", err)
	}
	if record.ID != userID.String() || record.Username != "alice" || record.Role != "admin" {
		t.Fatalf("GetByUserID() record = %#v", record)
	}
	if record.IsActive || !record.PrincipalActive || record.MembershipActive {
		t.Fatalf("active state was not preserved: %#v", record)
	}
	if !record.HasPasswordHash || record.PasswordHash != "hash" {
		t.Fatalf("credential state was not preserved: %#v", record)
	}
	if record.CreatedAt != createdAt || record.TitleModelID != modelID.String() {
		t.Fatalf("timestamps/model reference were not preserved: %#v", record)
	}
}

func TestStoreCountAccounts(t *testing.T) {
	t.Parallel()

	store := newStore(queryFake{countAccounts: func(context.Context) (int64, error) {
		return 7, nil
	}})
	count, err := store.CountAccounts(context.Background())
	if err != nil {
		t.Fatalf("CountAccounts() error = %v", err)
	}
	if count != 7 {
		t.Fatalf("CountAccounts() = %d, want 7", count)
	}
}

func TestStoreCreateAccountMapsPersistenceNeutralInput(t *testing.T) {
	t.Parallel()

	userID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	store := newStore(queryFake{createAccount: func(_ context.Context, got dbsqlc.CreateAccountParams) (dbsqlc.CreateAccountRow, error) {
		if got.UserID != userID || got.Username.String != "alice" || !got.Username.Valid {
			t.Fatalf("CreateAccount() identity params = %#v", got)
		}
		if got.Email.Valid || got.AvatarUrl.Valid {
			t.Fatalf("CreateAccount() optional params = %#v", got)
		}
		if got.PasswordHash.String != "hash" || !got.PasswordHash.Valid || got.Role != "member" || !got.IsActive {
			t.Fatalf("CreateAccount() credential params = %#v", got)
		}
		return dbsqlc.CreateAccountRow{
			ID:           userID,
			Username:     got.Username,
			Role:         got.Role,
			IsActive:     pgtype.Bool{Bool: got.IsActive, Valid: true},
			PasswordHash: got.PasswordHash,
		}, nil
	}})

	record, err := store.CreateAccount(context.Background(), persistence.CreateInput{
		UserID: userID.String(), Username: "alice", PasswordHash: "hash", Role: "member", IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	if record.ID != userID.String() || record.Username != "alice" || !record.IsActive {
		t.Fatalf("CreateAccount() record = %#v", record)
	}
}

func TestStoreUpdateProfileMapsNullableTitleModel(t *testing.T) {
	t.Parallel()

	userID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	modelID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	store := newStore(queryFake{updateProfile: func(_ context.Context, got dbsqlc.UpdateAccountProfileParams) (dbsqlc.UpdateAccountProfileRow, error) {
		if got.UserID != userID || got.TitleModelID != modelID {
			t.Fatalf("UpdateAccountProfile() ids = %#v", got)
		}
		if got.Timezone != "Asia/Tokyo" || string(got.Metadata) != `{"onboarding_completed":true}` {
			t.Fatalf("UpdateAccountProfile() profile params = %#v", got)
		}
		return dbsqlc.UpdateAccountProfileRow{ID: userID, Timezone: got.Timezone, TitleModelID: got.TitleModelID}, nil
	}})

	record, err := store.UpdateProfile(context.Background(), persistence.ProfileUpdate{
		UserID: userID.String(), Timezone: "Asia/Tokyo", Metadata: `{"onboarding_completed":true}`, TitleModelID: modelID.String(),
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if record.Timezone != "Asia/Tokyo" || record.TitleModelID != modelID.String() {
		t.Fatalf("UpdateProfile() record = %#v", record)
	}
}

func TestMapQueryErr(t *testing.T) {
	t.Parallel()

	if err := mapQueryErr(pgx.ErrNoRows); !errors.Is(err, persistence.ErrAccountNotFound) {
		t.Fatalf("mapQueryErr(pgx.ErrNoRows) = %v, want ErrAccountNotFound", err)
	} else if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("mapQueryErr(pgx.ErrNoRows) leaked pgx.ErrNoRows: %v", err)
	}
	lastAdmin := &pgconn.PgError{ConstraintName: "team_members_last_active_admin"}
	if err := mapQueryErr(lastAdmin); !errors.Is(err, persistence.ErrLastActiveAdmin) {
		t.Fatalf("mapQueryErr(last admin) = %v, want ErrLastActiveAdmin", err)
	} else {
		var leaked *pgconn.PgError
		if errors.As(err, &leaked) {
			t.Fatalf("mapQueryErr(last admin) leaked pgconn.PgError: %v", err)
		}
	}
	sentinel := errors.New("sentinel")
	if err := mapQueryErr(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("mapQueryErr(sentinel) = %v, want sentinel", err)
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q) error = %v", value, err)
	}
	return id
}
