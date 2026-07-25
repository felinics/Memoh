// Package httpfixture holds shared HTTP-surface test helpers for domains/api/http/*.
package httpfixture

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/setting"
	"github.com/memohai/memoh/domains/iam/account"
)

func UUID(value string) pgtype.UUID {
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		panic(err)
	}
	return out
}

func JSON(value map[string]any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func AuthContext(e *echo.Echo, req *http.Request, rec http.ResponseWriter, userID string) echo.Context {
	ctx := e.NewContext(req, rec)
	ctx.Set("user", &jwt.Token{
		Valid: true,
		Claims: jwt.MapClaims{
			"sub":     userID,
			"user_id": userID,
		},
	})
	return ctx
}

type AdminAccountStore struct {
	account.Store
	Role string
}

func (s AdminAccountStore) GetByUserID(_ context.Context, _ string) (account.Record, error) {
	return account.Record{Role: s.Role, IsActive: true}, nil
}

func NewAdminAccountService(role string) *account.Service {
	return account.NewService(nil, AdminAccountStore{Role: role})
}

func BotRow(botID string, metadata map[string]any) bot.Record {
	return bot.Record{
		ID:          botID,
		OwnerUserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		DisplayName: "bot",
		IsActive:    true,
		Status:      bot.BotStatusCreating,
		Metadata:    JSON(metadata),
		CreatedAt:   time.Time{},
		UpdatedAt:   time.Time{},
	}
}

func NewBotService(log *slog.Logger, store interface {
	bot.BotStore
	bot.GrantStore
}, presetAppliers ...bot.ACLPresetApplier,
) *bot.Service {
	return bot.NewService(log, store, store, stubUserReader{}, stubContainerReader{}, presetAppliers...)
}

type stubModelReader struct{}

func (stubModelReader) ModelExists(context.Context, string) (bool, error) { return true, nil }

func (stubModelReader) ListModelIDs(_ context.Context, modelID string) ([]string, error) {
	if modelID == "" {
		return nil, nil
	}
	return []string{modelID}, nil
}

func NewSettingsService(log *slog.Logger, store setting.Store) *setting.Service {
	return setting.NewService(log, store, stubModelReader{}, nil, nil)
}
