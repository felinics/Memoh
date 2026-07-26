package link

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	accesspersistence "github.com/memohai/memoh/domains/api/identity/link/persistence"
	apisqlc "github.com/memohai/memoh/domains/api/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	testUserID     = "11111111-1111-4111-8111-111111111111"
	testIdentityID = "22222222-2222-4222-8222-222222222222"
	testBindingID  = "33333333-3333-4333-8333-333333333333"
	testBotID      = "44444444-4444-4444-8444-444444444444"
)

type queriesStub struct {
	createArg apisqlc.CreateChannelLinkCodeParams
	createRow apisqlc.ApiChannelLinkCode
	getRow    apisqlc.ApiChannelLinkCode
	getErr    error
	redeemArg apisqlc.RedeemChannelLinkCodeParams
	redeemRow apisqlc.ApiUserChannelIdentityBinding
	redeemErr error
	userArg   pgtype.UUID
	userRows  []apisqlc.ApiUserChannelIdentityBinding
	botArg    pgtype.UUID
	botRows   []apisqlc.ApiUserChannelIdentityBinding
	deleteArg apisqlc.DeleteUserChannelIdentityBindingParams
	lookupArg pgtype.UUID
	userIDs   []pgtype.UUID
}

func (q *queriesStub) CreateChannelLinkCode(_ context.Context, arg apisqlc.CreateChannelLinkCodeParams) (apisqlc.ApiChannelLinkCode, error) {
	q.createArg = arg
	return q.createRow, nil
}

func (q *queriesStub) DeleteUserChannelIdentityBinding(_ context.Context, arg apisqlc.DeleteUserChannelIdentityBindingParams) error {
	q.deleteArg = arg
	return nil
}

func (q *queriesStub) GetChannelLinkCodeByToken(context.Context, string) (apisqlc.ApiChannelLinkCode, error) {
	return q.getRow, q.getErr
}

func (q *queriesStub) ListChannelIdentityBindingsForBot(_ context.Context, id pgtype.UUID) ([]apisqlc.ApiUserChannelIdentityBinding, error) {
	q.botArg = id
	return q.botRows, nil
}

func (q *queriesStub) ListChannelIdentityBindingsForUser(_ context.Context, id pgtype.UUID) ([]apisqlc.ApiUserChannelIdentityBinding, error) {
	q.userArg = id
	return q.userRows, nil
}

func (q *queriesStub) ListUserIDsByChannelIdentity(_ context.Context, id pgtype.UUID) ([]pgtype.UUID, error) {
	q.lookupArg = id
	return q.userIDs, nil
}

func (q *queriesStub) RedeemChannelLinkCode(_ context.Context, arg apisqlc.RedeemChannelLinkCodeParams) (apisqlc.ApiUserChannelIdentityBinding, error) {
	q.redeemArg = arg
	return q.redeemRow, q.redeemErr
}

