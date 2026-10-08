package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	codexruntime "github.com/felinics/memoh/internal/agent/runtime/codex"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/server"
)

type codexLoginFailure struct {
	codexService
	err error
}

func (s codexLoginFailure) StartChatGPTDeviceLogin(context.Context, string, string) (codexruntime.DeviceLoginStart, error) {
	return codexruntime.DeviceLoginStart{}, s.err
}

func TestCodexDeviceAuthorizeAnswersMissingDependencyWithItsCode(t *testing.T) {
	queries := &botAgentsQueries{
		bot:  testBotRow(botAgentsTestBotID, nil),
		rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
	}
	handler := NewExternalAgentCodexHandler(slog.Default(),
		codexLoginFailure{err: fmt.Errorf("start app-server: %w", &external.DependencyMissingError{DependencyID: "codex"})},
		botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
	ctx, _ := botAgentsRequest(t, http.MethodPost, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/codex/login/device/authorize", "")
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	err := handler.AuthorizeDevice(ctx)
	if got := apperror.CodeOf(err); got != apperror.CodeAgentDependencyMissing {
		t.Fatalf("authorize code = %q, want %s: %v", got, apperror.CodeAgentDependencyMissing, err)
	}
	args := apperror.ArgsOf(err)
	if args["dep_id"] != "codex" || args["operation_in_progress"] != "false" {
		t.Fatalf("authorize error lost the dependency args: %v", args)
	}
}

// Starting a device login answers a runtime failure with its own code, and
// any other error as the runtime being unavailable.
func TestCodexDeviceAuthorizeAnswersRuntimeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, http.StatusUnprocessableEntity},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired, http.StatusConflict},
		{"runtime unavailable", external.Unavailable(errors.New("bridge refused")), apperror.CodeExternalRuntimeUnavailable, http.StatusServiceUnavailable},
		{"plain error", errors.New("login/account/start: stream closed"), apperror.CodeExternalRuntimeUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newCodexDeviceTestHandler(codexLoginFailure{err: fmt.Errorf("start login: %w", tc.err)})
			ctx := codexDeviceRequest(t, "authorize", "")
			problem, ok := server.ProblemFrom(handler.AuthorizeDevice(ctx), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("AuthorizeDevice() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}

type codexDeviceCompletion struct {
	codexService
	err error
}

func (codexDeviceCompletion) PollDeviceLogin(string, string, string) codexruntime.DeviceLoginStatus {
	return codexruntime.DeviceLoginStatus{Status: "success"}
}

func (s codexDeviceCompletion) CompleteChatGPTDeviceLogin(context.Context, string, string, string, string) error {
	return s.err
}

// Saving a finished device login answers a runtime failure with its own code;
// anything else, a missing dependency included, is a materialization failure.
func TestCodexDevicePollAnswersCompletionFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"credential revoked", external.CredentialError(agentcredential.ErrRevoked), apperror.CodeAgentCredentialRevoked, http.StatusConflict},
		{"encryption unavailable", external.CredentialError(agentcredential.ErrEncryptionUnavailable), apperror.CodeAgentCredentialEncryptionUnavailable, http.StatusServiceUnavailable},
		{"runtime unavailable", external.Unavailable(errors.New("bridge refused")), apperror.CodeExternalRuntimeUnavailable, http.StatusServiceUnavailable},
		{"missing dependency", &external.DependencyMissingError{DependencyID: "codex"}, apperror.CodeAgentCredentialMaterializationFailed, http.StatusInternalServerError},
		{"plain error", errors.New("read auth.json: no such file"), apperror.CodeAgentCredentialMaterializationFailed, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newCodexDeviceTestHandler(codexDeviceCompletion{err: fmt.Errorf("complete login: %w", tc.err)})
			ctx := codexDeviceRequest(t, "poll", `{"login_id":"login-1"}`)
			problem, ok := server.ProblemFrom(handler.PollDevice(ctx), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("PollDevice() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}

func newCodexDeviceTestHandler(driver codexService) *ExternalAgentCodexHandler {
	queries := &botAgentsQueries{
		bot:  testBotRow(botAgentsTestBotID, nil),
		rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
	}
	return NewExternalAgentCodexHandler(slog.Default(), driver,
		botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
}

func codexDeviceRequest(t *testing.T, action, body string) echo.Context {
	t.Helper()
	ctx, _ := botAgentsRequest(t, http.MethodPost, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/codex/login/device/"+action, body)
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	return ctx
}

type codexUsageFailure struct {
	codexService
	err error
}

func (s codexUsageFailure) AccountUsage(context.Context, string, string) (codexruntime.AccountUsage, error) {
	return codexruntime.AccountUsage{}, s.err
}

func TestCodexUsageAnswersFailuresWithTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"sign-in expired", codexruntime.ErrUsageSignInExpired, apperror.CodeAgentCredentialUsageAuthExpired, http.StatusConflict},
		{"usage unavailable", fmt.Errorf("%w: status 500: body", codexruntime.ErrUsageUnavailable), apperror.CodeAgentCredentialUsageUnavailable, http.StatusBadGateway},
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, http.StatusUnprocessableEntity},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newCodexDeviceTestHandler(codexUsageFailure{err: tc.err})
			ctx, _ := botAgentsRequest(t, http.MethodGet, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/codex/usage", "")
			ctx.SetParamNames("bot_id", "id")
			ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
			problem, ok := server.ProblemFrom(handler.Usage(ctx), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("Usage() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}
