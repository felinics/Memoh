package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type botChannelsTestAdapter struct {
	channelType channel.ChannelType
	configless  bool
}

func (a botChannelsTestAdapter) Type() channel.ChannelType { return a.channelType }

func (a botChannelsTestAdapter) Descriptor() channel.Descriptor {
	return channel.Descriptor{
		Type:        a.channelType,
		DisplayName: string(a.channelType),
		Configless:  a.configless,
	}
}

type botChannelsTestQueries struct {
	dbstore.Queries
	bot        *sqlc.GetBotByIDRow
	configured map[string]sqlc.BotChannelConfig
}

func (*botChannelsTestQueries) SupportsTransactions() bool { return false }

func (q *botChannelsTestQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	if q.bot == nil {
		return sqlc.GetBotByIDRow{}, pgx.ErrNoRows
	}
	return *q.bot, nil
}

func (*botChannelsTestQueries) ListBotUserGrantsForUser(context.Context, sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return nil, nil
}

func (q *botChannelsTestQueries) GetBotChannelConfig(_ context.Context, params sqlc.GetBotChannelConfigParams) (sqlc.BotChannelConfig, error) {
	if row, ok := q.configured[params.ChannelType]; ok {
		return row, nil
	}
	return sqlc.BotChannelConfig{}, pgx.ErrNoRows
}

func newBotChannelsTestHandler(queries *botChannelsTestQueries, accountRole string) *UsersHandler {
	registry := channel.NewRegistry()
	registry.MustRegister(botChannelsTestAdapter{channelType: "telegram"})
	registry.MustRegister(botChannelsTestAdapter{channelType: "discord"})
	registry.MustRegister(botChannelsTestAdapter{channelType: "local", configless: true})

	return NewUsersHandler(
		nil,
		newTestAdminAccountService(accountRole),
		bots.NewService(nil, queries),
		nil,
		channel.NewStore(queries, registry),
		nil,
		registry,
		nil,
	)
}

func TestListBotChannelConfigs(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	ownerID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	otherUserID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	teamID := "22222222-2222-2222-2222-222222222222"

	botRow := testBotRow(botID, map[string]any{})
	botRow.OwnerUserID = testUUID(ownerID)

	t.Run("rejects unauthenticated caller", func(t *testing.T) {
		t.Parallel()

		h := newBotChannelsTestHandler(&botChannelsTestQueries{bot: &botRow}, "user")
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/channels", nil), rec)
		c.SetParamNames("id")
		c.SetParamValues(botID)

		err := h.ListBotChannelConfigs(c)
		var httpErr *echo.HTTPError
		if !errors.As(err, &httpErr) || httpErr.Code != http.StatusUnauthorized {
			t.Fatalf("error = %v, want 401 unauthorized", err)
		}
	})

	t.Run("answers bot_not_found when bot is missing", func(t *testing.T) {
		t.Parallel()

		h := newBotChannelsTestHandler(&botChannelsTestQueries{bot: nil}, "admin")
		e := echo.New()
		rec := httptest.NewRecorder()
		c := testAuthContext(e, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/channels", nil), rec, ownerID)
		c.SetParamNames("id")
		c.SetParamValues(botID)

		requireNotFoundCode(t, h.ListBotChannelConfigs(c), apperror.CodeBotNotFound, true)
	})

	t.Run("forbids caller without manage permission", func(t *testing.T) {
		t.Parallel()

		h := newBotChannelsTestHandler(&botChannelsTestQueries{bot: &botRow}, "user")
		e := echo.New()
		rec := httptest.NewRecorder()
		c := testAuthContext(e, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/channels", nil), rec, otherUserID)
		c.SetParamNames("id")
		c.SetParamValues(botID)

		requireForbidden(t, h.ListBotChannelConfigs(c))
	})

	t.Run("returns empty non-null array when nothing is configured", func(t *testing.T) {
		t.Parallel()

		h := newBotChannelsTestHandler(&botChannelsTestQueries{bot: &botRow}, "user")
		e := echo.New()
		rec := httptest.NewRecorder()
		c := testAuthContext(e, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/channels", nil), rec, ownerID)
		c.SetParamNames("id")
		c.SetParamValues(botID)

		if err := h.ListBotChannelConfigs(c); err != nil {
			t.Fatalf("ListBotChannelConfigs returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Body.String(); got != "{\"items\":[]}\n" {
			t.Fatalf("body = %q, want {\"items\":[]}\\n", got)
		}
	})

	t.Run("returns configured channels and skips unconfigured and configless types", func(t *testing.T) {
		t.Parallel()

		row := sqlc.BotChannelConfig{
			ID:           testUUID("33333333-3333-3333-3333-333333333333"),
			TeamID:       testUUID(teamID),
			BotID:        testUUID(botID),
			ChannelType:  "telegram",
			Credentials:  []byte(`{"token":"secret"}`),
			SelfIdentity: []byte(`{}`),
			Routing:      []byte(`{}`),
			CreatedAt:    pgtype.Timestamptz{Valid: true},
			UpdatedAt:    pgtype.Timestamptz{Valid: true},
		}
		h := newBotChannelsTestHandler(&botChannelsTestQueries{
			bot:        &botRow,
			configured: map[string]sqlc.BotChannelConfig{"telegram": row},
		}, "user")
		e := echo.New()
		rec := httptest.NewRecorder()
		c := testAuthContext(e, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/channels", nil), rec, ownerID)
		c.SetParamNames("id")
		c.SetParamValues(botID)

		if err := h.ListBotChannelConfigs(c); err != nil {
			t.Fatalf("ListBotChannelConfigs returned error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var payload ListBotChannelsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(payload.Items) != 1 || payload.Items[0].ChannelType != channel.ChannelType("telegram") {
			t.Fatalf("items = %+v, want single telegram config", payload.Items)
		}
	})
}
