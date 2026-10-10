package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/providers"
)

type mcpErrQueries struct {
	dbstore.Queries
	tokenErr   error
	stateErr   error
	createErr  error
	providerEr error
	oauthState error
}

func (q *mcpErrQueries) GetMCPOAuthToken(context.Context, pgtype.UUID) (sqlc.McpOauthToken, error) {
	return sqlc.McpOauthToken{}, q.tokenErr
}

func (q *mcpErrQueries) GetMCPOAuthTokenByState(context.Context, string) (sqlc.McpOauthToken, error) {
	return sqlc.McpOauthToken{}, q.stateErr
}

func (q *mcpErrQueries) CreateMCPConnection(context.Context, sqlc.CreateMCPConnectionParams) (sqlc.McpConnection, error) {
	return sqlc.McpConnection{}, q.createErr
}

func (q *mcpErrQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return sqlc.Provider{}, q.providerEr
}

func (q *mcpErrQueries) GetProviderOAuthTokenByState(context.Context, string) (sqlc.ProviderOauthToken, error) {
	return sqlc.ProviderOauthToken{}, q.oauthState
}

// answered reports what the HTTP boundary would answer for err.
func answered(t *testing.T, err error) (apperror.Code, map[string]string, apperror.Fault) {
	t.Helper()
	answer, fault := errs.Answer(context.Background(), err)
	if answer == nil {
		return apperror.CodeInternal, nil, fault
	}
	return apperror.CodeOf(answer), apperror.ArgsOf(answer), fault
}

func jsonCtx(body string) echo.Context {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	return echo.New().NewContext(req, httptest.NewRecorder())
}

const validUUID = "11111111-1111-1111-1111-111111111111"

func TestMCPOAuthStartAuthorizationErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		id    string
		q     *mcpErrQueries
		code  apperror.Code
		args  map[string]string
		fault apperror.Fault
	}{
		{"not discovered", validUUID, &mcpErrQueries{tokenErr: pgx.ErrNoRows}, apperror.CodeMCPOAuthNotDiscovered, nil, apperror.FaultClient},
		{"database failure is not reported as undiscovered", validUUID, &mcpErrQueries{tokenErr: errors.New("connection reset")}, apperror.CodeInternal, nil, apperror.FaultServer},
		{"bad connection id", "nope", &mcpErrQueries{}, apperror.CodeRequestFieldInvalid, map[string]string{"field": "id"}, apperror.FaultClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mcp.NewOAuthService(nil, tc.q, "").StartAuthorization(context.Background(), tc.id, "", "", "")
			code, args, fault := answered(t, mcpStartAuthorizationError(err))
			if code != tc.code || fault != tc.fault || (tc.args != nil && args["field"] != tc.args["field"]) {
				t.Fatalf("answered %s %v %s, want %s %v %s", code, args, fault, tc.code, tc.args, tc.fault)
			}
		})
	}
}

func TestMCPOAuthExchangeErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		q     *mcpErrQueries
		code  apperror.Code
		fault apperror.Fault
	}{
		{"unknown state", &mcpErrQueries{stateErr: pgx.ErrNoRows}, apperror.CodeOAuthStateInvalid, apperror.FaultClient},
		{"database failure", &mcpErrQueries{stateErr: errors.New("connection reset")}, apperror.CodeInternal, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewMCPOAuthHandler(mcp.NewOAuthService(nil, tc.q, ""), nil, nil, nil)
			err := h.Exchange(jsonCtx(`{"code":"c","state":"s"}`))
			code, _, fault := answered(t, err)
			if code != tc.code || fault != tc.fault {
				t.Fatalf("answered %s %s, want %s %s", code, fault, tc.code, tc.fault)
			}
		})
	}
}

