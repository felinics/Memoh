package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	displaypkg "github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/server"
	supermarketclient "github.com/felinics/memoh/internal/supermarket"
)

type loginTestAccountStore struct {
	dbstore.AccountStore
	record dbstore.AccountRecord
	err    error
}

func (s *loginTestAccountStore) GetByIdentity(context.Context, string) (dbstore.AccountRecord, error) {
	return s.record, s.err
}

func (s *loginTestAccountStore) GetByUserID(context.Context, string) (dbstore.AccountRecord, error) {
	return s.record, s.err
}

func TestAuthHandlerLoginMapsCredentialsInactiveAndInternalErrors(t *testing.T) {
	t.Parallel()

	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	cases := []struct {
		name       string
		store      *loginTestAccountStore
		password   string
		wantStatus int
		wantCode   apperror.Code
		wantFault  apperror.Fault
	}{
		{
			name: "wrong password returns 401 auth.invalid_credentials",
			store: &loginTestAccountStore{
				record: dbstore.AccountRecord{
					ID:               "user-1",
					Username:         "alice",
					PasswordHash:     string(hash),
					HasPasswordHash:  true,
					Role:             "member",
					IsActive:         true,
					PrincipalActive:  true,
					MembershipActive: true,
				},
			},
			password:   "wrong-password",
			wantStatus: http.StatusUnauthorized,
			wantCode:   apperror.CodeAuthInvalidCredentials,
			wantFault:  apperror.FaultClient,
		},
		{
			name: "inactive account returns 403 auth.account_inactive",
			store: &loginTestAccountStore{
				record: dbstore.AccountRecord{
					ID:               "user-1",
					Username:         "alice",
					PasswordHash:     string(hash),
					HasPasswordHash:  true,
					Role:             "member",
					IsActive:         false,
					PrincipalActive:  true,
					MembershipActive: true,
				},
			},
			password:   "correct-password",
			wantStatus: http.StatusForbidden,
			wantCode:   apperror.CodeAuthAccountInactive,
			wantFault:  apperror.FaultClient,
		},
		{
			name: "database error returns 500 internal without leaking cause",
			store: &loginTestAccountStore{
				err: errors.New("postgres-SECRET-failure"),
			},
			password:   "correct-password",
			wantStatus: http.StatusInternalServerError,
			wantCode:   apperror.CodeInternal,
			wantFault:  apperror.FaultServer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := NewAuthHandler(
				slog.New(slog.DiscardHandler),
				accounts.NewService(slog.New(slog.DiscardHandler), tc.store),
				"test-jwt-secret",
				time.Hour,
			)
			e := echo.New()
			e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
			h.Register(e)

			body := `{"username":"alice","password":"` + tc.password + `"}`
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/login", strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var problem server.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Code != string(tc.wantCode) || problem.Fault != tc.wantFault {
				t.Fatalf("problem = (%s, %s), want (%s, %s)", problem.Code, problem.Fault, tc.wantCode, tc.wantFault)
			}
			if strings.Contains(rec.Body.String(), "SECRET") {
				t.Fatalf("response leaked internal diagnostic: %s", rec.Body.String())
			}
		})
	}
}

type scheduleAuthzTestQueries struct {
	*memoryCapabilityQueries
}

func (*scheduleAuthzTestQueries) ListBotUserGrantsForUser(context.Context, sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return nil, nil
}

func TestScheduleHandlerAuthorizesBotBeforeLookingUpSchedule(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	ownerID := "22222222-2222-2222-2222-222222222222"
	otherUserID := "33333333-3333-3333-3333-333333333333"
	botUUID, _ := db.ParseUUID(botID)
	ownerUUID, _ := db.ParseUUID(ownerID)

	queries := &scheduleAuthzTestQueries{
		memoryCapabilityQueries: &memoryCapabilityQueries{
			bot: sqlc.GetBotByIDRow{
				ID:          botUUID,
				OwnerUserID: ownerUUID,
			},
		},
	}
	accountStore := &loginTestAccountStore{
		record: dbstore.AccountRecord{
			ID:               otherUserID,
			Role:             "member",
			IsActive:         true,
			PrincipalActive:  true,
			MembershipActive: true,
		},
	}

	// h.service is intentionally nil: if Get/Update/Delete called service.Get
	// before authorizeBotAccess, this test would panic instead of returning 403.
	h := NewScheduleHandler(
		slog.New(slog.DiscardHandler),
		nil,
		bots.NewService(slog.New(slog.DiscardHandler), queries),
		accounts.NewService(slog.New(slog.DiscardHandler), accountStore),
	)

	cases := []struct {
		name   string
		method string
		body   string
		call   func(echo.Context) error
	}{
		{"Get", http.MethodGet, "", h.Get},
		{"Update", http.MethodPut, `{"name":"x"}`, h.Update},
		{"Delete", http.MethodDelete, "", h.Delete},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := echo.New()
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "/bots/"+botID+"/schedule/sched-1", strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			}
			rec := httptest.NewRecorder()
			c := testAuthContext(e, req, rec, otherUserID)
			c.SetParamNames("bot_id", "id")
			c.SetParamValues(botID, "sched-1")

			err := tc.call(c)
			status, problem, _ := renderMappedProblem(t, req, err)
			if status != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403 (problem=%+v)", tc.name, status, problem)
			}
		})
	}
}

