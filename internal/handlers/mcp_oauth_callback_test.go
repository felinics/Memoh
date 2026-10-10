package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/mcp"
)

type mcpOAuthStateQueries struct {
	dbstore.Queries
	err error
}

func (q *mcpOAuthStateQueries) GetMCPOAuthTokenByState(context.Context, string) (sqlc.McpOauthToken, error) {
	return sqlc.McpOauthToken{}, q.err
}

func TestMCPOAuthCallbackFailureRendersPageAndReturnsCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("state lookup failed: sensitive detail")
	handler := NewMCPOAuthHandler(mcp.NewOAuthService(nil, &mcpOAuthStateQueries{err: cause}, ""), nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/oauth/mcp/callback?code=c&state=s", nil)
	rec := httptest.NewRecorder()

	err := handler.Callback(echo.New().NewContext(req, rec))

	if !errors.Is(err, cause) {
		t.Fatalf("Callback() error = %v, want the callback cause", err)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "authorization could not be completed") {
		t.Fatalf("response = %d %q, want the 400 error page", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sensitive detail") {
		t.Fatalf("page carries the cause: %q", rec.Body.String())
	}
}