func TestMCPUpsertError(t *testing.T) {
	t.Parallel()
	unique := &pgconn.PgError{Code: "23505"}
	for _, tc := range []struct {
		name  string
		req   mcp.UpsertRequest
		q     *mcpErrQueries
		code  apperror.Code
		args  map[string]string
		fault apperror.Fault
	}{
		{"missing name", mcp.UpsertRequest{URL: "https://x"}, &mcpErrQueries{}, apperror.CodeRequestFieldRequired, map[string]string{"field": "name"}, apperror.FaultClient},
		{"no endpoint", mcp.UpsertRequest{Name: "a"}, &mcpErrQueries{}, apperror.CodeMCPEndpointInvalid, nil, apperror.FaultClient},
		{"both endpoints", mcp.UpsertRequest{Name: "a", URL: "https://x", Command: "c"}, &mcpErrQueries{}, apperror.CodeMCPEndpointInvalid, nil, apperror.FaultClient},
		{"duplicate name", mcp.UpsertRequest{Name: "a", URL: "https://x"}, &mcpErrQueries{createErr: unique}, apperror.CodeMCPNameTaken, map[string]string{"field": "name"}, apperror.FaultClient},
		{"database failure", mcp.UpsertRequest{Name: "a", URL: "https://x"}, &mcpErrQueries{createErr: errors.New("connection reset")}, apperror.CodeInternal, nil, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mcp.NewConnectionService(nil, tc.q).Create(context.Background(), validUUID, tc.req)
			code, args, fault := answered(t, mcpUpsertError(err, "create mcp connection"))
			if code != tc.code || fault != tc.fault || (tc.args != nil && args["field"] != tc.args["field"]) {
				t.Fatalf("answered %s %v %s, want %s %v %s", code, args, fault, tc.code, tc.args, tc.fault)
			}
		})
	}
}

func TestMCPImportEndpointInvalidNamesServer(t *testing.T) {
	t.Parallel()
	svc := mcp.NewConnectionService(nil, &mcpErrQueries{})
	_, err := svc.Import(context.Background(), validUUID, mcp.ImportRequest{MCPServers: map[string]mcp.MCPServerEntry{"broken": {}}})
	code, args, fault := answered(t, mcpUpsertError(err, "import mcp servers"))
	if code != apperror.CodeMCPEndpointInvalid || args["server"] != "broken" || fault != apperror.FaultClient {
		t.Fatalf("answered %s %v %s", code, args, fault)
	}
}

func TestProviderOAuthErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		id    string
		q     *mcpErrQueries
		code  apperror.Code
		fault apperror.Fault
	}{
		{"bad id", "nope", &mcpErrQueries{}, apperror.CodeRequestFieldInvalid, apperror.FaultClient},
		{"missing provider", validUUID, &mcpErrQueries{providerEr: pgx.ErrNoRows}, apperror.CodeProviderNotFound, apperror.FaultClient},
		{"database failure", validUUID, &mcpErrQueries{providerEr: errors.New("connection reset")}, apperror.CodeInternal, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := providers.NewService(nil, tc.q, "")
			_, err := svc.StartOAuthAuthorization(context.Background(), tc.id)
			code, _, fault := answered(t, providerOAuthError(err, "start provider oauth"))
			if code != tc.code || fault != tc.fault {
				t.Fatalf("answered %s %s, want %s %s", code, fault, tc.code, tc.fault)
			}
		})
	}
	t.Run("unsupported provider", func(t *testing.T) {
		t.Parallel()
		code, _, fault := answered(t, providerOAuthError(errs.Wrap(providers.ErrOAuthUnsupported, ""), "start provider oauth"))
		if code != apperror.CodeHTTPBadRequest || fault != apperror.FaultClient {
			t.Fatalf("answered %s %s", code, fault)
		}
	})
}

func TestProviderOAuthCallbackStateErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		err   error
		code  apperror.Code
		fault apperror.Fault
	}{
		{"unknown state", pgx.ErrNoRows, apperror.CodeOAuthStateInvalid, apperror.FaultClient},
		{"database failure", errors.New("connection reset"), apperror.CodeInternal, apperror.FaultServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewProviderOAuthHandler(providers.NewService(nil, &mcpErrQueries{oauthState: tc.err}, ""))
			req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=c&state=s", nil)
			err := h.Callback(echo.New().NewContext(req, httptest.NewRecorder()))
			code, _, fault := answered(t, err)
			if code != tc.code || fault != tc.fault {
				t.Fatalf("answered %s %s, want %s %s", code, fault, tc.code, tc.fault)
			}
		})
	}
}
