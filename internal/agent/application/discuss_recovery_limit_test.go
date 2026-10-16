package application

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestDiscussExhaustedRecoveryOnlyAttemptsAdmission(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider", true: "channel"}[overflow], func(t *testing.T) {
			service, _, provider, compactor, cmd := discussBudgetRecoveryFixture(t)
			configureDiscussLifecycle(service)
			cmd.DiscussRecoveryExhausted = true
			cmd.DiscussContextOverflow = overflow
			handle, err := service.StartTurn(t.Context(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for event := range handle.Events() {
				if event.Kind == turn.DiscussEventRecompose {
					t.Error("exhausted recovery requested another compaction")
				}
			}
			failed := false
			for err := range handle.Errs() {
				failed = failed || err != nil
			}
			if !failed || len(compactor.configs) != 0 || provider.callCount() != 0 {
				t.Fatalf("failed=%v compactions=%d provider=%d", failed, len(compactor.configs), provider.callCount())
			}
		})
	}
}

func TestExternalDiscussExhaustedRecoveryOnlyAttemptsAdmission(t *testing.T) {
	service, compactor := newControllerPolicyService(t, nil)
	service.SetContextAbsoluteMaxTokens(1000)
	_, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID, discussRecoveryExhausted: true,
		discussMessages: []turn.DiscussMessage{
			{Role: "assistant", Content: strings.Repeat("s", 8000), CompactionArtifactID: "summary"},
			{Role: "user", Content: "current"},
		},
	}, "", 0)
	if err == nil || len(compactor.configs) != 0 {
		t.Fatalf("err=%v compactions=%d", err, len(compactor.configs))
	}
}
