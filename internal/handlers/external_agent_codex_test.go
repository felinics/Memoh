package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	codexruntime "github.com/felinics/memoh/internal/agent/runtime/external/codex"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/server"
)

type codexUsageFailure struct {
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
		{"usage unavailable", fmt.Errorf("%w: status 500: body", codexruntime.ErrUsageUnavailable), apperror.CodeAgentCredentialUsageUnavailable, http.StatusBadGateway},
		{"credential incompatible", external.CredentialError(agentcredential.ErrIncompatible), apperror.CodeAgentCredentialIncompatible, http.StatusUnprocessableEntity},
		{"auth required", external.Fail(external.FailureAuthRequired, external.ErrAuthRequired), apperror.CodeExternalRuntimeAuthRequired, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := &botAgentsQueries{
				bot:  testBotRow(botAgentsTestBotID, nil),
				rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
			}
			handler := NewExternalAgentCodexHandler(slog.Default(), codexUsageFailure{err: tc.err},
				botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
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
