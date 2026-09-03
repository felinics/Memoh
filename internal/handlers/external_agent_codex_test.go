package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"

	agentfeedback "github.com/felinics/memoh/internal/agent/decision/feedback"
	codexruntime "github.com/felinics/memoh/internal/agent/runtime/codex"
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

func TestCodexDeviceAuthorizePreservesMissingDependencyFeedback(t *testing.T) {
	feedback := agentfeedback.New(agentfeedback.CodeAgentDependencyMissing,
		"dependency_missing", http.StatusConflict, "chat.externalAgent.dependencyMissing",
		"Ask an administrator to install Codex.", map[string]string{"dep_id": "codex", "operation_in_progress": "false"})
	queries := &botAgentsQueries{
		bot:  testBotRow(botAgentsTestBotID, nil),
		rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
	}
	handler := NewExternalAgentCodexHandler(slog.Default(),
		codexLoginFailure{err: fmt.Errorf("start app-server: %w", feedback)},
		botagents.NewService(nil, queries), bots.NewService(nil, queries), newTestAdminAccountService("admin"))
	ctx, rec := botAgentsRequest(t, http.MethodPost, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/codex/login/device/authorize", "")
	ctx.SetParamNames("bot_id", "id")
	ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
	err := handler.AuthorizeDevice(ctx)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("authorize error = %v, want conflict feedback", err)
	}
	ctx.Echo().DefaultHTTPErrorHandler(err, ctx)
	var got agentfeedback.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != feedback.Code || got.Args["dep_id"] != "codex" || got.Args["operation_in_progress"] != "false" {
		t.Fatalf("authorize response lost actionable dependency feedback: %s", rec.Body.String())
	}
}
