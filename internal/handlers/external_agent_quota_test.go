package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/server"
)

// quotaFailure is a runtime whose account usage read fails.
type quotaFailure struct {
	err error
}

func (quotaFailure) RuntimeType() string { return botagents.RuntimeCodex }

func (quotaFailure) Caps() external.Caps {
	return external.Caps{}
}

func (quotaFailure) Prompt(context.Context, external.PromptInput) (external.PromptResult, error) {
	return external.PromptResult{}, nil
}

func (s quotaFailure) Quota(context.Context, string, string) (external.Quota, error) {
	return external.Quota{}, s.err
}

func TestQuotaAnswersFailuresWithTheirCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   apperror.Code
		status int
	}{
		{"usage unavailable", fmt.Errorf("%w: status 500: body", external.ErrQuotaUnavailable), apperror.CodeAgentCredentialUsageUnavailable, http.StatusBadGateway},
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, http.StatusUnprocessableEntity},
		{"auth required", external.Fail(external.FailureAuthRequired, nil), apperror.CodeExternalRuntimeAuthRequired, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := &botAgentsQueries{
				bot:  testBotRow(botAgentsTestBotID, nil),
				rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
			}
			runtimes, err := external.NewRegistry(quotaFailure{err: tc.err})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewExternalAgentLoginHandler(slog.Default(), runtimes,
				botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
			ctx, _ := botAgentsRequest(t, http.MethodGet, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/quota", "")
			ctx.SetParamNames("bot_id", "id")
			ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
			problem, ok := server.ProblemFrom(handler.Quota(ctx), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("Quota() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}
