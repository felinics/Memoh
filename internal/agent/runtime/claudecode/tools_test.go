package claudecode

import (
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestToolSessionPreservesTrustedSubagentIdentity(t *testing.T) {
	for _, mode := range []string{"chat", "subagent"} {
		t.Run(mode, func(t *testing.T) {
			fence := runtimefence.Fence{BotID: "bot", SessionID: "child", Token: 7}
			ctx := runtimefence.WithContext(t.Context(), fence)
			session := turnToolSession(ctx, external.PromptInput{
				BotID: "bot", ThreadID: "child", RunID: "child-run", SessionMode: mode,
				ActorUserID: "caller", RuntimeOwnerAccountID: "runtime-owner",
				CanRequestUserInput: true, RuntimeMetadata: map[string]any{"is_subagent": mode != "subagent"},
			})
			if session.UserID != "caller" {
				t.Fatalf("trusted caller lost: %q", session.UserID)
			}
			if session.IsSubagent != (mode == "subagent") || session.SessionType != mode {
				t.Fatalf("trusted session mode lost: %+v", session)
			}
			if session.SessionID != "child" || session.RunID != "child-run" || session.RuntimeFence != fence || session.RunContext != ctx {
				t.Fatalf("child run identity lost: %+v", session)
			}
			if !session.CanRequestUserInput || !session.RequireActiveRun || !session.RuntimeActive {
				t.Fatalf("execution approval or active-run guard lost: %+v", session)
			}
		})
	}
}
