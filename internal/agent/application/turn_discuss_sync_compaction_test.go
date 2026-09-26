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

// syncCompactPressureMessages composes ~1000 estimator tokens of raw history,
// which sits above the 75% hard threshold of a 1000-token budget.
func syncCompactPressureMessages() []turn.DiscussMessage {
	return []turn.DiscussMessage{
		{Role: "user", Content: strings.Repeat("a", 2000)},
		{Role: "assistant", Content: strings.Repeat("b", 2000)},
	}
}

func TestDiscussRecoveryTargetsSpaceRemainingAfterCurrentInput(t *testing.T) {
	for _, runtimeType := range []string{sessionpkg.RuntimeModel, sessionpkg.RuntimeACPAgent} {
		service, runner := newControllerPolicyService(t, nil)
		fired := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
			BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
			DiscussMessages: []turn.DiscussMessage{
				{Role: "user", Content: strings.Repeat("s", 1400), CompactionArtifactID: "summary"},
				{Role: "user", Content: strings.Repeat("c", 3000)},
			},
		}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000, RuntimeType: runtimeType}, "recovery")
		if !fired || len(runner.configs) != 1 || runner.configs[0].TargetTokens >= 250 {
			t.Fatalf("recovery ignored current input: runtime=%s fired=%v configs=%+v", runtimeType, fired, runner.configs)
		}
	}
}

func TestDiscussCurrentInputAloneTooLargeDoesNotRunCompaction(t *testing.T) {
	service, runner := newControllerPolicyService(t, nil)
	fired := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussMessages: []turn.DiscussMessage{{Role: "user", Content: strings.Repeat("c", 4004)}},
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
	if fired || len(runner.configs) != 0 {
		t.Fatalf("irreducible current input ran compaction: fired=%v calls=%d", fired, len(runner.configs))
	}
	fired = service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 3000, DiscussCurrentTokens: 1200,
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "smaller-model-window")
	if fired || len(runner.configs) != 0 {
		t.Fatal("Channel metadata did not respect the smaller resolved model budget")
	}
}

func TestMaybeSyncCompactDiscussDefaultsToShadow(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, nil)
	fired := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: syncCompactPressureMessages(),
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "run-1")

	if fired {
		t.Fatal("shadow mode must never request a recompose")
	}
	if len(runner.configs) != 0 {
		t.Fatalf("shadow mode ran the summarizer %d times, want 0", len(runner.configs))
	}
}

func TestDiscussAdmissionRecoveryCanRunInShadow(t *testing.T) {
	service, runner := newControllerPolicyService(t, nil)
	fired := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
		BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
		DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 500,
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
	if !fired || len(runner.configs) != 1 || !runner.configs[0].AllowFrontierFusion || runner.configs[0].HistoryBudgetTokens != 500 {
		t.Fatalf("rejected context did not enter recovery: fired=%v configs=%+v", fired, runner.configs)
	}
}

func TestDiscussRecoveryHonorsExplicitOffAndCancellation(t *testing.T) {
	for _, mode := range []string{"off", "active"} {
		service, runner := newControllerPolicyService(t, nil)
		service.SetSyncCompactionMode(mode)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if mode == "active" {
			cancel()
		}
		fired := service.maybeSyncCompactDiscuss(ctx, turn.StartTurnCommand{
			BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
			DiscussContextOverflow: true, DiscussContextTokens: 2000, DiscussCurrentTokens: 500,
		}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
		if fired || len(runner.configs) != 0 {
			t.Fatalf("disabled/cancelled recovery ran: mode=%s configs=%+v", mode, runner.configs)
		}
	}
}

func TestDiscussCompactionSeesSummaryAndDroppedHistoryPressure(t *testing.T) {
	for _, pressure := range []int{0, 3000} {
		service, runner := newControllerPolicyService(t, nil)
		service.SetSyncCompactionMode("active")
		fired := service.maybeSyncCompactDiscuss(t.Context(), turn.StartTurnCommand{
			BotID: syncCompactBotID, ThreadID: syncCompactThreadID, DiscussContextTokens: pressure,
			DiscussMessages: []turn.DiscussMessage{
				{Role: "user", Content: strings.Repeat("s", 2400), CompactionArtifactID: "summary"},
				{Role: "user", Content: strings.Repeat("r", 1200)},
			},
		}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "recovery")
		if !fired || len(runner.configs) != 1 || runner.configs[0].TotalInputTokens != max(900, pressure) {
			t.Fatalf("lost pre-selection pressure: fired=%v configs=%+v", fired, runner.configs)
		}
	}
}

func TestMaybeSyncCompactDiscussActiveCompactsAtHardThreshold(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, nil)
	service.SetSyncCompactionMode("active")
	fired := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: syncCompactPressureMessages(),
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "run-1")

	if !fired {
		t.Fatal("active mode at hard threshold must compact and request a recompose")
	}
	if len(runner.configs) != 1 {
		t.Fatalf("summarizer runs = %d, want 1", len(runner.configs))
	}
	cfg := runner.configs[0]
	if cfg.ContextWindowTokens != 1000 {
		t.Fatalf("ContextWindowTokens = %d, want 1000", cfg.ContextWindowTokens)
	}
	if cfg.TargetTokens != 400 {
		t.Fatalf("TargetTokens = %d, want 400 (default 40%% target under the soft-share cap)", cfg.TargetTokens)
	}
}

func TestMaybeSyncCompactDiscussActiveBelowThresholdNoop(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, nil)
	service.SetSyncCompactionMode("active")
	fired := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: []turn.DiscussMessage{{Role: "user", Content: "small"}},
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "run-1")

	if fired || len(runner.configs) != 0 {
		t.Fatalf("below threshold must not compact: fired=%v runs=%d", fired, len(runner.configs))
	}
}

func TestMaybeSyncCompactDiscussOffDisabled(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, nil)
	service.SetSyncCompactionMode("off")
	fired := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: syncCompactPressureMessages(),
	}, ResolveRunConfigResult{ContextBudgetMaxTokens: 1000}, "run-1")

	if fired || len(runner.configs) != 0 {
		t.Fatalf("off mode must not compact: fired=%v runs=%d", fired, len(runner.configs))
	}
}

func TestMaybeSyncCompactDiscussMissingWindowUsesAbsoluteCap(t *testing.T) {
	t.Parallel()

	service, runner := newControllerPolicyService(t, nil)
	service.SetSyncCompactionMode("active")
	service.SetContextAbsoluteMaxTokens(1000)
	fired := service.maybeSyncCompactDiscuss(context.Background(), turn.StartTurnCommand{
		BotID:           syncCompactBotID,
		ThreadID:        syncCompactThreadID,
		DiscussMessages: syncCompactPressureMessages(),
	}, ResolveRunConfigResult{}, "run-1")

	if !fired {
		t.Fatal("a missing model window must fall back to the absolute cap, not disable the backstop")
	}
	if len(runner.configs) != 1 {
		t.Fatalf("summarizer runs = %d, want 1", len(runner.configs))
	}
}
