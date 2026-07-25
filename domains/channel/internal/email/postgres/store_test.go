package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

const (
	providerID = "a6692f30-51c2-4f6a-acb8-948f86349a24"
	userID     = "e388086c-085d-43dc-8d1e-c32bc4002795"
	botID      = "29d1a891-6a33-4263-b49a-9f7bc4b45bb4"
	bindingID  = "db81d680-34ec-4310-a79a-66c22c5e17f7"
	outboxID   = "46c909fb-2870-452b-8bd7-92e50495372e"
)

func mustUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type providerQueriesStub struct {
	providerQueries
	row          channelsqlc.ChannelEmailProvider
	createArg    channelsqlc.CreateEmailProviderParams
	listProvider string
	findErr      error
}

func (q *providerQueriesStub) CreateEmailProvider(_ context.Context, arg channelsqlc.CreateEmailProviderParams) (channelsqlc.ChannelEmailProvider, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *providerQueriesStub) ListEmailProvidersByProvider(_ context.Context, provider string) ([]channelsqlc.ChannelEmailProvider, error) {
	q.listProvider = provider
	return []channelsqlc.ChannelEmailProvider{q.row}, nil
}

func (q *providerQueriesStub) GetEmailProviderByID(context.Context, pgtype.UUID) (channelsqlc.ChannelEmailProvider, error) {
	return q.row, q.findErr
}

