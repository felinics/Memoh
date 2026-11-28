package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/accounts"
	"github.com/felinics/memoh/internal/acl"
	"github.com/felinics/memoh/internal/agent/application"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/display"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/mcp"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/userruntime"
	"github.com/felinics/memoh/internal/workdir"
	"github.com/felinics/memoh/internal/workspace"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type errorMapperSentinelCase struct {
	name       string
	sentinel   error
	wantCode   apperror.Code
	wantStatus int
	wantFault  apperror.Fault
	// noCause is true for field-required errors that carry no underlying cause.
	noCause bool
}

// assertErrorMapperContract enforces Guard 2 on a handler error translator:
//  1. Every mapped sentinel yields the expected apperror.Code and HTTP status,
//     and preserves the sentinel in its cause chain (unless noCause is set).
//  2. An unknown error with a secret diagnostic renders via NewHTTPErrorHandler
//     as HTTP 500 code="internal" fault="server" with zero secret leakage.
//  3. context.Canceled under a canceled context resolves via errs.Answer to
//     code="canceled" fault="canceled" rather than a 4xx business error.
func renderMappedProblem(t *testing.T, req *http.Request, err error) (int, server.Problem, string) {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))(err, e.NewContext(req, rec))
	var problem server.Problem
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &problem); decodeErr != nil {
		t.Fatalf("decode problem body %q: %v", rec.Body.String(), decodeErr)
	}
	return rec.Code, problem, rec.Body.String()
}

func assertErrorMapperContract(t *testing.T, mapper func(error) error, cases []errorMapperSentinelCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run("sentinel/"+tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("op: %w", tc.sentinel)
			got := mapper(wrapped)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			status, problem, _ := renderMappedProblem(t, req, got)
			if status != tc.wantStatus {
				t.Fatalf("HTTP status = %d, want %d (problem=%+v)", status, tc.wantStatus, problem)
			}
			if problem.Code != string(tc.wantCode) {
				t.Fatalf("problem.Code = %q, want %q", problem.Code, tc.wantCode)
			}
			if tc.wantFault != "" && problem.Fault != tc.wantFault {
				t.Fatalf("problem.Fault = %q, want %q", problem.Fault, tc.wantFault)
			}
			if !tc.noCause {
				if cause := mapperCauseOf(got); !errors.Is(cause, tc.sentinel) {
					t.Fatalf("cause = %v, want errors.Is(_, %v)", cause, tc.sentinel)
				}
			}
		})
	}

	t.Run("unknown_error_is_internal_without_leak", func(t *testing.T) {
		unknown := errors.New("synthetic-cause-SECRET")
		got := mapper(unknown)
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
		status, problem, rawBody := renderMappedProblem(t, req, got)
		if status != http.StatusInternalServerError {
			t.Fatalf("unknown error status = %d, want 500 (problem=%+v)", status, problem)
		}
		if problem.Code != string(apperror.CodeInternal) {
			t.Fatalf("unknown error code = %q, want %q", problem.Code, apperror.CodeInternal)
		}
		if problem.Fault != apperror.FaultServer {
			t.Fatalf("unknown error fault = %q, want %q", problem.Fault, apperror.FaultServer)
		}
		if strings.Contains(rawBody, "SECRET") {
			t.Fatalf("response leaked internal diagnostic: %s", rawBody)
		}
		if !errors.Is(got, unknown) && !errors.Is(mapperCauseOf(got), unknown) {
			t.Fatalf("unknown error lost original cause: %v", got)
		}
	})

	t.Run("context_canceled_propagates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := mapper(fmt.Errorf("downstream: %w", context.Canceled))
		public, fault := errs.Answer(ctx, got)
		if apperror.CodeOf(public) != apperror.CodeCanceled {
			t.Fatalf("canceled answer = %+v, want code %q", public, apperror.CodeCanceled)
		}
		if fault != apperror.FaultCanceled {
			t.Fatalf("canceled fault = %q, want %q", fault, apperror.FaultCanceled)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
		status, problem, _ := renderMappedProblem(t, req, got)
		if status != 499 || problem.Code != string(apperror.CodeCanceled) || problem.Fault != apperror.FaultCanceled {
			t.Fatalf("rendered canceled problem = (%d, %+v), want 499 canceled/canceled", status, problem)
		}
	})
}

