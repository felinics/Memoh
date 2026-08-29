package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type contextLifecycleBoundaryQueries struct {
	*contextLifecycleQueryStub
	sessionErr error
	grants     []sqlc.ListBotUserGrantsForUserRow
}

func (q *contextLifecycleBoundaryQueries) GetSessionByID(
	_ context.Context,
	_ pgtype.UUID,
) (sqlc.BotSession, error) {
	return q.session, q.sessionErr
}

func (q *contextLifecycleBoundaryQueries) ListBotUserGrantsForUser(
	_ context.Context,
	_ sqlc.ListBotUserGrantsForUserParams,
) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return q.grants, nil
}

func newContextLifecycleBoundaryHandler(queries dbstore.Queries, accountRole string) *SessionInfoHandler {
	return NewSessionInfoHandler(
		slog.New(slog.DiscardHandler),
		queries,
		bots.NewService(nil, queries),
		newTestAdminAccountService(accountRole),
		nil,
		nil,
	)
}

func TestGetSessionContextLifecycleMapsEndpointBoundaryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		accountRole string
		configure   func(*contextLifecycleBoundaryQueries, echo.Context)
		wantCode    apperror.Code
		wantStatus  int
		wantCause   string
	}{
		{
			name:        "request invalid",
			accountRole: "admin",
			configure: func(_ *contextLifecycleBoundaryQueries, c echo.Context) {
				c.SetParamValues("", lifecycleTestSessionID)
			},
			wantCode:   apperror.CodeContextLifecycleRequestInvalid,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "authentication required",
			accountRole: "admin",
			configure: func(_ *contextLifecycleBoundaryQueries, c echo.Context) {
				c.Set("user", nil)
			},
			wantCode:   apperror.CodeContextLifecycleAuthenticationRequired,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:        "session not found",
			accountRole: "admin",
			configure: func(queries *contextLifecycleBoundaryQueries, _ echo.Context) {
				queries.sessionErr = pgx.ErrNoRows
			},
			wantCode:   apperror.CodeContextLifecycleNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:        "session lookup failed",
			accountRole: "admin",
			configure: func(queries *contextLifecycleBoundaryQueries, _ echo.Context) {
				queries.sessionErr = errors.New("private session database detail")
			},
			wantCode:   apperror.CodeContextLifecycleLoadFailed,
			wantStatus: http.StatusInternalServerError,
			wantCause:  "private session database detail",
		},
		{
			name:        "access denied",
			accountRole: "member",
			configure:   func(_ *contextLifecycleBoundaryQueries, _ echo.Context) {},
			wantCode:    apperror.CodeContextLifecycleAccessDenied,
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "session belongs to another bot",
			accountRole: "admin",
			configure: func(queries *contextLifecycleBoundaryQueries, _ echo.Context) {
				queries.session.BotID = testUUID("33333333-3333-3333-3333-333333333333")
			},
			wantCode:   apperror.CodeContextLifecycleNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:        "session ownership hidden",
			accountRole: "member",
			configure: func(queries *contextLifecycleBoundaryQueries, _ echo.Context) {
				queries.grants = []sqlc.ListBotUserGrantsForUserRow{{Permissions: []byte(`["chat"]`)}}
			},
			wantCode:   apperror.CodeContextLifecycleNotFound,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			queries := &contextLifecycleBoundaryQueries{contextLifecycleQueryStub: newContextLifecycleTestQueries()}
			ctx := newContextLifecycleTestContext(t, "")
			test.configure(queries, ctx)
			err := newContextLifecycleBoundaryHandler(queries, test.accountRole).GetSessionContextLifecycle(ctx)
			problem, ok := apperror.ProblemFrom(err, "request-1")
			if !ok || problem.Code != string(test.wantCode) || problem.Status != test.wantStatus {
				t.Fatalf("error = %#v, problem = %#v, want %s with status %d", err, problem, test.wantCode, test.wantStatus)
			}
			if test.wantCause != "" {
				cause := apperror.CauseOf(err)
				if cause == nil || !strings.Contains(cause.Error(), test.wantCause) {
					t.Fatalf("cause = %v, want private cause containing %q", cause, test.wantCause)
				}
				if strings.Contains(problem.Detail, test.wantCause) {
					t.Fatalf("problem detail exposed private cause: %#v", problem)
				}
			}
		})
	}
}

func TestMapContextLifecycleErrorConvertsLegacyHelperErrors(t *testing.T) {
	t.Parallel()

	private := errors.New("private authorization database detail")
	privateHTTP := echo.NewHTTPError(http.StatusInternalServerError, "private helper database detail")
	tests := []struct {
		name       string
		err        error
		wantCode   apperror.Code
		wantStatus int
		wantCause  error
	}{
		{"bad request", echo.NewHTTPError(http.StatusBadRequest, "private validation detail"), apperror.CodeContextLifecycleRequestInvalid, http.StatusBadRequest, nil},
		{"unauthorized", echo.NewHTTPError(http.StatusUnauthorized, "private auth detail"), apperror.CodeContextLifecycleAuthenticationRequired, http.StatusUnauthorized, nil},
		{"forbidden", echo.NewHTTPError(http.StatusForbidden, "private access detail"), apperror.CodeContextLifecycleAccessDenied, http.StatusForbidden, nil},
		{"not found", echo.NewHTTPError(http.StatusNotFound, "private lookup detail"), apperror.CodeContextLifecycleNotFound, http.StatusNotFound, nil},
		{"legacy helper failure", privateHTTP, apperror.CodeContextLifecycleLoadFailed, http.StatusInternalServerError, privateHTTP},
		{"unexpected", private, apperror.CodeContextLifecycleLoadFailed, http.StatusInternalServerError, private},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := mapContextLifecycleError(test.err)
			problem, ok := apperror.ProblemFrom(err, "request-1")
			if !ok || problem.Code != string(test.wantCode) || problem.Status != test.wantStatus {
				t.Fatalf("error = %#v, problem = %#v, want %s with status %d", err, problem, test.wantCode, test.wantStatus)
			}
			if test.wantCause != nil && !errors.Is(apperror.CauseOf(err), test.wantCause) {
				t.Fatalf("cause = %v, want %v", apperror.CauseOf(err), test.wantCause)
			}
			if strings.Contains(problem.Detail, "private") {
				t.Fatalf("problem detail exposed private helper failure: %#v", problem)
			}
		})
	}
}