func TestProviderStoreMapsCommandsRowsAndNotFound(t *testing.T) {
	t.Parallel()
	createdAt := time.Date(2026, time.July, 24, 1, 2, 3, 0, time.UTC)
	queries := &providerQueriesStub{row: channelsqlc.ChannelEmailProvider{
		ID: mustUUID(t, providerID), UserID: mustUUID(t, userID), Name: "Primary",
		Provider: "gmail", Config: []byte(`{"address":"person@example.com"}`),
		CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: createdAt.Add(time.Minute), Valid: true},
	}}
	store := NewProviderStoreWithQueries(queries)

	record, err := store.CreateProvider(t.Context(), emailport.CreateProviderInput{
		UserID: userID, Name: "Primary", Provider: "gmail", Config: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if queries.createArg.UserID != mustUUID(t, userID) || queries.createArg.Provider != "gmail" {
		t.Fatalf("CreateEmailProvider params = %+v", queries.createArg)
	}
	if record.ID != providerID || record.UserID != userID || !record.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreateProvider() = %+v", record)
	}
	record.Config[0] = 'x'
	if queries.row.Config[0] == 'x' {
		t.Fatal("ProviderRecord.Config aliases generated row storage")
	}

	if _, err := store.ListProviders(t.Context(), "gmail"); err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if queries.listProvider != "gmail" {
		t.Fatalf("ListEmailProvidersByProvider provider = %q", queries.listProvider)
	}

	queries.findErr = pgx.ErrNoRows
	_, err = store.FindProvider(t.Context(), providerID)
	if !errors.Is(err, emailport.ErrNotFound) {
		t.Fatalf("FindProvider() error = %v, want emailport.ErrNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("FindProvider() error = %v, leaked pgx.ErrNoRows", err)
	}
}

type bindingQueriesStub struct {
	bindingQueries
	row        channelsqlc.ChannelBotEmailBinding
	createArg  channelsqlc.CreateBotEmailBindingParams
	readableID pgtype.UUID
}

func (q *bindingQueriesStub) CreateBotEmailBinding(_ context.Context, arg channelsqlc.CreateBotEmailBindingParams) (channelsqlc.ChannelBotEmailBinding, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *bindingQueriesStub) ListReadableBindingsByProvider(_ context.Context, id pgtype.UUID) ([]channelsqlc.ChannelBotEmailBinding, error) {
	q.readableID = id
	return []channelsqlc.ChannelBotEmailBinding{q.row}, nil
}

func TestBindingStoreMapsCommandsAndRows(t *testing.T) {
	t.Parallel()
	queries := &bindingQueriesStub{row: channelsqlc.ChannelBotEmailBinding{
		ID: mustUUID(t, bindingID), BotID: mustUUID(t, botID), EmailProviderID: mustUUID(t, providerID),
		EmailAddress: "person@example.com", CanRead: true, CanWrite: true, Config: []byte(`{}`),
	}}
	store := NewBindingStoreWithQueries(queries)

	record, err := store.CreateBinding(t.Context(), emailport.CreateBindingInput{
		BotID: botID, EmailProviderID: providerID, EmailAddress: "person@example.com",
		CanRead: true, CanWrite: true, Config: []byte(`{"folder":"inbox"}`),
	})
	if err != nil {
		t.Fatalf("CreateBinding() error = %v", err)
	}
	if queries.createArg.BotID != mustUUID(t, botID) || queries.createArg.EmailProviderID != mustUUID(t, providerID) {
		t.Fatalf("CreateBotEmailBinding params = %+v", queries.createArg)
	}
	if record.ID != bindingID || record.BotID != botID || !record.CanRead {
		t.Fatalf("CreateBinding() = %+v", record)
	}

	if _, err := store.ListReadableBindings(t.Context(), providerID); err != nil {
		t.Fatalf("ListReadableBindings() error = %v", err)
	}
	if queries.readableID != mustUUID(t, providerID) {
		t.Fatalf("ListReadableBindingsByProvider id = %v", queries.readableID)
	}
}

type outboxQueriesStub struct {
	outboxQueries
	row       channelsqlc.ChannelEmailOutbox
	createArg channelsqlc.CreateEmailOutboxParams
	listArg   channelsqlc.ListEmailOutboxByBotParams
	sentArg   channelsqlc.UpdateEmailOutboxSentParams
	failedArg channelsqlc.UpdateEmailOutboxFailedParams
}

func (q *outboxQueriesStub) CreateEmailOutbox(_ context.Context, arg channelsqlc.CreateEmailOutboxParams) (channelsqlc.ChannelEmailOutbox, error) {
	q.createArg = arg
	return q.row, nil
}

func (q *outboxQueriesStub) ListEmailOutboxByBot(_ context.Context, arg channelsqlc.ListEmailOutboxByBotParams) ([]channelsqlc.ChannelEmailOutbox, error) {
	q.listArg = arg
	return []channelsqlc.ChannelEmailOutbox{q.row}, nil
}

func (*outboxQueriesStub) CountEmailOutboxByBot(context.Context, pgtype.UUID) (int64, error) {
	return 1, nil
}

func (q *outboxQueriesStub) UpdateEmailOutboxSent(_ context.Context, arg channelsqlc.UpdateEmailOutboxSentParams) error {
	q.sentArg = arg
	return nil
}

func (q *outboxQueriesStub) UpdateEmailOutboxFailed(_ context.Context, arg channelsqlc.UpdateEmailOutboxFailedParams) error {
	q.failedArg = arg
	return nil
}

func TestOutboxStoreMapsCommandsRowsAndPagination(t *testing.T) {
	t.Parallel()
	sentAt := time.Date(2026, time.July, 24, 2, 3, 4, 0, time.UTC)
	queries := &outboxQueriesStub{row: channelsqlc.ChannelEmailOutbox{
		ID: mustUUID(t, outboxID), ProviderID: mustUUID(t, providerID), BotID: mustUUID(t, botID),
		ToAddresses: []byte(`["to@example.com"]`), Attachments: []byte(`[]`), BodyHtml: "<p>body</p>",
		Status: "sent", SentAt: pgtype.Timestamptz{Time: sentAt, Valid: true},
	}}
	store := NewOutboxStoreWithQueries(queries)

	record, err := store.CreateOutbox(t.Context(), emailport.CreateOutboxInput{
		ProviderID: providerID, BotID: botID, ToAddresses: []byte(`["to@example.com"]`),
		BodyHTML: "<p>body</p>", Attachments: []byte(`[]`), Status: "pending",
	})
	if err != nil {
		t.Fatalf("CreateOutbox() error = %v", err)
	}
	if queries.createArg.ProviderID != mustUUID(t, providerID) || queries.createArg.BodyHtml != "<p>body</p>" {
		t.Fatalf("CreateEmailOutbox params = %+v", queries.createArg)
	}
	if record.ID != outboxID || !record.SentAt.Equal(sentAt) {
		t.Fatalf("CreateOutbox() = %+v", record)
	}

	if _, err := store.ListOutboxByBot(t.Context(), botID, 25, 50); err != nil {
		t.Fatalf("ListOutboxByBot() error = %v", err)
	}
	if queries.listArg.Lim != 25 || queries.listArg.Off != 50 {
		t.Fatalf("ListEmailOutboxByBot params = %+v", queries.listArg)
	}
	if err := store.MarkOutboxSent(t.Context(), outboxID, "message-1"); err != nil {
		t.Fatalf("MarkOutboxSent() error = %v", err)
	}
	if queries.sentArg.MessageID != "message-1" {
		t.Fatalf("UpdateEmailOutboxSent params = %+v", queries.sentArg)
	}
	if err := store.MarkOutboxFailed(t.Context(), outboxID, "delivery failed"); err != nil {
		t.Fatalf("MarkOutboxFailed() error = %v", err)
	}
	if queries.failedArg.Error != "delivery failed" {
		t.Fatalf("UpdateEmailOutboxFailed params = %+v", queries.failedArg)
	}
}

type oauthQueriesStub struct {
	oauthQueries
	row       channelsqlc.ChannelEmailOauthToken
	upsertArg channelsqlc.UpsertEmailOAuthTokenParams
	stateArg  channelsqlc.UpdateEmailOAuthStateParams
	state     string
	deleteID  pgtype.UUID
}

func (q *oauthQueriesStub) UpsertEmailOAuthToken(_ context.Context, arg channelsqlc.UpsertEmailOAuthTokenParams) (channelsqlc.ChannelEmailOauthToken, error) {
	q.upsertArg = arg
	return q.row, nil
}

func (q *oauthQueriesStub) UpdateEmailOAuthState(_ context.Context, arg channelsqlc.UpdateEmailOAuthStateParams) error {
	q.stateArg = arg
	return nil
}

func (q *oauthQueriesStub) GetEmailOAuthTokenByState(_ context.Context, state string) (channelsqlc.ChannelEmailOauthToken, error) {
	q.state = state
	return q.row, nil
}

func (q *oauthQueriesStub) DeleteEmailOAuthToken(_ context.Context, id pgtype.UUID) error {
	q.deleteID = id
	return nil
}

func TestOAuthTokenStoreMapsCommandsAndRows(t *testing.T) {
	t.Parallel()
	expiresAt := time.Date(2026, time.July, 24, 3, 4, 5, 0, time.UTC)
	queries := &oauthQueriesStub{row: channelsqlc.ChannelEmailOauthToken{
		EmailProviderID: mustUUID(t, providerID), EmailAddress: "person@example.com",
		AccessToken: "access", RefreshToken: "refresh", Scope: "mail.read",
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}}
	store := NewOAuthTokenStoreWithQueries(queries)

	if err := store.Save(t.Context(), emailport.OAuthToken{
		ProviderID: providerID, EmailAddress: "person@example.com",
		AccessToken: "access", RefreshToken: "refresh", ExpiresAt: expiresAt, Scope: "mail.read",
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if queries.upsertArg.EmailProviderID != mustUUID(t, providerID) ||
		!queries.upsertArg.ExpiresAt.Valid || !queries.upsertArg.ExpiresAt.Time.Equal(expiresAt) {
		t.Fatalf("UpsertEmailOAuthToken params = %+v", queries.upsertArg)
	}

	token, err := store.GetByState(t.Context(), "oauth-state")
	if err != nil {
		t.Fatalf("GetByState() error = %v", err)
	}
	if queries.state != "oauth-state" || token.ProviderID != providerID || !token.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("GetByState() = %+v, state = %q", token, queries.state)
	}
	if err := store.SetPendingState(t.Context(), providerID, "pending"); err != nil {
		t.Fatalf("SetPendingState() error = %v", err)
	}
	if queries.stateArg.State != "pending" {
		t.Fatalf("UpdateEmailOAuthState params = %+v", queries.stateArg)
	}
	if err := store.Delete(t.Context(), providerID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if queries.deleteID != mustUUID(t, providerID) {
		t.Fatalf("DeleteEmailOAuthToken id = %v", queries.deleteID)
	}
}

func TestStoresRejectInvalidUUIDs(t *testing.T) {
	t.Parallel()
	if _, err := NewProviderStoreWithQueries(&providerQueriesStub{}).FindProvider(t.Context(), "invalid"); err == nil {
		t.Fatal("FindProvider() error = nil, want invalid UUID")
	}
	if _, err := NewBindingStoreWithQueries(&bindingQueriesStub{}).FindBinding(t.Context(), "invalid"); err == nil {
		t.Fatal("FindBinding() error = nil, want invalid UUID")
	}
	if _, err := NewOutboxStoreWithQueries(&outboxQueriesStub{}).FindOutbox(t.Context(), "invalid"); err == nil {
		t.Fatal("FindOutbox() error = nil, want invalid UUID")
	}
	if _, err := NewOAuthTokenStoreWithQueries(&oauthQueriesStub{}).Get(t.Context(), "invalid"); err == nil {
		t.Fatal("Get() error = nil, want invalid UUID")
	}
}
