package application

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
)

const (
	syncCompactBotID    = "00000000-0000-0000-0000-000000000453"
	syncCompactThreadID = "00000000-0000-0000-0000-000000000454"
)

func TestDiscussBatchThatDoesNotFitRunsWholeWithoutPreTurnCompaction(t *testing.T) {
	for _, runtimeType := range []string{sessionpkg.RuntimeModel, sessionpkg.RuntimeACPAgent} {
		service, runner := newControllerPolicyService(t)
		var messages []turn.DiscussMessage
		for _, id := range []string{"a", "b", "c", "d", "e"} {
			messages = append(messages, turn.DiscussMessage{Role: "user", Content: strings.Repeat(id, 1000), Source: &turn.ContextMessageSource{Kind: "external", ID: id, Current: true}})
		}
		fired, err := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
			BotID: syncCompactBotID, ThreadID: syncCompactThreadID, DiscussMessages: messages,
			DiscussOmittedSources: []turn.ContextMessageSource{{Kind: "external", ID: "z", Current: true}},
		}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000, RuntimeType: runtimeType}, "recovery")
		if err != nil || fired || len(runner.configs) != 0 {
			t.Fatalf("runtime=%s: fired=%v err=%v calls=%d, want the run's budget check to compact it", runtimeType, fired, err, len(runner.configs))
		}
	}
}

func TestDiscussCurrentInputAloneTooLargeDoesNotRunCompaction(t *testing.T) {
	service, runner := newControllerPolicyService(t)
	fired, err := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussMessages: []turn.DiscussMessage{{Role: "user", Content: strings.Repeat("c", 4004)}},
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
	if err != nil || fired || len(runner.configs) != 0 {
		t.Fatalf("irreducible current input ran compaction: fired=%v calls=%d", fired, len(runner.configs))
	}
	fired, err = service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 3000, DiscussCurrentTokens: 1200,
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "smaller-model-window")
	if err != nil || fired || len(runner.configs) != 0 {
		t.Fatal("Channel metadata did not respect the smaller resolved model budget")
	}
}

func TestDiscussAdmissionOverflowRunsRecovery(t *testing.T) {
	service, runner := newControllerPolicyService(t)
	fired, err := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 500,
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
	if err != nil || !fired || len(runner.configs) != 1 || runner.configs[0].HistoryBudgetTokens != 500 {
		t.Fatalf("rejected context did not enter recovery: fired=%v configs=%+v", fired, runner.configs)
	}
}

func TestDiscussRecoveryHonorsCancellation(t *testing.T) {
	service, runner := newControllerPolicyService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fired, err := service.maybeSyncCompactDiscuss(ctx, turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 500,
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
	if err != nil || fired || len(runner.configs) != 0 {
		t.Fatalf("cancelled recovery ran: configs=%+v", runner.configs)
	}
}

func TestMaybeSyncCompactDiscussFittingInputNoop(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t)
	fired, err := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: []turn.DiscussMessage{{Role: "user", Content: "small"}},
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "run-1")

	if err != nil || fired || len(runner.configs) != 0 {
		t.Fatalf("fitting input must not compact: fired=%v runs=%d", fired, len(runner.configs))
	}
}

func TestMaybeSyncCompactDiscussMissingWindowUsesAbsoluteCap(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t)
	service.SetContextAbsoluteMaxTokens(1000)
	fired, err := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 500,
	}, ResolveRunConfigResult{}, "run-1")

	if err != nil || !fired || len(runner.configs) != 1 || runner.configs[0].HistoryBudgetTokens != 500 {
		t.Fatalf("a missing model window must fall back to the absolute cap: fired=%v configs=%+v", fired, runner.configs)
	}
}
