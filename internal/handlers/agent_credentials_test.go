package handlers

import (
	"net/http"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Disconnecting a credential purges it from the runtime first, and answers a
// failed purge the way deleting the Agent does.
func TestAgentCredentialHandlerDeleteAnswersPurgeFailures(t *testing.T) {
	for _, tc := range purgeFailures {
		t.Run(tc.name, func(t *testing.T) {
			queries := &botAgentsQueries{
				bot:  testBotRow(botAgentsTestBotID, nil),
				rows: []sqlc.BotAgent{botAgentRow(botAgentsTestCodexID, botagents.RuntimeCodex, true)},
			}
			handler := NewAgentCredentialHandler(nil, botagents.NewService(nil, queries), bots.NewService(nil, queries),
				newTestAdminAccountService("admin"), external.Drivers{purgeDriver{
					plainDriver: plainDriver{runtimeType: botagents.RuntimeCodex},
					err:         tc.err,
				}})
			ctx, _ := botAgentsRequest(t, http.MethodDelete, "/bots/"+botAgentsTestBotID+"/agents/"+botAgentsTestCodexID+"/credential", "")
			ctx.SetParamNames("bot_id", "id")
			ctx.SetParamValues(botAgentsTestBotID, botAgentsTestCodexID)
			problem, ok := apperror.ProblemFrom(handler.Delete(ctx), "")
			if !ok || problem.Code != string(tc.code) || problem.Status != tc.status {
				t.Fatalf("Delete() problem = %+v, want %d %s", problem, tc.status, tc.code)
			}
		})
	}
}