func TestStoreMapsLinkCodeLifecycle(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	userID := mustUUID(t, testUserID)
	identityID := mustUUID(t, testIdentityID)
	bindingID := mustUUID(t, testBindingID)
	queries := &queriesStub{
		createRow: apisqlc.ApiChannelLinkCode{
			Token: "ABCDEFGH", UserID: userID, ChannelType: "telegram",
			ExpiresAt: pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
		getRow: apisqlc.ApiChannelLinkCode{
			ExpiresAt:  pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true},
			ConsumedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
		redeemRow: apisqlc.ApiUserChannelIdentityBinding{
			ID: bindingID, UserID: userID, ChannelIdentityID: identityID,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		},
	}
	store := newStore(queries)

	code, err := store.CreateLinkCode(t.Context(), accesspersistence.CreateLinkCodeCommand{
		Token: "ABCDEFGH", UserID: testUserID, ChannelType: "telegram", ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateLinkCode() error = %v", err)
	}
	if code.UserID != testUserID || code.ChannelType != "telegram" || !code.CreatedAt.Equal(now) {
		t.Fatalf("CreateLinkCode() = %#v", code)
	}
	if queries.createArg.UserID != userID || !queries.createArg.ChannelType.Valid || queries.createArg.ChannelType.String != "telegram" || !queries.createArg.ExpiresAt.Time.Equal(now.Add(time.Minute)) {
		t.Fatalf("CreateChannelLinkCode params = %#v", queries.createArg)
	}

	state, found, err := store.FindLinkCode(t.Context(), "ABCDEFGH")
	if err != nil || !found || !state.Consumed || !state.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("FindLinkCode() = %#v, %v, %v", state, found, err)
	}
	binding, found, err := store.RedeemLinkCode(t.Context(), "ABCDEFGH", testIdentityID)
	if err != nil || !found {
		t.Fatalf("RedeemLinkCode() found = %v, error = %v", found, err)
	}
	if binding.ID != testBindingID || binding.UserID != testUserID || binding.ChannelIdentityID != testIdentityID || !binding.CreatedAt.Equal(now) {
		t.Fatalf("RedeemLinkCode() = %#v", binding)
	}
	if queries.redeemArg.Token != "ABCDEFGH" || queries.redeemArg.ChannelIdentityID != identityID {
		t.Fatalf("RedeemChannelLinkCode params = %#v", queries.redeemArg)
	}
}

func TestStoreMapsBindingsAndIdentityLookup(t *testing.T) {
	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	userID := mustUUID(t, testUserID)
	identityID := mustUUID(t, testIdentityID)
	bindingID := mustUUID(t, testBindingID)
	queries := &queriesStub{
		userRows: []apisqlc.ApiUserChannelIdentityBinding{{
			ID: bindingID, UserID: userID, ChannelIdentityID: identityID,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}},
		botRows: []apisqlc.ApiUserChannelIdentityBinding{{
			ID: bindingID, UserID: userID, ChannelIdentityID: identityID,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}},
		userIDs: []pgtype.UUID{userID, {}},
	}
	store := newStore(queries)

	bindings, err := store.ListBindingsForUser(t.Context(), testUserID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("ListBindingsForUser() = %#v, %v", bindings, err)
	}
	if got := bindings[0]; got.ChannelType != "" || got.ChannelSubjectID != "" || got.ChannelIdentityDisplayName != "" || got.ChannelIdentityAvatarURL != "" || !got.CreatedAt.Equal(now) {
		t.Fatalf("binding = %#v", got)
	}
	if queries.userArg != userID {
		t.Fatalf("user query id = %v", queries.userArg)
	}

	if _, err := store.ListBindingsForBot(t.Context(), testBotID); err != nil {
		t.Fatalf("ListBindingsForBot() error = %v", err)
	}
	if queries.botArg.String() != testBotID {
		t.Fatalf("bot query id = %s", queries.botArg.String())
	}
	ids, err := store.ListUserIDsByChannelIdentity(t.Context(), testIdentityID)
	if err != nil || len(ids) != 1 || ids[0] != testUserID {
		t.Fatalf("ListUserIDsByChannelIdentity() = %v, %v", ids, err)
	}
	if queries.lookupArg != identityID {
		t.Fatalf("identity query id = %v", queries.lookupArg)
	}
	if err := store.DeleteBinding(t.Context(), testUserID, testIdentityID); err != nil {
		t.Fatalf("DeleteBinding() error = %v", err)
	}
	if queries.deleteArg.UserID != userID || queries.deleteArg.ChannelIdentityID != identityID {
		t.Fatalf("delete params = %#v", queries.deleteArg)
	}
}

func TestStoreMapsNoRowsAndRejectsInvalidIDs(t *testing.T) {
	queries := &queriesStub{getErr: pgx.ErrNoRows, redeemErr: pgx.ErrNoRows}
	store := newStore(queries)
	if _, found, err := store.FindLinkCode(t.Context(), "missing"); err != nil || found {
		t.Fatalf("FindLinkCode() found = %v, error = %v", found, err)
	}
	if _, found, err := store.RedeemLinkCode(t.Context(), "missing", testIdentityID); err != nil || found {
		t.Fatalf("RedeemLinkCode() found = %v, error = %v", found, err)
	}
	if _, err := store.ListBindingsForUser(t.Context(), "bad-id"); err == nil {
		t.Fatal("ListBindingsForUser() error = nil")
	}
}

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