func TestPublicMediaReadErrorIsInternalNotBadRequest(t *testing.T) {
	t.Parallel()

	tooLarge := publicMediaTooLargeHTTPError(media.ErrAssetTooLarge)
	var he *echo.HTTPError
	if !errors.As(tooLarge, &he) || he.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ErrAssetTooLarge -> %v, want 413", tooLarge)
	}

	ioErr := errors.New("disk read failed")
	got := publicMediaTooLargeHTTPError(ioErr)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	status, problem, _ := renderMappedProblem(t, req, got)
	if status != http.StatusInternalServerError || problem.Code != string(apperror.CodeInternal) || problem.Fault != apperror.FaultServer {
		t.Fatalf("read error -> (%d, %+v), want 500 internal/server", status, problem)
	}
	if !errors.Is(got, ioErr) {
		t.Fatalf("read error lost cause: %v", got)
	}
}

func TestSupermarketSkillIconValidationFailuresAreDependencyFault(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", sha256.Size*2)
	cases := []struct {
		name        string
		contentType string
		body        string
	}{
		{"unsupported content type", "text/html", "<html>oops</html>"},
		{"empty body", "image/png", ""},
		{"digest mismatch", "image/png", "not-matching-digest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(upstream.Close)

			h := &SupermarketHandler{
				upstream: supermarketclient.NewClient(upstream.URL, upstream.Client()),
				logger:   slog.New(slog.DiscardHandler),
			}
			e := echo.New()
			e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
			h.Register(e)

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/supermarket/artifacts/icon/"+digest, nil))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 (body=%s)", rec.Code, rec.Body.String())
			}
			var problem server.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Fault != apperror.FaultDependency {
				t.Fatalf("problem.Fault = %q, want %q", problem.Fault, apperror.FaultDependency)
			}
		})
	}
}

type failingDisplayManager struct {
	containerWorkspace
	err error
}

func (m *failingDisplayManager) BotDisplayEnabled(context.Context, string) (bool, error) {
	return false, m.err
}

func TestDisplayHandlersPropagateBotDisplayEnabledError(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-1111-1111-111111111111"
	userID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	botUUID, _ := db.ParseUUID(botID)
	queries := &memoryCapabilityQueries{
		bot: sqlc.GetBotByIDRow{ID: botUUID},
	}
	dbErr := errors.New("display settings db timeout")
	h := &ContainerdHandler{
		manager:        &failingDisplayManager{err: dbErr},
		displayService: displaypkg.NewService(slog.New(slog.DiscardHandler), nil),
		botService:     bots.NewService(slog.New(slog.DiscardHandler), queries),
		accountService: accounts.NewService(slog.New(slog.DiscardHandler), testAdminAccountStore{role: "admin"}),
		logger:         slog.New(slog.DiscardHandler),
	}

	t.Run("GetDisplayInfo returns 500 internal", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/container/display", nil)
		rec := httptest.NewRecorder()
		c := testAuthContext(e, req, rec, userID)
		c.SetParamNames("bot_id")
		c.SetParamValues(botID)

		err := h.GetDisplayInfo(c)
		status, problem, _ := renderMappedProblem(t, req, err)
		if status != http.StatusInternalServerError || problem.Code != string(apperror.CodeInternal) {
			t.Fatalf("GetDisplayInfo -> (%d, %+v), want 500 internal", status, problem)
		}
		if !errors.Is(err, dbErr) {
			t.Fatalf("GetDisplayInfo lost dbErr cause: %v", err)
		}
	})

	t.Run("PrepareDisplay emits internal SSE error instead of workspace_display_disabled", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/"+botID+"/container/display/prepare", nil)
		rec := httptest.NewRecorder()
		c := testAuthContext(e, req, rec, userID)
		c.SetParamNames("bot_id")
		c.SetParamValues(botID)

		err := h.PrepareDisplay(c)
		if err == nil {
			t.Fatal("expected PrepareDisplay to return rendered error")
		}
		body := rec.Body.String()
		if strings.Contains(body, string(apperror.CodeWorkspaceDisplayDisabled)) {
			t.Fatalf("PrepareDisplay misreported DB failure as workspace_display_disabled: %s", body)
		}
		if !strings.Contains(body, `"code":"internal"`) {
			t.Fatalf("PrepareDisplay SSE body = %q, want internal error frame", body)
		}
	})
}
