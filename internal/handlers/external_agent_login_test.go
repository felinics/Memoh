package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/server"
)

// loginTestDriver is a direct runtime whose sign-in answers with fixed errors.
type loginTestDriver struct {
	startErr error
	pollErr  error
}

func (loginTestDriver) RuntimeType() string { return botagents.RuntimeCodex }

func (loginTestDriver) Caps() external.Caps {
	return external.Caps{}
}

func (loginTestDriver) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	return external.PromptResult{}, errors.New("not a turn")
}

func (d loginTestDriver) StartDeviceLogin(context.Context, string, string) (external.DeviceLoginStart, error) {
	if d.startErr != nil {
		return external.DeviceLoginStart{}, fmt.Errorf("start login: %w", d.startErr)
	}
	return external.DeviceLoginStart{LoginID: "login-1", UserCode: "ABCD-1234", VerificationURL: "https://example.test/device"}, nil
}

func (d loginTestDriver) PollDeviceLogin(context.Context, string, string, string, string) (external.DeviceLoginStatus, error) {
	if d.pollErr != nil {
		return external.DeviceLoginStatus{}, fmt.Errorf("complete login: %w", d.pollErr)
	}
	return external.DeviceLoginStatus{Status: "success"}, nil
}

func (loginTestDriver) CancelDeviceLogin(context.Context, string, string, string) error { return nil }

func newLoginTestHandler(driver external.Driver) *ExternalAgentLoginHandler {
	queries := &botAgentsQueries{
		bot:  testBotRow(botAgentsTestBotID, nil),
		rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
	}
	return NewExternalAgentLoginHandler(slog.Default(), testRuntimeRegistry(driver),
		botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
}

func deviceLoginRequest(t *testing.T, action, body string) echo.Context {
	t.Helper()
	ctx, _ := botAgentsRequest(t, http.MethodPost, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/login/device/"+action, body)
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	return ctx
}

func TestDeviceAuthorizeAnswersMissingDependencyWithItsCode(t *testing.T) {
	handler := newLoginTestHandler(loginTestDriver{startErr: &external.DependencyMissingError{DependencyID: "codex"}})
	err := handler.AuthorizeDevice(deviceLoginRequest(t, "authorize", ""))
	if got := apperror.CodeOf(err); got != apperror.CodeAgentDependencyMissing {
		t.Fatalf("authorize code = %q, want %s: %v", got, apperror.CodeAgentDependencyMissing, err)
	}
	args := apperror.ArgsOf(err)
	if args["dep_id"] != "codex" || args["operation_in_progress"] != "false" {
		t.Fatalf("authorize error lost the dependency args: %v", args)
	}
}

// Starting a sign-in answers a runtime failure with its own code, and any
// other error as the runtime being unavailable.
func TestDeviceAuthorizeAnswersRuntimeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, http.StatusUnprocessableEntity},
		{"auth required", external.Fail(external.FailureAuthRequired, nil), apperror.CodeExternalRuntimeAuthRequired, http.StatusConflict},
		{"agent failed", external.Upstream(errors.New("bridge refused")), apperror.CodeExternalRuntimeReported, http.StatusBadGateway},
		{"plain error", errors.New("login/account/start: stream closed"), apperror.CodeExternalRuntimeUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newLoginTestHandler(loginTestDriver{startErr: tc.err})
			problem, ok := server.ProblemFrom(handler.AuthorizeDevice(deviceLoginRequest(t, "authorize", "")), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("AuthorizeDevice() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}

// Storing a finished sign-in answers a runtime failure with its own code;
// anything else, a missing dependency included, is a materialization failure.
func TestDevicePollAnswersCompletionFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"credential revoked", external.CredentialError(agentcredential.ErrRevoked), apperror.CodeAgentCredentialRevoked, http.StatusConflict},
		{"encryption unavailable", external.CredentialError(agentcredential.ErrEncryptionUnavailable), apperror.CodeAgentCredentialEncryptionUnavailable, http.StatusServiceUnavailable},
		{"agent failed", external.Upstream(errors.New("bridge refused")), apperror.CodeExternalRuntimeReported, http.StatusBadGateway},
		{"missing dependency", &external.DependencyMissingError{DependencyID: "codex"}, apperror.CodeAgentCredentialMaterializationFailed, http.StatusInternalServerError},
		{"plain error", errors.New("read auth.json: no such file"), apperror.CodeAgentCredentialMaterializationFailed, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newLoginTestHandler(loginTestDriver{pollErr: tc.err})
			problem, ok := server.ProblemFrom(handler.PollDevice(deviceLoginRequest(t, "poll", `{"login_id":"login-1"}`)), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("PollDevice() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}

// A runtime without an in-process sign-in has no such routes.
func TestDeviceLoginRefusesARuntimeWithoutOne(t *testing.T) {
	queries := &botAgentsQueries{
		bot:  testBotRow(botAgentsTestBotID, nil),
		rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeClaudeCode, true)},
	}
	handler := NewExternalAgentLoginHandler(slog.Default(), testRuntimeRegistry(loginTestDriver{}),
		botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
	err := handler.AuthorizeDevice(deviceLoginRequest(t, "authorize", ""))
	if got := apperror.CodeOf(err); got != apperror.CodeBotAgentNotFound {
		t.Fatalf("authorize code = %q, want %s: %v", got, apperror.CodeBotAgentNotFound, err)
	}
}
