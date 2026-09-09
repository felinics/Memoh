package application

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
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
