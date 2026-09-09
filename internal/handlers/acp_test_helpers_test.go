package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

func testUUID(value string) pgtype.UUID {
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		panic(err)
	}
	return out
}

func testJSON(value map[string]any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func testAuthContext(e *echo.Echo, req *http.Request, rec http.ResponseWriter, userID string) echo.Context {
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

func newTestAdminAccountService(role string) *accounts.Service {
	return accounts.NewService(nil, testAdminAccountStore{role: role})
}

type testAdminAccountStore struct {
	dbstore.AccountStore
	role string
}

func (s testAdminAccountStore) GetByUserID(_ context.Context, _ string) (dbstore.AccountRecord, error) {
	return dbstore.AccountRecord{Role: s.role, IsActive: true}, nil
}
