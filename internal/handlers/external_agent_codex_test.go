package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	codexruntime "github.com/felinics/memoh/internal/agent/runtime/codex"
	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
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