func mapperCauseOf(err error) error {
	if cause := apperror.CauseOf(err); cause != nil {
		return cause
	}
	var he *echo.HTTPError
	if errors.As(err, &he) && he.Internal != nil {
		return he.Internal
	}
	return err
}

func TestErrorMapperContracts(t *testing.T) {
	t.Parallel()

	t.Run("workspaceTargetError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, workspaceTargetError, []errorMapperSentinelCase{
			{name: "target not found", sentinel: workspace.ErrWorkspaceTargetNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "runtime offline", sentinel: workspace.ErrRemoteRuntimeOffline, wantCode: apperror.CodeWorkspaceTargetOffline, wantStatus: http.StatusConflict},
			{name: "runtime revoked", sentinel: workspace.ErrRemoteRuntimeRevoked, wantCode: apperror.CodeWorkspaceTargetRevoked, wantStatus: http.StatusConflict},
			{name: "owner mismatch", sentinel: workspace.ErrRemoteRuntimeOwnerMismatch, wantCode: apperror.CodeWorkspaceTargetOwnerMismatch, wantStatus: http.StatusConflict},
			{name: "client update required", sentinel: workspace.ErrRemoteRuntimeClientUpdateNeeded, wantCode: apperror.CodeWorkspaceTargetClientUpdateRequired, wantStatus: http.StatusConflict},
			{name: "bridge unavailable", sentinel: bridge.ErrUnavailable, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
		})
	})

	t.Run("workdirHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, workdirHTTPError, []errorMapperSentinelCase{
			{name: "name required", sentinel: workdir.ErrNameRequired, wantCode: apperror.CodeRequestFieldRequired, wantStatus: http.StatusBadRequest, noCause: true},
			{name: "path required", sentinel: workdir.ErrPathRequired, wantCode: apperror.CodeRequestFieldRequired, wantStatus: http.StatusBadRequest, noCause: true},
			{name: "invalid path", sentinel: workdir.ErrInvalidPath, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "path not found", sentinel: workdir.ErrPathNotFound, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "path not directory", sentinel: workdir.ErrPathNotDirectory, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "target not found", sentinel: workspace.ErrWorkspaceTargetNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "workdir not found", sentinel: workdir.ErrWorkdirNotFound, wantCode: apperror.CodeWorkdirNotFound, wantStatus: http.StatusNotFound},
			{name: "db not found", sentinel: db.ErrNotFound, wantCode: apperror.CodeWorkdirNotFound, wantStatus: http.StatusNotFound},
			{name: "duplicate path", sentinel: workdir.ErrDuplicatePath, wantCode: apperror.CodeWorkdirPathAlreadyExists, wantStatus: http.StatusConflict},
			{name: "archived", sentinel: workdir.ErrWorkdirArchived, wantCode: apperror.CodeWorkdirArchived, wantStatus: http.StatusConflict},
			{name: "runtime offline", sentinel: workspace.ErrRemoteRuntimeOffline, wantCode: apperror.CodeWorkspaceTargetOffline, wantStatus: http.StatusConflict},
			{name: "runtime revoked", sentinel: workspace.ErrRemoteRuntimeRevoked, wantCode: apperror.CodeWorkspaceTargetRevoked, wantStatus: http.StatusConflict},
			{name: "owner mismatch", sentinel: workspace.ErrRemoteRuntimeOwnerMismatch, wantCode: apperror.CodeWorkspaceTargetOwnerMismatch, wantStatus: http.StatusConflict},
			{name: "client update required", sentinel: workspace.ErrRemoteRuntimeClientUpdateNeeded, wantCode: apperror.CodeWorkspaceTargetClientUpdateRequired, wantStatus: http.StatusConflict},
		})
	})

	t.Run("workdirDirectoriesHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, workdirDirectoriesHTTPError, []errorMapperSentinelCase{
			// Hard contract for directory-picker.vue: 503 for offline/unreachable, 403 for forbidden.
			{name: "runtime offline stays 503", sentinel: workspace.ErrRemoteRuntimeOffline, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
			{name: "bridge unavailable stays 503", sentinel: bridge.ErrUnavailable, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
			{name: "path forbidden stays 403", sentinel: workdir.ErrPathForbidden, wantCode: apperror.CodeHTTPForbidden, wantStatus: http.StatusForbidden},
			{name: "target not found", sentinel: workspace.ErrWorkspaceTargetNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "runtime revoked", sentinel: workspace.ErrRemoteRuntimeRevoked, wantCode: apperror.CodeWorkspaceTargetRevoked, wantStatus: http.StatusConflict},
			{name: "owner mismatch", sentinel: workspace.ErrRemoteRuntimeOwnerMismatch, wantCode: apperror.CodeWorkspaceTargetOwnerMismatch, wantStatus: http.StatusConflict},
			{name: "client update required", sentinel: workspace.ErrRemoteRuntimeClientUpdateNeeded, wantCode: apperror.CodeWorkspaceTargetClientUpdateRequired, wantStatus: http.StatusConflict},
		})
	})

	t.Run("workspaceTargetHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, workspaceTargetHTTPError, []errorMapperSentinelCase{
			{name: "runtime not usable", sentinel: workspace.ErrRemoteRuntimeNotUsable, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "target not found", sentinel: workspace.ErrWorkspaceTargetNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "db not found", sentinel: db.ErrNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "runtime offline", sentinel: workspace.ErrRemoteRuntimeOffline, wantCode: apperror.CodeWorkspaceTargetOffline, wantStatus: http.StatusConflict},
			{name: "runtime revoked", sentinel: workspace.ErrRemoteRuntimeRevoked, wantCode: apperror.CodeWorkspaceTargetRevoked, wantStatus: http.StatusConflict},
			{name: "owner mismatch", sentinel: workspace.ErrRemoteRuntimeOwnerMismatch, wantCode: apperror.CodeWorkspaceTargetOwnerMismatch, wantStatus: http.StatusConflict},
			{name: "client update required", sentinel: workspace.ErrRemoteRuntimeClientUpdateNeeded, wantCode: apperror.CodeWorkspaceTargetClientUpdateRequired, wantStatus: http.StatusConflict},
			{name: "bridge unavailable", sentinel: bridge.ErrUnavailable, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
		})
	})

	t.Run("runtimeHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, runtimeHTTPError, []errorMapperSentinelCase{
			{name: "not found", sentinel: db.ErrNotFound, wantCode: apperror.CodeUserRuntimeNotFound, wantStatus: http.StatusNotFound},
			{name: "name taken", sentinel: userruntime.ErrNameTaken, wantCode: apperror.CodeUserRuntimeNameTaken, wantStatus: http.StatusConflict},
		})
	})

	t.Run("terminalWorkspaceClientError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, terminalWorkspaceClientError, []errorMapperSentinelCase{
			{name: "bridge unavailable", sentinel: bridge.ErrUnavailable, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
			{name: "target not found", sentinel: workspace.ErrWorkspaceTargetNotFound, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "runtime not usable", sentinel: workspace.ErrRemoteRuntimeNotUsable, wantCode: apperror.CodeWorkspaceTargetNotFound, wantStatus: http.StatusNotFound},
			{name: "runtime offline", sentinel: workspace.ErrRemoteRuntimeOffline, wantCode: apperror.CodeWorkspaceTargetOffline, wantStatus: http.StatusConflict},
			{name: "runtime revoked", sentinel: workspace.ErrRemoteRuntimeRevoked, wantCode: apperror.CodeWorkspaceTargetRevoked, wantStatus: http.StatusConflict},
			{name: "owner mismatch", sentinel: workspace.ErrRemoteRuntimeOwnerMismatch, wantCode: apperror.CodeWorkspaceTargetOwnerMismatch, wantStatus: http.StatusConflict},
			{name: "client update required", sentinel: workspace.ErrRemoteRuntimeClientUpdateNeeded, wantCode: apperror.CodeWorkspaceTargetClientUpdateRequired, wantStatus: http.StatusConflict},
		})
	})

	t.Run("mapGrantError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, (*BotUserAccessHandler)(nil).mapGrantError, []errorMapperSentinelCase{
			{name: "grant not found", sentinel: bots.ErrGrantNotFound, wantCode: apperror.CodeBotAccessGrantNotFound, wantStatus: http.StatusNotFound},
			{name: "owner user not found", sentinel: bots.ErrOwnerUserNotFound, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "invalid permission", sentinel: bots.ErrInvalidPermission, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "invalid grant subject", sentinel: bots.ErrInvalidGrantSubject, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "grant user required", sentinel: bots.ErrGrantUserRequired, wantCode: apperror.CodeRequestFieldRequired, wantStatus: http.StatusBadRequest, noCause: true},
			{name: "grant owner conflict", sentinel: bots.ErrGrantOwnerConflict, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "grant exists", sentinel: bots.ErrGrantExists, wantCode: apperror.CodeBotAccessGrantExists, wantStatus: http.StatusConflict},
			{name: "bot not found", sentinel: bots.ErrBotNotFound, wantCode: apperror.CodeBotNotFound, wantStatus: http.StatusNotFound},
		})
	})

	t.Run("createBotHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, func(err error) error {
			return createBotHTTPError(err, false)
		}, []errorMapperSentinelCase{
			{name: "template bootstrap failed", sentinel: workspace.ErrWorkspaceTemplateBootstrapFailed, wantCode: apperror.CodeWorkspaceTemplateBootstrapFailed, wantStatus: http.StatusInternalServerError},
			{name: "owner user not found", sentinel: bots.ErrOwnerUserNotFound, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "unknown acl preset", sentinel: acl.ErrUnknownPreset, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "bot name taken", sentinel: bots.ErrBotNameTaken, wantCode: apperror.CodeBotNameTaken, wantStatus: http.StatusConflict, noCause: true},
			{name: "bot name invalid", sentinel: bots.ErrBotNameInvalid, wantCode: apperror.CodeBotNameInvalid, wantStatus: http.StatusBadRequest},
			{name: "bot name reserved", sentinel: bots.ErrBotNameReserved, wantCode: apperror.CodeBotNameInvalid, wantStatus: http.StatusBadRequest},
			{name: "create request deleted", sentinel: bots.ErrCreateRequestDeleted, wantCode: apperror.CodeBotCreateRequestDeleted, wantStatus: http.StatusConflict},
		})
	})

	t.Run("sessionForkError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, sessionForkError, []errorMapperSentinelCase{
			{name: "source not found", sentinel: session.ErrForkSourceNotFound, wantCode: apperror.CodeSessionNotFound, wantStatus: http.StatusNotFound},
			{name: "source not reply", sentinel: session.ErrForkSourceNotReply, wantCode: apperror.CodeSessionForkSourceInvalid, wantStatus: http.StatusConflict},
			{name: "source not chat", sentinel: session.ErrForkSourceNotChat, wantCode: apperror.CodeSessionForkSourceInvalid, wantStatus: http.StatusConflict},
			{name: "external fork unsupported", sentinel: application.ErrExternalForkUnsupported, wantCode: apperror.CodeSessionForkRuntimeUnsupported, wantStatus: http.StatusConflict},
			{name: "external fork anchor missing", sentinel: application.ErrExternalForkAnchorMissing, wantCode: apperror.CodeSessionForkAnchorMissing, wantStatus: http.StatusConflict},
		})
	})

	t.Run("modelWriteError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, func(err error) error {
			return modelWriteError(err, "write model")
		}, []errorMapperSentinelCase{
			{name: "model id taken", sentinel: models.ErrModelIDAlreadyExists, wantCode: apperror.CodeModelIDTaken, wantStatus: http.StatusConflict},
			{name: "model id ambiguous", sentinel: models.ErrModelIDAmbiguous, wantCode: apperror.CodeModelIDAmbiguous, wantStatus: http.StatusConflict},
		})
	})

	t.Run("displayOfferHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, displayOfferHTTPError, []errorMapperSentinelCase{
			{name: "display disabled", sentinel: display.ErrDisplayDisabled, wantCode: apperror.CodeWorkspaceDisplayDisabled, wantStatus: http.StatusConflict},
			{name: "offer required", sentinel: display.ErrOfferRequired, wantCode: apperror.CodeRequestFieldRequired, wantStatus: http.StatusBadRequest, noCause: true},
			{name: "offer type unsupported", sentinel: display.ErrOfferTypeUnsupported, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "offer invalid", sentinel: display.ErrOfferInvalid, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "codec unsupported", sentinel: display.ErrCodecUnsupported, wantCode: apperror.CodeRequestFieldInvalid, wantStatus: http.StatusBadRequest},
			{name: "encoder unavailable", sentinel: display.ErrEncoderUnavailable, wantCode: apperror.CodeHTTPServiceUnavailable, wantStatus: http.StatusServiceUnavailable},
			{name: "display unavailable", sentinel: display.ErrDisplayUnavailable, wantCode: apperror.CodeHTTPServiceUnavailable, wantStatus: http.StatusServiceUnavailable},
		})
	})

	t.Run("fsHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, fsHTTPError, []errorMapperSentinelCase{
			{name: "not found", sentinel: bridge.ErrNotFound, wantCode: apperror.CodeWorkspaceFileNotFound, wantStatus: http.StatusNotFound},
			{name: "bad request", sentinel: bridge.ErrBadRequest, wantCode: apperror.CodeHTTPBadRequest, wantStatus: http.StatusBadRequest},
			{name: "forbidden", sentinel: bridge.ErrForbidden, wantCode: apperror.CodeHTTPForbidden, wantStatus: http.StatusForbidden},
			{name: "unavailable", sentinel: bridge.ErrUnavailable, wantCode: apperror.CodeWorkspaceUnreachable, wantStatus: http.StatusServiceUnavailable},
		})
	})

	t.Run("loginHTTPError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, loginHTTPError, []errorMapperSentinelCase{
			{name: "invalid credentials", sentinel: accounts.ErrInvalidCredentials, wantCode: apperror.CodeAuthInvalidCredentials, wantStatus: http.StatusUnauthorized, wantFault: apperror.FaultClient},
			{name: "inactive account", sentinel: accounts.ErrInactiveAccount, wantCode: apperror.CodeAuthAccountInactive, wantStatus: http.StatusForbidden, wantFault: apperror.FaultClient},
		})
	})

	t.Run("mcpOAuthExchangeError", func(t *testing.T) {
		t.Parallel()
		assertErrorMapperContract(t, mcpOAuthExchangeError, []errorMapperSentinelCase{
			{name: "oauth state invalid", sentinel: mcp.ErrOAuthStateInvalid, wantCode: apperror.CodeOAuthStateInvalid, wantStatus: http.StatusBadRequest, wantFault: apperror.FaultClient},
			{name: "token exchange failed", sentinel: mcp.ErrTokenExchange, wantCode: apperror.CodeMCPOAuthTokenExchangeFailed, wantStatus: http.StatusBadGateway, wantFault: apperror.FaultDependency},
		})
	})
}
